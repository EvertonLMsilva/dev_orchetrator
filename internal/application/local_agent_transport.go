package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"time"
)

// BotCommand carries only the existing typed action contract.
type BotCommand struct{ Action domain.Action }
type BotResultStatus = ports.BotResultStatus
type BotError = ports.BotError
type BotResult = ports.BotResult

const (
	BotResultSuccess          = ports.BotResultSuccess
	BotResultBlocked          = ports.BotResultBlocked
	BotResultApprovalRequired = ports.BotResultApprovalRequired
	BotResultFailed           = ports.BotResultFailed
)

var ErrInvalidBotCommand = errors.New("invalid BOT_COMMAND")

// LocalAgentTransport is an in-process protocol boundary, with no capability access.
type LocalAgentTransport struct{ agent ports.LocalAgent }

func NewLocalAgentTransport(agent ports.LocalAgent) *LocalAgentTransport {
	return &LocalAgentTransport{agent: agent}
}

// Handle returns boundary errors before execution. Execution failures are safe
// BOT_RESULT payloads; cancellation is passed unchanged to LocalAgent.
func (t *LocalAgentTransport) Handle(ctx context.Context, e domain.Envelope) (domain.Envelope, error) {
	if e.Validate() != nil || e.MessageType != domain.MessageTypeBotCommand {
		return domain.Envelope{}, ErrInvalidBotCommand
	}
	command, ok := e.Payload.(BotCommand)
	if !ok || command.Action.Validate() != nil || command.Action.ProjectID != e.ProjectID || !sameTaskID(command.Action.TaskID, e.TaskID) {
		return domain.Envelope{}, ErrInvalidBotCommand
	}
	p := BotResult{ActionType: command.Action.Type, Status: BotResultFailed}
	if t.agent == nil {
		p.Error = &BotError{Code: "AGENT_UNAVAILABLE"}
	} else {
		r, err := t.agent.Execute(ctx, command.Action)
		if err != nil {
			p.Status, p.Error = botExecutionError(err)
		} else if r.Validate() != nil || r.Type != command.Action.Type {
			p.Error = &BotError{Code: "INVALID_RESULT"}
		} else {
			p.Status = BotResultSuccess
			p.Result = &r
		}
	}
	out := domain.Envelope{ProtocolVersion: e.ProtocolVersion, MessageType: domain.MessageTypeBotResult, MessageID: domain.MessageID(string(e.MessageID) + ":result"), CorrelationID: e.CorrelationID, ProjectID: e.ProjectID, CreatedAt: time.Now().UTC(), Payload: p}
	if e.TaskID != nil {
		id := *e.TaskID
		out.TaskID = &id
	}
	if e.SessionID != nil {
		id := *e.SessionID
		out.SessionID = &id
	}
	return out, nil
}
func sameTaskID(a, b *domain.TaskID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
func botExecutionError(err error) (BotResultStatus, *BotError) {
	status, code := BotResultFailed, "EXECUTION_FAILED"
	switch {
	case errors.Is(err, ErrPolicyBlocked):
		status, code = BotResultBlocked, "POLICY_BLOCKED"
	case errors.Is(err, ErrActionNotAllowed):
		status, code = BotResultBlocked, "ACTION_NOT_ALLOWED"
	case errors.Is(err, ErrPolicyRequiresApproval):
		status, code = BotResultApprovalRequired, "APPROVAL_REQUIRED"
	case errors.Is(err, ErrLocalAgentProjectNotFound):
		status, code = BotResultBlocked, "PROJECT_NOT_FOUND"
	case errors.Is(err, context.Canceled):
		code = "CANCELED"
	case errors.Is(err, context.DeadlineExceeded):
		code = "DEADLINE_EXCEEDED"
	}
	return status, &BotError{Code: code}
}
