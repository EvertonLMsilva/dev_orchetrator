package domain

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrProtocolVersionRequired   = errors.New("protocol version is required")
	ErrMessageTypeRequired       = errors.New("message type is required")
	ErrUnknownMessageType        = errors.New("unknown message type")
	ErrMessageIDRequired         = errors.New("message ID is required")
	ErrCorrelationIDRequired     = errors.New("correlation ID is required")
	ErrEnvelopeProjectIDRequired = errors.New("envelope project ID is required")
	ErrCreatedAtRequired         = errors.New("created at is required")
)

type MessageType string

type MessageID string

type CorrelationID string

type SessionID string

const (
	MessageTypeBotCommand      MessageType = "BOT_COMMAND"
	MessageTypeBotResult       MessageType = "BOT_RESULT"
	MessageTypePlannerDecision MessageType = "PLANNER_DECISION"
	MessageTypeCodexTask       MessageType = "CODEX_TASK"
	MessageTypeCodexResult     MessageType = "CODEX_RESULT"
	MessageTypeApprovalRequest MessageType = "APPROVAL_REQUEST"
	MessageTypeApprovalResult  MessageType = "APPROVAL_RESULT"
	MessageTypePlannerHandoff  MessageType = "PLANNER_HANDOFF"
	MessageTypeSessionStarted  MessageType = "SESSION_STARTED"
	MessageTypeSessionClosed   MessageType = "SESSION_CLOSED"
)

type Envelope struct {
	ProtocolVersion string
	MessageType     MessageType
	MessageID       MessageID
	CorrelationID   CorrelationID
	ProjectID       ProjectID
	TaskID          *TaskID
	SessionID       *SessionID
	CreatedAt       time.Time
	Payload         any
}

func (e Envelope) Validate() error {
	if strings.TrimSpace(e.ProtocolVersion) == "" {
		return ErrProtocolVersionRequired
	}
	if strings.TrimSpace(string(e.MessageType)) == "" {
		return ErrMessageTypeRequired
	}
	switch e.MessageType {
	case MessageTypeBotCommand, MessageTypeBotResult, MessageTypePlannerDecision,
		MessageTypeCodexTask, MessageTypeCodexResult, MessageTypeApprovalRequest,
		MessageTypeApprovalResult, MessageTypePlannerHandoff, MessageTypeSessionStarted,
		MessageTypeSessionClosed:
	default:
		return ErrUnknownMessageType
	}
	if strings.TrimSpace(string(e.MessageID)) == "" {
		return ErrMessageIDRequired
	}
	if strings.TrimSpace(string(e.CorrelationID)) == "" {
		return ErrCorrelationIDRequired
	}
	if strings.TrimSpace(string(e.ProjectID)) == "" {
		return ErrEnvelopeProjectIDRequired
	}
	if e.CreatedAt.IsZero() {
		return ErrCreatedAtRequired
	}
	return nil
}
