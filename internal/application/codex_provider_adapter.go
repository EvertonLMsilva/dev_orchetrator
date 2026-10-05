package application

import (
	"context"
	"errors"
	"time"

	"dev-orchestrator/internal/ports"
)

// CodexProviderAdapter connects authorized execution packages to an external runtime.
// Configuration is application-owned and never derived from task or provider text.
type CodexProviderAdapter struct {
	builder  *ExecutionPackageBuilder
	runtime  ports.ExecutorRuntime
	limits   ExecutionLimits
	sessions *ExecutorSessionManager
	observe  func(ExecutorSession)
}

var _ ports.Executor = (*CodexProviderAdapter)(nil)

type CodexProviderAdapterOption func(*CodexProviderAdapter)

func WithExecutorExecutionLimits(limits ExecutionLimits) CodexProviderAdapterOption {
	return func(a *CodexProviderAdapter) { a.limits = limits }
}

// WithExecutorSessionLifecycle observes immutable application session snapshots.
// Callbacks run synchronously and must return promptly; concurrent Execute calls
// require a concurrency-safe observer and ID generator. No history is retained.
func WithExecutorSessionLifecycle(manager *ExecutorSessionManager, observer func(ExecutorSession)) CodexProviderAdapterOption {
	return func(a *CodexProviderAdapter) { a.sessions, a.observe = manager, observer }
}

// The default application policy bounds legacy callers to one minute and 1 MiB.
// This Summary policy is independent of the infrastructure's hard transport/text
// limits, which remain enforced even when application policy permits more output.
func NewCodexProviderAdapter(builder *ExecutionPackageBuilder, runtime ports.ExecutorRuntime, options ...CodexProviderAdapterOption) *CodexProviderAdapter {
	limits, _ := NewExecutionLimits(time.Minute, 1<<20)
	a := &CodexProviderAdapter{builder: builder, runtime: runtime, limits: limits, sessions: NewExecutorSessionManager(nil)}
	for _, option := range options {
		option(a)
	}
	return a
}

func (a *CodexProviderAdapter) Execute(ctx context.Context, request ports.ExecutorRequest) (result ports.ExecutorResult, executionErr error) {
	if err := request.Validate(); err != nil {
		return ports.ExecutorResult{}, err
	}
	session, err := a.sessions.Create(request.ProjectID, request.TaskID)
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	if a.observe != nil {
		a.observe(session)
	}
	// Registered before policy/build failures; classify before releasing the
	// bounded context so our own cleanup cancellation cannot change the outcome.
	finishCtx := ctx
	var release context.CancelFunc
	defer func() {
		if release != nil {
			defer release()
		}
		terminal, err := session.Finish(finishCtx, executionErr)
		executionErr = errors.Join(executionErr, err)
		if a.observe != nil {
			a.observe(terminal)
		}
	}()
	bounded, cancel, err := BoundedExecutionContext(ctx, a.limits)
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	finishCtx = bounded
	release = cancel
	pkg, err := a.builder.Build(bounded, request)
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	if err := bounded.Err(); err != nil {
		return ports.ExecutorResult{}, err
	}
	if a.runtime == nil {
		return ports.ExecutorResult{}, errors.New("executor runtime required")
	}
	runtimeResult, err := a.runtime.Execute(bounded, ports.RuntimeExecutionRequest{
		ProjectID: pkg.ProjectID, TaskID: pkg.TaskID, Workspace: pkg.Workspace,
		Objective:          pkg.Objective,
		Scope:              append([]string(nil), pkg.Scope...),
		Constraints:        append([]string(nil), pkg.Constraints...),
		AcceptanceCriteria: append([]string(nil), pkg.AcceptanceCriteria...),
	})
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	if err := bounded.Err(); err != nil {
		return ports.ExecutorResult{}, err
	}
	output, err := NewExecutionOutput(a.limits)
	if err != nil {
		return ports.ExecutorResult{}, err
	}
	if err := output.Append(runtimeResult.Summary); err != nil {
		return ports.ExecutorResult{}, err
	}
	result = ports.ExecutorResult{ProjectID: pkg.ProjectID, TaskID: pkg.TaskID, Outcome: runtimeResult.Outcome, Summary: output.String()}
	if err := result.Validate(); err != nil {
		return ports.ExecutorResult{}, err
	}
	return result, nil
}
