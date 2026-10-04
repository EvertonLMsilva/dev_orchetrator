package application

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type adapterRuntime func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error)

func (f adapterRuntime) Execute(ctx context.Context, r ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
	return f(ctx, r)
}

var _ ports.Executor = (*CodexProviderAdapter)(nil)
var _ ports.ExecutorRuntime = adapterRuntime(nil)

func TestCodexProviderAdapterOutcomes(t *testing.T) {
	for _, outcome := range []ports.ExecutorOutcome{ports.ExecutorOutcomeDone, ports.ExecutorOutcomeBlocked, ports.ExecutorOutcomeFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			request := packageRequest()
			workspace := t.TempDir()
			root, err := filepath.EvalSymlinks(workspace)
			if err != nil {
				t.Fatal(err)
			}
			repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: workspace}, found: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			runtime := adapterRuntime(func(gotCtx context.Context, got ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				calls++
				want := ports.RuntimeExecutionRequest{ProjectID: request.ProjectID, TaskID: request.TaskID, Workspace: root, Objective: request.Spec.Objective, Scope: request.Spec.Scope, Constraints: request.Spec.Constraints, AcceptanceCriteria: request.Spec.AcceptanceCriteria}
				if gotCtx != ctx || !reflect.DeepEqual(got, want) {
					t.Fatalf("runtime received %#v, want %#v with original context", got, want)
				}
				return ports.RuntimeExecutionResult{Outcome: outcome, Summary: "runtime summary"}, nil
			})
			got, err := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime).Execute(ctx, request)
			want := ports.ExecutorResult{ProjectID: request.ProjectID, TaskID: request.TaskID, Outcome: outcome, Summary: "runtime summary"}
			if err != nil || got != want || calls != 1 || repo.requested != request.ProjectID {
				t.Fatalf("got %#v, %v, calls=%d; want %#v", got, err, calls, want)
			}
		})
	}
}

func TestCodexProviderAdapterBuildFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ports.ExecutorRequest, *packageProjects)
		want   error
	}{
		{"request", func(r *ports.ExecutorRequest, _ *packageProjects) { r.TaskID = "" }, ports.ErrInvalidExecutorRequest},
		{"workspace", func(_ *ports.ExecutorRequest, p *packageProjects) { p.project.Workspace = "relative" }, ErrUnsafeExecutionPackage},
		{"scope", func(r *ports.ExecutorRequest, _ *packageProjects) { r.Spec.Scope[0] = "../escape" }, ports.ErrInvalidExecutorRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := packageRequest()
			repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: t.TempDir()}, found: true}
			tc.change(&request, repo)
			runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				t.Fatal("runtime called after build failure")
				return ports.RuntimeExecutionResult{}, nil
			})
			got, err := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime).Execute(context.Background(), request)
			if !errors.Is(err, tc.want) || got != (ports.ExecutorResult{}) {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
}

func TestCodexProviderAdapterRuntimeError(t *testing.T) {
	request := packageRequest()
	repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: t.TempDir()}, found: true}
	failure := errors.New("runtime transport failure")
	runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
		return ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeFailed, Summary: "must be discarded"}, failure
	})
	got, err := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime).Execute(context.Background(), request)
	if !errors.Is(err, failure) || got != (ports.ExecutorResult{}) {
		t.Fatalf("got %#v, %v", got, err)
	}
}

func TestCodexProviderAdapterDefensiveCopies(t *testing.T) {
	request := packageRequest()
	repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: t.TempDir()}, found: true}
	builder := NewExecutionPackageBuilder(repo)
	authorized, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := packageRequest()
	runtime := adapterRuntime(func(_ context.Context, r ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
		r.Scope[0], r.Constraints[0], r.AcceptanceCriteria[0] = "changed", "changed", "changed"
		return ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeDone, Summary: "done"}, nil
	})
	if _, err := NewCodexProviderAdapter(builder, runtime).Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(request, want) {
		t.Fatal("runtime mutation changed request")
	}
	if !reflect.DeepEqual(authorized.Scope, want.Spec.Scope) || !reflect.DeepEqual(authorized.Constraints, want.Spec.Constraints) || !reflect.DeepEqual(authorized.AcceptanceCriteria, want.Spec.AcceptanceCriteria) {
		t.Fatal("runtime mutation changed previously authorized package")
	}
}
