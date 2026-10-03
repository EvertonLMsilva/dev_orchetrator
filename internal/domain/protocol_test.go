package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func validEnvelope() Envelope {
	return Envelope{ProtocolVersion: " custom-version ", MessageType: MessageTypeBotCommand,
		MessageID: " message ", CorrelationID: " correlation ", ProjectID: " project ",
		CreatedAt: time.Date(1900, 1, 1, 0, 0, 0, 0, time.FixedZone("custom", 3600))}
}

func TestEnvelopeValidateRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Envelope)
		want   error
	}{
		{"valid minimum", func(e *Envelope) {}, nil},
		{"version empty", func(e *Envelope) { e.ProtocolVersion = "" }, ErrProtocolVersionRequired},
		{"version whitespace", func(e *Envelope) { e.ProtocolVersion = " \t\n" }, ErrProtocolVersionRequired},
		{"type empty", func(e *Envelope) { e.MessageType = "" }, ErrMessageTypeRequired},
		{"type whitespace", func(e *Envelope) { e.MessageType = " \t\n" }, ErrMessageTypeRequired},
		{"type unknown", func(e *Envelope) { e.MessageType = "UNKNOWN" }, ErrUnknownMessageType},
		{"type padded", func(e *Envelope) { e.MessageType = " BOT_COMMAND " }, ErrUnknownMessageType},
		{"message empty", func(e *Envelope) { e.MessageID = "" }, ErrMessageIDRequired},
		{"message whitespace", func(e *Envelope) { e.MessageID = " \t\n" }, ErrMessageIDRequired},
		{"correlation empty", func(e *Envelope) { e.CorrelationID = "" }, ErrCorrelationIDRequired},
		{"correlation whitespace", func(e *Envelope) { e.CorrelationID = " \t\n" }, ErrCorrelationIDRequired},
		{"project empty", func(e *Envelope) { e.ProjectID = "" }, ErrEnvelopeProjectIDRequired},
		{"project whitespace", func(e *Envelope) { e.ProjectID = " \t\n" }, ErrEnvelopeProjectIDRequired},
		{"created zero", func(e *Envelope) { e.CreatedAt = time.Time{} }, ErrCreatedAtRequired},
		{"created future", func(e *Envelope) { e.CreatedAt = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := validEnvelope()
			tt.change(&e)
			before := e
			if err := e.Validate(); !errors.Is(err, tt.want) {
				t.Fatalf("Validate() = %v, want %v", err, tt.want)
			}
			if !reflect.DeepEqual(e, before) {
				t.Fatal("Validate modified envelope")
			}
		})
	}
}

