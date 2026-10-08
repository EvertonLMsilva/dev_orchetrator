package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
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
		return empty, plannerFailure(ctx, "PLANNER")
	}
	if r == nil || r.home == nil || r.newContainer == nil {
		return empty, plannerFailure(ctx, "NEW_CONTAINER")
	}
	ctx, cancel := context.WithTimeout(ctx, request.Limits.Timeout)
	defer cancel()
	container, err := r.newContainer()
	if err != nil || container == nil {
		return empty, plannerFailure(ctx, "NEW_CONTAINER")
	}
	lease, err := r.home.Materialize(ctx, container)
	if err != nil {
		if errors.Is(err, ErrRuntimeHomeCleanup) {
			return empty, plannerFailure(ctx, "CLEANUP")
		}
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		if container.Destroy(cleanup) != nil {
			return empty, plannerFailure(ctx, "CLEANUP")
		}
		stage := "MATERIALIZE"
		if errors.Is(err, ErrRuntimeHomePreparation) {
			stage = "PREPARE"
		}
		return empty, plannerFailure(ctx, stage)
	}
	result, err := container.Infer(ctx, request)
	if err != nil || ctx.Err() != nil || len(result.StructuredOutput) > request.Limits.MaxOutputBytes || !utf8.Valid(result.StructuredOutput) || !json.Valid(result.StructuredOutput) || bytes.Equal(bytes.TrimSpace(result.StructuredOutput), []byte("null")) {
		clear(result.StructuredOutput)
		cleanupErr := lease.Abort(context.Background())
		if cleanupErr != nil {
			return empty, plannerFailure(ctx, "CLEANUP")
		}
		stage := "INFER"
		if diagnostic, ok := container.(interface{ SanitizedFailureClass() string }); ok && diagnostic.SanitizedFailureClass() == "host_start_failure" {
			stage = "HOST_START"
		}
		return empty, plannerFailure(ctx, stage)
	}
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelCleanup()
	if err := lease.Finish(cleanup); err != nil {
		clear(result.StructuredOutput)
		return empty, plannerFailure(ctx, "CLEANUP")
	}
	if ctx.Err() != nil {
		clear(result.StructuredOutput)
		return empty, plannerFailure(ctx, "INFER")
	}
	return result, nil
}
func plannerFailure(ctx context.Context, stage string) error {
	if stage != "CLEANUP" {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			stage = "TIMEOUT"
		} else if errors.Is(ctx.Err(), context.Canceled) {
			stage = "CANCELLED"
		}
	}
	return errors.Join(infrastructure.ErrPlannerRuntimeFailure, ports.NewPlannerFailure(stage, ctx.Err()))
}
