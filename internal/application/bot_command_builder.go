package application

import (
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

// EvidenceRequest binds a planner intention to the existing closed parameter
// union. It carries no execution capability.
type EvidenceRequest struct {
	Decision ports.PlannerDecision
	Params   domain.ActionParams
	Metadata BotCommandMetadata
}

// BotCommandMetadata is supplied by the caller; construction generates neither
// identities nor timestamps. Project and task come exclusively from Decision.
type BotCommandMetadata struct {
	ProtocolVersion string
	MessageID       domain.MessageID
	CorrelationID   domain.CorrelationID
	SessionID       *domain.SessionID
	CreatedAt       time.Time
}

type BotCommandBuilder struct{}

// Build prepares a P2 command without invoking the local agent. Every failure
// returns an empty envelope, so callers cannot use a partially built message.
func (BotCommandBuilder) Build(r EvidenceRequest) (domain.Envelope, error) {
	if err := r.Decision.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	if r.Decision.Type != ports.PlannerDecisionRequestEvidence {
		return domain.Envelope{}, ports.ErrInvalidPlannerDecision
	}
	taskID := r.Decision.TaskID
	action, err := domain.NewAction(r.Decision.EvidenceKind, r.Decision.ProjectID, &taskID, r.Params)
	if err != nil {
		return domain.Envelope{}, err
	}
	envelope := domain.Envelope{
		ProtocolVersion: r.Metadata.ProtocolVersion,
		MessageType:     domain.MessageTypeBotCommand,
		MessageID:       r.Metadata.MessageID,
		CorrelationID:   r.Metadata.CorrelationID,
		ProjectID:       action.ProjectID,
		TaskID:          &taskID,
		CreatedAt:       r.Metadata.CreatedAt,
		Payload:         BotCommand{Action: action},
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
