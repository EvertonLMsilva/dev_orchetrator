package provider

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
	"testing"
	"time"
)

type candidateRuntime struct {
	output []byte
	fail   error
	calls  int
}

func (r *candidateRuntime) Plan(ctx context.Context, q infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error) {
	r.calls++
	if err := q.Validate(); err != nil {
		return infrastructure.PlannerRuntimeResult{}, err
	}
	return infrastructure.PlannerRuntimeResult{StructuredOutput: r.output}, r.fail
}
func TestConcreteCandidateStrictOutput(t *testing.T) {
	q := ports.CandidateGenerationRequest{Context: domain.CandidateContext{ProjectID: "p", TaskID: "t", CorrelationID: "c"}, Objective: "create note.txt", WriteTargets: []string{"note.txt"}, SchemaVersion: 1}
	good := []byte(`{"Edits":[{"Operation":"CREATE","Target":"note.txt","Content":"hello\n"}]}`)
	r := &candidateRuntime{output: good}
	g := &ToolFreeCandidateGenerator{runtime: r}
	if _, err := g.Generate(context.Background(), q); err != nil {
		t.Fatal(err)
	}
	r.output = []byte(`{"Edits":[{"Content":"hello\n","Target":"note.txt","Operation":"CREATE"}]}`)
	if _, err := g.Generate(context.Background(), q); err != nil {
		t.Fatal("field order", err)
	}
	for _, bad := range [][]byte{[]byte(`{"ALLOW":true}`), []byte(`null`), append(good, good...), []byte(`{"Edits":[],"Edits":[]}`), []byte(`{"Edits":[{"Operation":"CREATE","Target":"note.txt","Content":"a","Content":"b"}]}`), []byte(`{"Edits":[{"Operation":"CREATE","Target":".git/config","Content":"a"}]}`), []byte(`{"Edits":[{"Operation":"CREATE","Target":"note.txt","Content":null}]}`)} {
		r.output = bad
		if _, err := g.Generate(context.Background(), q); err == nil {
			t.Fatal("malformed output accepted")
		}
	}
	r.output = good
	r.fail = context.DeadlineExceeded
	if _, err := g.Generate(context.Background(), q); err == nil {
		t.Fatal("provider failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	calls := r.calls
	if _, err := g.Generate(ctx, q); err == nil || r.calls != calls {
		t.Fatal("cancelled inference")
	}
	if err := NewToolFreeCandidateGenerator(nil).ProveToolFree(context.Background()); err == nil {
		t.Fatal("missing auth runtime")
	}
}
