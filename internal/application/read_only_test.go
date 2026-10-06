package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"testing"
)

type readOnlyPlanner struct {
	decisions []ports.PlannerDecision
	calls     int
}

func (p *readOnlyPlanner) Plan(_ context.Context, r ports.PlannerRequest) (ports.PlannerDecision, error) {
	d := p.decisions[p.calls]
	p.calls++
	d.ProjectID = r.ProjectID
	d.TaskID = r.TaskID
	return d, nil
}
func TestReadOnlyPlannerGate(t *testing.T) {
	for _, d := range []ports.PlannerDecision{
		{Type: ports.PlannerDecisionPrepareExecutor, Reason: "prepare"},
		{Type: ports.PlannerDecisionRequestEvidence, Reason: "tests", EvidenceKind: domain.ActionTypeRunTests},
	} {
		p := &readOnlyPlanner{decisions: []ports.PlannerDecision{d}}
		got, err := NewReadOnlyPlanner(p).Plan(context.Background(), ports.PlannerRequest{})
		if !errors.Is(err, ErrReadOnlyDenied) || got.Type != "" || p.calls != 1 {
			t.Fatalf("gate: %#v %v calls=%d", got, err, p.calls)
		}
	}
}
func TestReadOnlyAllowlist(t *testing.T) {
	a := NewReadOnlyActionAllowlist()
	for _, k := range []domain.ActionType{domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitStatus, domain.ActionTypeGitDiff} {
		if !a.Allows(k) {
			t.Fatal(k)
		}
	}
	if a.Allows(domain.ActionTypeRunTests) || a.Allows("WRITE") {
		t.Fatal("effects allowed")
	}
}
