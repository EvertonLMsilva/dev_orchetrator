package ports

import (
	"context"

	"dev-orchestrator/internal/domain"
)

// ExecutorRuntime executes validated, declarative authority externally.
// Protocol outcomes use ExecutorOutcome; errors indicate boundary or
// infrastructure failures, never a FAILED protocol result.
type ExecutorRuntime interface {
	Execute(context.Context, RuntimeExecutionRequest) (RuntimeExecutionResult, error)
}

// RuntimeExecutionRequest contains only authority from an execution package.
type RuntimeExecutionRequest struct {
	ProjectID          domain.ProjectID
	TaskID             domain.TaskID
	Workspace          string
	Objective          string
	Scope              []string
	Constraints        []string
	AcceptanceCriteria []string
}

// RuntimeExecutionResult leaves canonical correlation under the caller's control.
// Outcome must be DONE, BLOCKED or FAILED, as in ExecutorResult.
type RuntimeExecutionResult struct {
	Outcome ExecutorOutcome
	Summary string
}
