package application

import (
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

// CodexResultMetadata is explicit caller input; normalization creates no IDs
// or timestamps. ProjectID and TaskID come exclusively from ExecutorResult.
type CodexResultMetadata struct {
	ProtocolVersion string
	MessageID       domain.MessageID
	CorrelationID   domain.CorrelationID
	SessionID       *domain.SessionID
	CreatedAt       time.Time
}

type ExecutorResultNormalizer struct{}

// Normalize prepares only a canonical contract; it invokes no execution,
// transport or workflow. Contract errors return an empty envelope and error,
// while FAILED remains a valid execution outcome.
func (ExecutorResultNormalizer) Normalize(r ports.ExecutorResult, m CodexResultMetadata) (domain.Envelope, error) {
	if err := r.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	taskID := r.TaskID
	e := domain.Envelope{
		ProtocolVersion: m.ProtocolVersion,
		MessageType:     domain.MessageTypeCodexResult,
		MessageID:       m.MessageID,
		CorrelationID:   m.CorrelationID,
		ProjectID:       r.ProjectID,
		TaskID:          &taskID,
		CreatedAt:       m.CreatedAt,
		Payload:         CodexResult{Outcome: r.Outcome, Summary: r.Summary},
	}
	if m.SessionID != nil {
		sessionID := *m.SessionID
		e.SessionID = &sessionID
	}
	if err := ValidateCodexResultEnvelope(e); err != nil {
		return domain.Envelope{}, err
	}
	return e, nil
}
