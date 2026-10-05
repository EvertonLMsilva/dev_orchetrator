package application

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func canonicalResultForTest() domain.Envelope {
	task := domain.TaskID("task")
	return domain.Envelope{ProtocolVersion: "v1", MessageType: domain.MessageTypeCodexResult,
		MessageID: "message", CorrelationID: "correlation", ProjectID: "project", TaskID: &task,
		CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
		Payload:   CodexResult{Outcome: ports.ExecutorOutcomeDone, Summary: "BLOCKED"}}
}

func TestCodexResultContract(t *testing.T) {
	for _, outcome := range []ports.ExecutorOutcome{ports.ExecutorOutcomeDone, ports.ExecutorOutcomeBlocked, ports.ExecutorOutcomeFailed} {
		e := canonicalResultForTest()
		e.Payload = CodexResult{Outcome: outcome, Summary: "  FAILED; $(command)\ntexto válido  "}
		before := e
		if err := ValidateCodexResultEnvelope(e); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e, before) {
			t.Fatal("validation changed data")
		}
	}
	// No message-level summary limit exists; do not introduce a global policy.
	if err := (CodexResult{Outcome: ports.ExecutorOutcomeDone, Summary: strings.Repeat("x", 128*1024)}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexResultRejectsMalformedContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*domain.Envelope)
	}{
		{"version", func(e *domain.Envelope) { e.ProtocolVersion = " " }},
		{"type", func(e *domain.Envelope) { e.MessageType = domain.MessageTypeBotResult }},
		{"message", func(e *domain.Envelope) { e.MessageID = "" }},
		{"correlation", func(e *domain.Envelope) { e.CorrelationID = "\t" }},
		{"project", func(e *domain.Envelope) { e.ProjectID = " " }},
		{"task absent", func(e *domain.Envelope) { e.TaskID = nil }},
		{"task empty", func(e *domain.Envelope) { *e.TaskID = "" }},
		{"task blank", func(e *domain.Envelope) { *e.TaskID = "\u2003" }},
		{"timestamp", func(e *domain.Envelope) { e.CreatedAt = time.Time{} }},
		{"payload absent", func(e *domain.Envelope) { e.Payload = nil }},
		{"payload map", func(e *domain.Envelope) { e.Payload = map[string]any{"Outcome": "DONE", "Summary": "text"} }},
		{"payload pointer", func(e *domain.Envelope) { p := e.Payload.(CodexResult); e.Payload = &p }},
		{"outcome", func(e *domain.Envelope) { e.Payload = CodexResult{Outcome: "SUCCESS", Summary: "text"} }},
		{"summary", func(e *domain.Envelope) {
			e.Payload = CodexResult{Outcome: ports.ExecutorOutcomeDone, Summary: "\t\n\u2003"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := canonicalResultForTest()
			tc.change(&e)
			if err := ValidateCodexResultEnvelope(e); err == nil {
				t.Fatal("malformed contract accepted")
			}
		})
	}
	for _, outcome := range []ports.ExecutorOutcome{"", "SUCCESS", "ERROR", "INTERRUPTED", "CANCELLED", "UNKNOWN", " DONE "} {
		if (CodexResult{Outcome: outcome, Summary: "text"}).Validate() == nil {
			t.Fatalf("invalid outcome %q accepted", outcome)
		}
	}
	if (CodexResult{}).Validate() == nil {
		t.Fatal("zero payload accepted")
	}
}

func TestCodexResultProviderIndependentShape(t *testing.T) {
	typ := reflect.TypeOf(CodexResult{})
	if typ.NumField() != 2 || typ.Field(0).Name != "Outcome" || typ.Field(0).Type != reflect.TypeOf(ports.ExecutorOutcomeDone) || typ.Field(1).Name != "Summary" || typ.Field(1).Type.Kind() != reflect.String {
		t.Fatal("payload must contain only typed outcome and summary; identities belong to Envelope")
	}
}
