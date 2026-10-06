package application

import (
	"context"
	"dev-orchestrator/internal/ports"
	"errors"
)

var ErrReadOnlyDenied = errors.New("read-only execution denied")

// ReadOnlyPlanner enforces the deployment profile on every planner round,
// before the orchestrator can resolve an executor spec or invoke a capability.
type ReadOnlyPlanner struct{ planner ports.Planner }

func NewReadOnlyPlanner(planner ports.Planner) *ReadOnlyPlanner { return &ReadOnlyPlanner{planner} }
func (p *ReadOnlyPlanner) Plan(ctx context.Context, r ports.PlannerRequest) (ports.PlannerDecision, error) {
	if p == nil || p.planner == nil {
		return ports.PlannerDecision{}, ErrInvalidOrchestrator
	}
	d, err := p.planner.Plan(ctx, r)
	if err != nil {
		return ports.PlannerDecision{}, err
	}
	if d.Type == ports.PlannerDecisionPrepareExecutor || (d.Type == ports.PlannerDecisionRequestEvidence && !NewReadOnlyActionAllowlist().Allows(d.EvidenceKind)) {
		return ports.PlannerDecision{}, ErrReadOnlyDenied
	}
	return d, nil
}
