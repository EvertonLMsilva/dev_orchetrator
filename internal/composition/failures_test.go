package composition

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompositionFailureBoundaries(t *testing.T) {
	for _, kind := range []string{"route", "evidence-missing", "evidence-output", "audit-start", "audit-finish", "task-save", "intent", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			c := testConfig(t)
			r := &scriptedRuntime{outputs: []string{`{"type":"REQUEST_EVIDENCE","reason":"read","evidenceKind":"READ_FILE"}`, `{"type":"BLOCK","reason":"blocked"}`}}
			if kind == "evidence-output" {
				c.MaxEvidenceBytes = 1
			}
			if kind == "timeout" {
				c.RequestTimeout = Duration(20 * time.Millisecond)
				r.block = make(chan struct{})
			}
			s, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), func(string) error { return nil })
			if e != nil {
				t.Fatal(e)
			}
			defer s.Shutdown(context.Background())
			input := application.ConversationInput{Source: c.Routes[0].Source, Text: "intent"}
			switch kind {
			case "route":
				input.Source.ChannelID = "999"
			case "intent":
				input.Text = string(make([]byte, c.MaxIntentBytes+1))
			case "evidence-missing":
				if e := os.Remove(filepath.Join(c.Projects[0].Workspace, "evidence.txt")); e != nil {
					t.Fatal(e)
				}
			case "audit-start":
				if e := os.Mkdir(filepath.Join(c.StateDir, "audit.json"), 0700); e != nil {
					t.Fatal(e)
				}
			case "task-save":
				if e := os.Mkdir(filepath.Join(c.StateDir, "tasks.json"), 0700); e != nil {
					t.Fatal(e)
				}
			case "audit-finish":
				r.block = make(chan struct{})
				r.entered = make(chan struct{})
			}
			var response application.ConversationResponse
			if kind == "audit-finish" {
				done := make(chan application.ConversationResponse, 1)
				go func() { done <- s.Handle(context.Background(), input) }()
				<-r.entered
				// An existing pending artifact simulates unavailable atomic replacement.
				if e := os.WriteFile(filepath.Join(c.StateDir, "audit.pending"), []byte("fixture"), 0600); e != nil {
					t.Fatal(e)
				}
				close(r.block)
				response = <-done
			} else {
				response = s.Handle(context.Background(), input)
			}
			if kind == "evidence-missing" {
				if len(r.requests) != 2 || r.requests[1].Evidence[0].BotResult.Status != "FAILED" {
					t.Fatal("safe failure evidence missing")
				}
				if response.Status != "BLOCKED" {
					t.Fatal(response)
				}
			} else if response.Status != "REJECTED" {
				t.Fatalf("failure reported success: %#v", response)
			}
			if kind == "route" || kind == "intent" || kind == "audit-start" || kind == "task-save" {
				if len(r.requests) != 0 {
					t.Fatal("provider reached")
				}
			}
		})
	}
}

type corruptTransport struct{ base application.EvidenceTransport }

func (c corruptTransport) Handle(ctx context.Context, e domain.Envelope) (domain.Envelope, error) {
	out, err := c.base.Handle(ctx, e)
	if err != nil {
		return domain.Envelope{}, err
	}
	out.CorrelationID = "other"
	return out, nil
}
func TestCompositionRejectsCorrelationBeforeSecondPlanner(t *testing.T) {
	c := testConfig(t)
	r := &scriptedRuntime{outputs: []string{`{"type":"REQUEST_EVIDENCE","reason":"read","evidenceKind":"READ_FILE"}`}}
	s, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), func(string) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer s.Shutdown(context.Background())
	// Exercise the same application coordinator with a corrupt external boundary.
	task, _ := domain.NewTask("t", "p", "Conversation request")
	task.Status = domain.TaskStatusAnalyzing
	if e = s.tasks.Save(context.Background(), task); e != nil {
		t.Fatal(e)
	}
	canonical, _ := s.builder.Build(context.Background(), "p", "t")
	o := application.NewOrchestrator(s.builder, s.planner, configuredEvidence{c.Projects}, corruptTransport{base: s.agent}, application.BotCommandMetadata{ProtocolVersion: "1.0", MessageID: "m", CorrelationID: "expected", CreatedAt: time.Now()})
	_, e = o.Run(context.Background(), application.OrchestrationInput{ProjectID: "p", TaskID: "t", Context: canonical, UserIntent: "intent"})
	if !errors.Is(e, application.ErrInvalidPlannerEvidence) || len(r.requests) != 1 {
		t.Fatalf("correlation: %v calls=%d", e, len(r.requests))
	}
}
func TestCompositionRejectsOrphanedAndCorruptStorage(t *testing.T) {
	for _, file := range []string{"audit.json", "tasks.json", "audit.pending", "tasks.pending"} {
		c := testConfig(t)
		if e := os.WriteFile(filepath.Join(c.StateDir, file), []byte(`{"invalid":"state"}`), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(&scriptedRuntime{}), func(string) error { return nil }); e == nil {
			t.Fatal("invalid persisted state accepted", file)
		}
	}
}
