package application

import (
	"errors"
	"strings"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrInvalidCodexResult = errors.New("invalid CODEX_RESULT")

// CodexResult is the provider-independent CODEX_RESULT payload. Envelope owns
// its identities. Summary is declarative text, never commands or outcome input.
type CodexResult struct {
	Outcome ports.ExecutorOutcome
	Summary string
}

func (r CodexResult) Validate() error {
	if strings.TrimSpace(r.Summary) == "" {
		return ErrInvalidCodexResult
	}
	switch r.Outcome {
	case ports.ExecutorOutcomeDone, ports.ExecutorOutcomeBlocked, ports.ExecutorOutcomeFailed:
		return nil
	default:
		return ErrInvalidCodexResult
	}
}

// ValidateCodexResultEnvelope adds the typed payload and required task identity
// to the shared envelope contract without changing other message types.
func ValidateCodexResultEnvelope(e domain.Envelope) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.MessageType != domain.MessageTypeCodexResult || e.TaskID == nil || strings.TrimSpace(string(*e.TaskID)) == "" {
		return ErrInvalidCodexResult
	}
	payload, ok := e.Payload.(CodexResult)
	if !ok {
		return ErrInvalidCodexResult
	}
	return payload.Validate()
}
