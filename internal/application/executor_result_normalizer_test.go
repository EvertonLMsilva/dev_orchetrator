package application

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func resultMetadataForTest() CodexResultMetadata {
	session := domain.SessionID("session")
	return CodexResultMetadata{ProtocolVersion: "v1", MessageID: "message", CorrelationID: "correlation",
		SessionID: &session, CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func TestExecutorResultNormalizerPreservesContract(t *testing.T) {
	for _, outcome := range []ports.ExecutorOutcome{ports.ExecutorOutcomeDone, ports.ExecutorOutcomeBlocked, ports.ExecutorOutcomeFailed} {
		for _, summary := range []string{"BLOCKED", "FAILED", "  texto válido\n$(command)  "} {
			for _, sessionPresent := range []bool{false, true} {
				r := ports.ExecutorResult{ProjectID: " project ", TaskID: " task ", Outcome: outcome, Summary: summary}
				m := resultMetadataForTest()
				if !sessionPresent {
					m.SessionID = nil
				}
				got, err := (ExecutorResultNormalizer{}).Normalize(r, m)
				if err != nil || ValidateCodexResultEnvelope(got) != nil {
					t.Fatalf("invalid normalization: %#v %v", got, err)
				}
				want := domain.Envelope{ProtocolVersion: m.ProtocolVersion, MessageType: domain.MessageTypeCodexResult,
					MessageID: m.MessageID, CorrelationID: m.CorrelationID, ProjectID: r.ProjectID, TaskID: &r.TaskID,
					SessionID: m.SessionID, CreatedAt: m.CreatedAt, Payload: CodexResult{Outcome: outcome, Summary: summary}}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("normalization changed identities, metadata, summary or outcome: %#v", got)
				}
				r.TaskID = "changed"
				if sessionPresent {
					*m.SessionID = "changed"
				}
				if *got.TaskID != " task " || (sessionPresent && *got.SessionID != "session") {
					t.Fatal("normalized envelope aliases caller IDs")
				}
			}
		}
	}
}

func TestExecutorResultNormalizerRejectsInvalidSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ports.ExecutorResult)
	}{
		{"empty project", func(r *ports.ExecutorResult) { r.ProjectID = "" }},
		{"blank project", func(r *ports.ExecutorResult) { r.ProjectID = "\t\u2003" }},
		{"empty task", func(r *ports.ExecutorResult) { r.TaskID = "" }},
		{"blank task", func(r *ports.ExecutorResult) { r.TaskID = "\n " }},
		{"outcome", func(r *ports.ExecutorResult) { r.Outcome = "ERROR" }},
		{"empty summary", func(r *ports.ExecutorResult) { r.Summary = "" }},
		{"blank summary", func(r *ports.ExecutorResult) { r.Summary = "\t\u2003" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ports.ExecutorResult{ProjectID: "project", TaskID: "task", Outcome: ports.ExecutorOutcomeDone, Summary: "text"}
			tc.change(&r)
			got, err := (ExecutorResultNormalizer{}).Normalize(r, resultMetadataForTest())
			if !errors.Is(err, ports.ErrInvalidExecutorResult) || !reflect.DeepEqual(got, domain.Envelope{}) {
				t.Fatalf("expected error and empty envelope, got %#v %v", got, err)
			}
		})
	}
}

func TestExecutorResultNormalizerRejectsInvalidMetadata(t *testing.T) {
	for _, change := range []func(*CodexResultMetadata){
		func(m *CodexResultMetadata) { m.ProtocolVersion = "" },
		func(m *CodexResultMetadata) { m.MessageID = " " },
		func(m *CodexResultMetadata) { m.CorrelationID = "" },
		func(m *CodexResultMetadata) { m.CreatedAt = time.Time{} },
	} {
		m := resultMetadataForTest()
		change(&m)
		got, err := (ExecutorResultNormalizer{}).Normalize(ports.ExecutorResult{ProjectID: "project", TaskID: "task", Outcome: ports.ExecutorOutcomeFailed, Summary: "valid failure"}, m)
		if err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
			t.Fatal("metadata error masked as execution result")
		}
	}
}
