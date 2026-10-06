package infrastructure

import "dev-orchestrator/internal/ports"

// PlannerWriteDispatcher forwards only policy-approved symbolic proposals.
// It has no integration with real Codex effects or real WRITE.
type PlannerWriteDispatcher struct {
	executor ports.WriteExecutor
}

func NewPlannerWriteDispatcher(executor ports.WriteExecutor) *PlannerWriteDispatcher {
	return &PlannerWriteDispatcher{executor: executor}
}

func (d *PlannerWriteDispatcher) Dispatch(review ports.PlannerWriteReview, operation ports.PlannerWriteOperation) (ports.PlannerWriteReview, error) {
	decision := AuthorizePlannerWrite(review, operation)
	if decision.Decision != "ALLOW" {
		return decision, nil
	}
	return decision, d.executor.Execute(operation)
}
