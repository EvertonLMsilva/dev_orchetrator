package ports

import (
	"context"
	"errors"
)

// PlannerFailure carries only a closed stage and optional cancellation sentinel.
// Raw provider errors, payloads and credentials must never be retained here.
type PlannerFailure struct {
	stage        string
	cancellation error
}

func NewPlannerFailure(stage string, cause error) *PlannerFailure {
	switch stage {
	case "NEW_CONTAINER", "MATERIALIZE", "PREPARE", "HOST_START", "INFER", "TIMEOUT", "CANCELLED", "CLEANUP", "PLANNER":
	default:
		stage = "PLANNER"
	}
	f := &PlannerFailure{stage: stage}
	if errors.Is(cause, context.DeadlineExceeded) {
		f.cancellation = context.DeadlineExceeded
	} else if errors.Is(cause, context.Canceled) {
		f.cancellation = context.Canceled
	}
	return f
}
func (f *PlannerFailure) Error() string        { return "planner failed: " + f.stage }
func (f *PlannerFailure) FailureStage() string { return f.stage }
func (f *PlannerFailure) Unwrap() error        { return f.cancellation }
