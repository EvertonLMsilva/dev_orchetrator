package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
)

var ErrExecutorSpecUnavailable = errors.New("trusted executor task spec unavailable")

type ExecutorTaskIdentity struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
}

// ExecutorTaskSpecResolver resolves typed authority, never planner prose.
type ExecutorTaskSpecResolver interface {
	Resolve(context.Context, ports.PlannerDecision, PlannerContext) (ports.ExecutorTaskSpec, error)
}

// Specs is trusted caller configuration keyed by both canonical identities.
type TrustedExecutorTaskSpecResolver struct {
	Specs map[ExecutorTaskIdentity]ports.ExecutorTaskSpec
}

func (r TrustedExecutorTaskSpecResolver) Resolve(ctx context.Context, d ports.PlannerDecision, c PlannerContext) (ports.ExecutorTaskSpec, error) {
	if err := ctx.Err(); err != nil {
		return ports.ExecutorTaskSpec{}, err
	}
	if err := d.Validate(); err != nil {
		return ports.ExecutorTaskSpec{}, err
	}
	if d.Type != ports.PlannerDecisionPrepareExecutor {
		return ports.ExecutorTaskSpec{}, ports.ErrInvalidPlannerDecision
	}
	if c.Project.ID != d.ProjectID || c.CurrentTask.ProjectID != d.ProjectID || c.CurrentTask.ID != d.TaskID {
		return ports.ExecutorTaskSpec{}, ErrPlannerTaskIdentityMismatch
	}
	s, ok := r.Specs[ExecutorTaskIdentity{d.ProjectID, d.TaskID}]
	if !ok {
		return ports.ExecutorTaskSpec{}, ErrExecutorSpecUnavailable
	}
	if err := s.Validate(); err != nil {
		return ports.ExecutorTaskSpec{}, err
	}
	return copyExecutorSpec(s), nil
}
func copyExecutorSpec(s ports.ExecutorTaskSpec) ports.ExecutorTaskSpec {
	s.Scope = append([]string(nil), s.Scope...)
	s.Constraints = append([]string(nil), s.Constraints...)
	s.AcceptanceCriteria = append([]string(nil), s.AcceptanceCriteria...)
	return s
}
