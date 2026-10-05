package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
)

// EvidenceTransport is the existing BOT_COMMAND/BOT_RESULT boundary.
type EvidenceTransport interface {
	Handle(context.Context, domain.Envelope) (domain.Envelope, error)
}

var ErrInvalidOrchestrator = errors.New("orchestrator dependencies are required")

type Orchestrator struct {
	builder   *ContextBuilder
	planner   ports.Planner
	resolver  EvidenceRequestResolver
	transport EvidenceTransport
	metadata  BotCommandMetadata
}

func NewOrchestrator(builder *ContextBuilder, planner ports.Planner, resolver EvidenceRequestResolver, transport EvidenceTransport, metadata BotCommandMetadata) *Orchestrator {
	if metadata.SessionID != nil {
		id := *metadata.SessionID
		metadata.SessionID = &id
	}
	return &Orchestrator{builder: builder, planner: planner, resolver: resolver, transport: transport, metadata: metadata}
}

// Run coordinates at most one evidence execution and two planner rounds.
// Caller snapshots are checked, then replaced with canonical repository state.
func (o *Orchestrator) Run(ctx context.Context, input OrchestrationInput) (OrchestrationOutput, error) {
	if err := input.Validate(); err != nil {
		return OrchestrationOutput{}, err
	}
	if err := ctx.Err(); err != nil {
		return OrchestrationOutput{}, err
	}
	if o == nil || o.builder == nil || o.planner == nil {
		return OrchestrationOutput{}, ErrInvalidOrchestrator
	}
	canonical, err := o.builder.Build(ctx, input.ProjectID, input.TaskID)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	request := ports.PlannerRequest{ProjectID: input.ProjectID, TaskID: input.TaskID, Context: canonical}
	decision, err := o.plan(ctx, request)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	if decision.Type != ports.PlannerDecisionRequestEvidence {
		return OrchestrationOutput{Decision: decision}, nil
	}
	if o.resolver == nil || o.transport == nil {
		return OrchestrationOutput{}, ErrInvalidOrchestrator
	}
	params, err := o.resolver.Resolve(ctx, decision.EvidenceKind, canonical)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	command, err := (BotCommandBuilder{}).Build(EvidenceRequest{Decision: decision, Params: params, Metadata: o.metadata})
	if err != nil {
		return OrchestrationOutput{}, err
	}
	expected := PlannerEvidenceExpectation{ProjectID: request.ProjectID, TaskID: request.TaskID, CorrelationID: command.CorrelationID, ProtocolVersion: command.ProtocolVersion, EvidenceKind: decision.EvidenceKind}
	if command.SessionID != nil {
		sessionID := *command.SessionID
		expected.SessionID = &sessionID
	}
	result, err := o.transport.Handle(ctx, command)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	evidence, err := ConsumePlannerEvidence(result, expected)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	request.Evidence = []PlannerEvidence{evidence}
	decision, err = o.plan(ctx, request)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	return OrchestrationOutput{Decision: decision}, nil
}

func (o *Orchestrator) plan(ctx context.Context, request ports.PlannerRequest) (ports.PlannerDecision, error) {
	if err := ctx.Err(); err != nil {
		return ports.PlannerDecision{}, err
	}
	if err := request.Validate(); err != nil {
		return ports.PlannerDecision{}, err
	}
	decision, err := o.planner.Plan(ctx, request)
	if err != nil {
		return ports.PlannerDecision{}, err
	}
	if err := decision.Validate(); err != nil {
		return ports.PlannerDecision{}, err
	}
	if decision.ProjectID != request.ProjectID || decision.TaskID != request.TaskID {
		return ports.PlannerDecision{}, ErrPlannerTaskIdentityMismatch
	}
	return decision, nil
}
