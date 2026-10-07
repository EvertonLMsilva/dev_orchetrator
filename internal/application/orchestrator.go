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
	builder                  *ContextBuilder
	planner                  ports.Planner
	resolver                 EvidenceRequestResolver
	transport                EvidenceTransport
	metadata                 BotCommandMetadata
	executorCycle            *ExecutorCycleConfig
	candidatePreparationOnly bool
}

// NewCandidatePlanningOrchestrator reuses canonical context and Planner round
// validation for MODEL_D. It prepares intention only; the separately confirmed
// P10 candidate/application cycle owns effects and terminal task evidence.
// No legacy Executor, LocalAgent, or effect capability is configured here.
func NewCandidatePlanningOrchestrator(builder *ContextBuilder, planner ports.Planner) *Orchestrator {
	o := NewOrchestrator(builder, planner, nil, nil, BotCommandMetadata{})
	o.candidatePreparationOnly = true
	return o
}

func NewOrchestrator(builder *ContextBuilder, planner ports.Planner, resolver EvidenceRequestResolver, transport EvidenceTransport, metadata BotCommandMetadata, executorCycle ...ExecutorCycleConfig) *Orchestrator {
	if metadata.SessionID != nil {
		id := *metadata.SessionID
		metadata.SessionID = &id
	}
	o := &Orchestrator{builder: builder, planner: planner, resolver: resolver, transport: transport, metadata: metadata}
	if len(executorCycle) == 1 {
		config := executorCycle[0]
		if config.TaskMetadata.SessionID != nil {
			id := *config.TaskMetadata.SessionID
			config.TaskMetadata.SessionID = &id
		}
		if config.ResultMetadata.SessionID != nil {
			id := *config.ResultMetadata.SessionID
			config.ResultMetadata.SessionID = &id
		}
		o.executorCycle = &config
	}
	return o
}