func TestEnvelopeValidateKnownMessageTypes(t *testing.T) {
	for _, messageType := range []MessageType{MessageTypeBotCommand, MessageTypeBotResult,
		MessageTypePlannerDecision, MessageTypeCodexTask, MessageTypeCodexResult,
		MessageTypeApprovalRequest, MessageTypeApprovalResult, MessageTypePlannerHandoff,
		MessageTypeSessionStarted, MessageTypeSessionClosed} {
		t.Run(string(messageType), func(t *testing.T) {
			e := validEnvelope()
			e.MessageType = messageType
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEnvelopeValidateOptionalFields(t *testing.T) {
	for _, payload := range []any{nil, "text", 42, []int{1, 2}, map[string]any{"items": []string{"a"}}, struct{ Name string }{"example"}} {
		for _, present := range []bool{false, true} {
			e := validEnvelope()
			e.Payload = payload
			if present {
				taskID := TaskID(" \t")
				sessionID := SessionID("")
				e.TaskID, e.SessionID = &taskID, &sessionID
			}
			before := e
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e, before) {
				t.Fatal("Validate modified envelope")
			}
			if present && (*e.TaskID != " \t" || *e.SessionID != "") {
				t.Fatal("Validate modified optional IDs")
			}
		}
	}
}

func TestEnvelopeValidateErrorOrder(t *testing.T) {
	e := Envelope{}
	steps := []struct {
		want error
		fix  func()
	}{
		{ErrProtocolVersionRequired, func() { e.ProtocolVersion = "arbitrary" }},
		{ErrMessageTypeRequired, func() { e.MessageType = "UNKNOWN" }},
		{ErrUnknownMessageType, func() { e.MessageType = MessageTypeBotResult }},
		{ErrMessageIDRequired, func() { e.MessageID = "message" }},
		{ErrCorrelationIDRequired, func() { e.CorrelationID = "correlation" }},
		{ErrEnvelopeProjectIDRequired, func() { e.ProjectID = "project" }},
		{ErrCreatedAtRequired, func() { e.CreatedAt = time.Now() }},
	}
	for _, step := range steps {
		if err := e.Validate(); !errors.Is(err, step.want) {
			t.Fatalf("Validate() = %v, want %v", err, step.want)
		}
		step.fix()
	}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageTypeValues(t *testing.T) {
	tests := []struct {
		value MessageType
		want  string
	}{
		{MessageTypeBotCommand, "BOT_COMMAND"},
		{MessageTypeBotResult, "BOT_RESULT"},
		{MessageTypePlannerDecision, "PLANNER_DECISION"},
		{MessageTypeCodexTask, "CODEX_TASK"},
		{MessageTypeCodexResult, "CODEX_RESULT"},
		{MessageTypeApprovalRequest, "APPROVAL_REQUEST"},
		{MessageTypeApprovalResult, "APPROVAL_RESULT"},
		{MessageTypePlannerHandoff, "PLANNER_HANDOFF"},
		{MessageTypeSessionStarted, "SESSION_STARTED"},
		{MessageTypeSessionClosed, "SESSION_CLOSED"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.value) != tt.want {
				t.Fatalf("MessageType = %q, want %q", tt.value, tt.want)
			}
		})
	}
}

func TestEnvelopeRepresentsAllFields(t *testing.T) {
	taskID := TaskID("task-1")
	sessionID := SessionID("session-1")
	createdAt := time.Now()
	payload := map[string]any{"count": 42, "items": []string{"a", "b"}}
	envelope := Envelope{
		ProtocolVersion: "custom-version",
		MessageType:     MessageTypeCodexTask,
		MessageID:       MessageID("message-1"),
		CorrelationID:   CorrelationID("correlation-1"),
		ProjectID:       ProjectID("project-1"),
		TaskID:          &taskID,
		SessionID:       &sessionID,
		CreatedAt:       createdAt,
		Payload:         payload,
	}
	if envelope.ProtocolVersion != "custom-version" || envelope.MessageType != MessageTypeCodexTask {
		t.Fatal("protocol fields were not preserved")
	}
	if envelope.MessageID != MessageID("message-1") || envelope.CorrelationID != CorrelationID("correlation-1") || envelope.ProjectID != ProjectID("project-1") {
		t.Fatal("typed IDs were not preserved")
	}
	if envelope.TaskID != &taskID || *envelope.TaskID != TaskID("task-1") {
		t.Fatal("present TaskID was not preserved")
	}
	if envelope.SessionID != &sessionID || *envelope.SessionID != SessionID("session-1") {
		t.Fatal("present SessionID was not preserved")
	}
	if envelope.CreatedAt != createdAt {
		t.Fatal("CreatedAt was not preserved exactly")
	}
	if !reflect.DeepEqual(envelope.Payload, payload) {
		t.Fatal("payload was not preserved")
	}
}

func TestEnvelopeOptionalIDsCanBeAbsent(t *testing.T) {
	envelope := Envelope{}
	if envelope.TaskID != nil {
		t.Fatal("TaskID should be absent")
	}
	if envelope.SessionID != nil {
		t.Fatal("SessionID should be absent")
	}
}

func TestEnvelopePayloadAcceptsArbitraryValues(t *testing.T) {
	values := []any{nil, "text", 42, []int{1, 2}, struct{ Name string }{"example"}}
	for _, value := range values {
		envelope := Envelope{Payload: value}
		if !reflect.DeepEqual(envelope.Payload, value) {
			t.Errorf("Payload = %#v, want %#v", envelope.Payload, value)
		}
	}
}
