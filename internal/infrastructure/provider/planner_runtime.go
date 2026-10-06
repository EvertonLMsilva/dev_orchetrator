package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"time"
	"unicode/utf8"

	"dev-orchestrator/internal/infrastructure"
)

type plannerContainer interface {
	RuntimeHomeContainer
	Infer(context.Context, infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error)
}

// ToolFreeCodexPlannerRuntime reuses the existing authenticated RuntimeHome lease.
// It never owns login or selects caller-provided container/process capabilities.
type ToolFreeCodexPlannerRuntime struct {
	home         *CodexRuntimeHome
	newContainer func() (plannerContainer, error)
}

func NewToolFreeCodexPlannerRuntime(home *CodexRuntimeHome) *ToolFreeCodexPlannerRuntime {
	return &ToolFreeCodexPlannerRuntime{home: home, newContainer: func() (plannerContainer, error) { return infrastructure.NewPlannerHostContainer() }}
}

var _ infrastructure.PlannerRuntime = (*ToolFreeCodexPlannerRuntime)(nil)

func (r *ToolFreeCodexPlannerRuntime) Plan(ctx context.Context, request infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error) {
	empty := infrastructure.PlannerRuntimeResult{}
	if err := request.Validate(); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if r == nil || r.home == nil || r.newContainer == nil {
		return empty, infrastructure.ErrPlannerRuntimeFailure
	}
	ctx, cancel := context.WithTimeout(ctx, request.Limits.Timeout)
	defer cancel()
	container, err := r.newContainer()
	if err != nil {
		return empty, infrastructure.ErrPlannerRuntimeFailure
	}
	lease, err := r.home.Materialize(ctx, container)
	if err != nil {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		if container.Destroy(cleanup) != nil {
			return empty, infrastructure.ErrPlannerRuntimeFailure
		}
		return empty, plannerFailure(ctx)
	}
	result, err := container.Infer(ctx, request)
	if err != nil || ctx.Err() != nil || len(result.StructuredOutput) > request.Limits.MaxOutputBytes || !utf8.Valid(result.StructuredOutput) || !json.Valid(result.StructuredOutput) || bytes.Equal(bytes.TrimSpace(result.StructuredOutput), []byte("null")) {
		clear(result.StructuredOutput)
		cleanupErr := lease.Abort(context.Background())
		if cleanupErr != nil {
			return empty, infrastructure.ErrPlannerRuntimeFailure
		}
		return empty, plannerFailure(ctx)
	}
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCleanup()
	if lease.Finish(cleanup) != nil || ctx.Err() != nil {
		clear(result.StructuredOutput)
		return empty, plannerFailure(ctx)
	}
	return result, nil
}
func plannerFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return infrastructure.ErrPlannerRuntimeFailure
}