// Run coordinates at most one evidence execution, two planner rounds and one
// authorized executor execution. Boundary failures are never retried.
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
	request := ports.PlannerRequest{ProjectID: input.ProjectID, TaskID: input.TaskID, Context: canonical, UserIntent: input.UserIntent}
	decision, err := o.plan(ctx, request)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	if decision.Type != ports.PlannerDecisionRequestEvidence {
		return o.finish(ctx, decision, canonical)
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
	return o.finish(ctx, decision, canonical)
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

// ExecutorCycleConfig is trusted application wiring. Metadata is explicit;
// the coordinator generates no IDs, executable commands, or provider options.
type ExecutorCycleConfig struct {
	Resolver       ExecutorTaskSpecResolver
	Executor       ports.Executor
	Tasks          ports.TaskRepository
	TaskMetadata   CodexTaskMetadata
	ResultMetadata CodexResultMetadata
}

func sameExecutorCorrelation(a, b domain.Envelope) bool {
	if a.ProtocolVersion != b.ProtocolVersion || a.CorrelationID != b.CorrelationID {
		return false
	}
	if a.SessionID == nil || b.SessionID == nil {
		return a.SessionID == nil && b.SessionID == nil
	}
	return *a.SessionID == *b.SessionID
}
func (o *Orchestrator) finish(ctx context.Context, d ports.PlannerDecision, canonical PlannerContext) (OrchestrationOutput, error) {
	if o.candidatePreparationOnly {
		return OrchestrationOutput{Decision: d}, nil
	}
	if d.Type != ports.PlannerDecisionPrepareExecutor {
		return OrchestrationOutput{Decision: d}, nil
	}
	c := o.executorCycle
	if c == nil || c.Resolver == nil || c.Executor == nil || c.Tasks == nil {
		return OrchestrationOutput{}, ErrInvalidOrchestrator
	}
	if canonical.CurrentTask.Status != domain.TaskStatusAnalyzing {
		return OrchestrationOutput{}, ErrTaskNotAnalyzing
	}
	spec, err := c.Resolver.Resolve(ctx, d, canonical)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	if err := spec.Validate(); err != nil {
		return OrchestrationOutput{}, err
	}
	task, err := (CodexTaskBuilder{}).Build(CodexTaskRequest{Decision: d, Spec: spec, Metadata: c.TaskMetadata})
	if err != nil {
		return OrchestrationOutput{}, err
	}
	// Check explicit result metadata before changing state or executing. This is
	// envelope validation only, not a manufactured executor/protocol result.
	metadataCheck := task
	metadataCheck.ProtocolVersion = c.ResultMetadata.ProtocolVersion
	metadataCheck.MessageID = c.ResultMetadata.MessageID
	metadataCheck.CorrelationID = c.ResultMetadata.CorrelationID
	metadataCheck.SessionID = c.ResultMetadata.SessionID
	metadataCheck.CreatedAt = c.ResultMetadata.CreatedAt
	if err := metadataCheck.Validate(); err != nil {
		return OrchestrationOutput{}, err
	}
	if !sameExecutorCorrelation(task, metadataCheck) {
		return OrchestrationOutput{}, ErrInvalidOrchestrationContext
	}
	request := ports.ExecutorRequest{ProjectID: task.ProjectID, TaskID: *task.TaskID, Spec: copyExecutorSpec(task.Payload.(CodexTask).Spec)}
	if request.ProjectID != d.ProjectID || request.TaskID != d.TaskID {
		return OrchestrationOutput{}, ErrPlannerTaskIdentityMismatch
	}
	if err := request.Validate(); err != nil {
		return OrchestrationOutput{}, err
	}
	if _, err := NewTaskRefiner(c.Tasks).Refine(ctx, d); err != nil {
		return OrchestrationOutput{}, err
	}
	transition := func(from, to domain.TaskStatus) error {
		_, err := NewWorkflowEngine(executorCycleRepository{TaskRepository: c.Tasks, identity: ExecutorTaskIdentity{d.ProjectID, d.TaskID}, expected: from}).Transition(ctx, d.TaskID, to)
		return err
	}
	if err := transition(domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress); err != nil {
		return OrchestrationOutput{}, err
	}
	if err := ctx.Err(); err != nil {
		return OrchestrationOutput{}, err
	}
	result, err := c.Executor.Execute(ctx, request)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	if result.ProjectID != request.ProjectID || result.TaskID != request.TaskID {
		return OrchestrationOutput{}, ErrPlannerTaskIdentityMismatch
	}
	normalized, err := (ExecutorResultNormalizer{}).Normalize(result, c.ResultMetadata)
	if err != nil {
		return OrchestrationOutput{}, err
	}
	out := OrchestrationOutput{Decision: d, CodexTask: &task, CodexResult: &normalized}
	if err := out.Validate(OrchestrationInput{ProjectID: d.ProjectID, TaskID: d.TaskID, Context: canonical}); err != nil {
		return OrchestrationOutput{}, err
	}
	target := map[ports.ExecutorOutcome]domain.TaskStatus{ports.ExecutorOutcomeDone: domain.TaskStatusDone, ports.ExecutorOutcomeBlocked: domain.TaskStatusBlocked, ports.ExecutorOutcomeFailed: domain.TaskStatusFailed}[result.Outcome]
	if err := transition(domain.TaskStatusInProgress, target); err != nil {
		return OrchestrationOutput{}, err
	}
	return out, nil
}

// Guard the same repository read used by WorkflowEngine; there is no CAS or
// exactly-once guarantee. An uncertain result is never retried here.
type executorCycleRepository struct {
	ports.TaskRepository
	identity ExecutorTaskIdentity
	expected domain.TaskStatus
}

func (r executorCycleRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	task, found, err := r.TaskRepository.FindByID(ctx, id)
	if err != nil {
		return domain.Task{}, false, err
	}
	if !found {
		return domain.Task{}, false, ErrTaskNotFound
	}
	if task.ProjectID != r.identity.ProjectID || task.ID != r.identity.TaskID {
		return domain.Task{}, false, ErrPlannerTaskIdentityMismatch
	}
	if task.Status != r.expected {
		return domain.Task{}, false, domain.ErrInvalidTaskTransition
	}
	return task, true, nil
}
