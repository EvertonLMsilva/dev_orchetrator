package composition

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type scriptedRuntime struct {
	outputs  []string
	requests []ports.PlannerRequest
	block    chan struct{}
	entered  chan struct{}
	once     sync.Once
}

func (r *scriptedRuntime) Plan(ctx context.Context, in infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error) {
	var req ports.PlannerRequest
	if json.Unmarshal(in.Input, &req) != nil {
		return infrastructure.PlannerRuntimeResult{}, errors.New("bad request")
	}
	r.requests = append(r.requests, req)
	if r.entered != nil {
		r.once.Do(func() { close(r.entered) })
	}
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
			return infrastructure.PlannerRuntimeResult{}, ctx.Err()
		}
	}
	if len(r.outputs) < len(r.requests) {
		return infrastructure.PlannerRuntimeResult{}, errors.New("excess call")
	}
	return infrastructure.PlannerRuntimeResult{StructuredOutput: []byte(r.outputs[len(r.requests)-1])}, nil
}
func TestCompositionEvidenceAndRestart(t *testing.T) {
	c := testConfig(t)
	r := &scriptedRuntime{outputs: []string{`{"type":"REQUEST_EVIDENCE","reason":"read","evidenceKind":"READ_FILE"}`, `{"type":"BLOCK","reason":"secret provider text"}`}}
	s, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), func(string) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	input := application.ConversationInput{Source: c.Routes[0].Source, Text: "private full prompt"}
	response := s.Handle(context.Background(), input)
	if response.Status != "BLOCKED" || !strings.Contains(response.Message, "Referência:") || len(r.requests) != 2 || len(r.requests[1].Evidence) != 1 {
		t.Fatalf("response=%#v rounds=%d", response, len(r.requests))
	}
	if strings.Contains(response.Message, "secret") {
		t.Fatal("provider text leaked")
	}
	data, e := os.ReadFile(filepath.Join(c.StateDir, "audit.json"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(data), input.Text) || strings.Contains(string(data), "public evidence") || strings.Contains(string(data), "secret provider") {
		t.Fatal("audit leaked")
	}
	if e = s.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	s2, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(&scriptedRuntime{outputs: []string{`{"type":"BLOCK","reason":"blocked"}`}}), func(string) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Shutdown(context.Background())
	tasks, e := s2.tasks.FindByProject(context.Background(), "p")
	if e != nil || len(tasks) != 1 || tasks[0].Status != "BLOCKED" {
		t.Fatalf("restart: %#v %v", tasks, e)
	}
	response2 := s2.Handle(context.Background(), input)
	if response2.Message == response.Message {
		t.Fatal("correlation reused")
	}
}
func TestCompositionReadOnlyAndLimits(t *testing.T) {
	for _, outputs := range [][]string{
		{`{"type":"PREPARE_EXECUTOR","reason":"write"}`},
		{`{"type":"REQUEST_EVIDENCE","reason":"read","evidenceKind":"READ_FILE"}`, `{"type":"PREPARE_EXECUTOR","reason":"write"}`},
		{`{"type":"REQUEST_EVIDENCE","reason":"tests","evidenceKind":"RUN_TESTS"}`},
		{`{"type":"REQUEST_EVIDENCE","reason":"read","evidenceKind":"READ_FILE"}`, `{"type":"REQUEST_EVIDENCE","reason":"again","evidenceKind":"READ_FILE"}`},
	} {
		c := testConfig(t)
		r := &scriptedRuntime{outputs: outputs}
		s, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), func(string) error { return nil })
		if e != nil {
			t.Fatal(e)
		}
		response := s.Handle(context.Background(), application.ConversationInput{Source: c.Routes[0].Source, Text: "intent"})
		if response.Status != "BLOCKED" && response.Status != "LIMITED" {
			t.Fatalf("unexpected result %#v", response)
		}
		tasks, _ := s.tasks.FindByProject(context.Background(), "p")
		if len(tasks) != 1 || tasks[0].Status != "BLOCKED" {
			t.Fatal("unsafe state", tasks)
		}
		if len(r.requests) > 2 {
			t.Fatal("unbounded rounds")
		}
		if e = s.Shutdown(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
}
func TestCompositionAdmissionCancellationAndLease(t *testing.T) {
	c := testConfig(t)
	r := &scriptedRuntime{block: make(chan struct{}), entered: make(chan struct{}), outputs: []string{`{"type":"BLOCK","reason":"blocked"}`}}
	s, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), func(string) error { return nil })
	if e != nil {
		t.Fatal(e)
	}
	if _, e = buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), func(string) error { return nil }); e == nil {
		t.Fatal("second process allowed")
	}
	input := application.ConversationInput{Source: c.Routes[0].Source, Text: "intent"}
	done := make(chan application.ConversationResponse, 1)
	go func() { done <- s.Handle(context.Background(), input) }()
	select {
	case <-r.entered:
	case <-time.After(time.Second):
		t.Fatal("did not enter")
	}
	if got := s.Handle(context.Background(), input); got.Status != "BUSY" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e = s.Shutdown(ctx); e != nil {
		t.Fatal(e)
	}
	if got := <-done; got.Status == "ACCEPTED" {
		t.Fatal("cancellation succeeded")
	}
	if got := s.Handle(context.Background(), input); got.Status != "REJECTED" {
		t.Fatal("admitted after shutdown")
	}
}
func TestCompositionRejectsMutableWorkspace(t *testing.T) {
	c := testConfig(t)
	if _, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(&scriptedRuntime{}), requireReadOnly); e == nil {
		t.Fatal("mutable mount accepted")
	}
}
