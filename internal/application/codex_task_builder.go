package application

import (
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

// CodexTask is the concrete CODEX_TASK payload. Envelope owns its identities.
type CodexTask struct {
	Spec ports.ExecutorTaskSpec
}

func (t CodexTask) Validate() error { return t.Spec.Validate() }

// CodexTaskMetadata is explicit caller input; the builder creates no IDs or clock.
type CodexTaskMetadata struct {
	ProtocolVersion string
	MessageID       domain.MessageID
	CorrelationID   domain.CorrelationID
	SessionID       *domain.SessionID
	CreatedAt       time.Time
}

type CodexTaskRequest struct {
	Decision ports.PlannerDecision
	Spec     ports.ExecutorTaskSpec
	Metadata CodexTaskMetadata
}

type CodexTaskBuilder struct{}

// Build only prepares a contract. It does not execute work or change task state.
// Every failure returns an empty envelope.
func (CodexTaskBuilder) Build(r CodexTaskRequest) (domain.Envelope, error) {
	if err := r.Decision.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	if r.Decision.Type != ports.PlannerDecisionPrepareExecutor {
		return domain.Envelope{}, ports.ErrInvalidPlannerDecision
	}
	if err := r.Spec.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	// Copy collections so caller mutations cannot change the prepared authority.
	spec := r.Spec
	spec.Scope = append([]string(nil), spec.Scope...)
	spec.Constraints = append([]string(nil), spec.Constraints...)
	spec.AcceptanceCriteria = append([]string(nil), spec.AcceptanceCriteria...)
	taskID := r.Decision.TaskID
	envelope := domain.Envelope{
		ProtocolVersion: r.Metadata.ProtocolVersion,
		MessageType:     domain.MessageTypeCodexTask,
		MessageID:       r.Metadata.MessageID,
		CorrelationID:   r.Metadata.CorrelationID,
		ProjectID:       r.Decision.ProjectID,
		TaskID:          &taskID,
		CreatedAt:       r.Metadata.CreatedAt,
		Payload:         CodexTask{Spec: spec},
	}
	if r.Metadata.SessionID != nil {
		sessionID := *r.Metadata.SessionID
		envelope.SessionID = &sessionID
	}
	if err := envelope.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	return envelope, nil
}
