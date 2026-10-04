package application

import (
	"context"

	"dev-orchestrator/internal/ports"
)

// CodexProviderAdapter connects authorized execution packages to an external runtime.
type CodexProviderAdapter struct {
	builder *ExecutionPackageBuilder
	runtime ports.ExecutorRuntime
}

var _ ports.Executor = (*CodexProviderAdapter)(nil)

func NewCodexProviderAdapter(builder *ExecutionPackageBuilder, runtime ports.ExecutorRuntime) *CodexProviderAdapter {
	return &CodexProviderAdapter{builder: builder, runtime: runtime}
}

func (a *CodexProviderAdapter) Execute(ctx context.Context, request ports.ExecutorRequest) (ports.ExecutorResult, error) {
	pkg, err := a.builder.Build(ctx, request)
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	result, err := a.runtime.Execute(ctx, ports.RuntimeExecutionRequest{
		ProjectID: pkg.ProjectID, TaskID: pkg.TaskID, Workspace: pkg.Workspace,
		Objective:          pkg.Objective,
		Scope:              append([]string(nil), pkg.Scope...),
		Constraints:        append([]string(nil), pkg.Constraints...),
		AcceptanceCriteria: append([]string(nil), pkg.AcceptanceCriteria...),
	})
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	return ports.ExecutorResult{
		ProjectID: pkg.ProjectID, TaskID: pkg.TaskID,
		Outcome: result.Outcome, Summary: result.Summary,
	}, nil
}
