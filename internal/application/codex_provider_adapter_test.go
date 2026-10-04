package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
				if gotCtx == ctx || !reflect.DeepEqual(got, want) {
					t.Fatalf("runtime received %#v, want %#v with bounded context", got, want)
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

func TestAdapterSessionLimitsIntegration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome ports.ExecutorOutcome
		summary string
		failure error
		state   ExecutorSessionState
		want    error
	}{
		{"done", ports.ExecutorOutcomeDone, "é", nil, ExecutorSessionCompleted, nil},
		{"blocked", ports.ExecutorOutcomeBlocked, "é", nil, ExecutorSessionCompleted, nil},
		{"failed_result", ports.ExecutorOutcomeFailed, "é", nil, ExecutorSessionCompleted, nil},
		{"ordinary", "", "", errors.New("ordinary"), ExecutorSessionFailed, nil},
		{"exact", ports.ExecutorOutcomeDone, "éabc", nil, ExecutorSessionCompleted, nil},
		{"overflow", ports.ExecutorOutcomeDone, "éabcd", nil, ExecutorSessionFailed, ErrExecutionOutputLimit},
		{"invalid_result", "UNKNOWN", "ok", nil, ExecutorSessionFailed, ports.ErrInvalidExecutorResult},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := packageRequest()
			before := packageRequest()
			repo := &packageProjects{project: domain.Project{ID: req.ProjectID, Workspace: t.TempDir()}, found: true}
			limits, _ := NewExecutionLimits(time.Second, 5)
			var sessions []ExecutorSession
			var bounded context.Context
			runtime := adapterRuntime(func(ctx context.Context, _ ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				bounded = ctx
				if len(sessions) != 1 || sessions[0].State() != ExecutorSessionActive {
					t.Fatal("missing active session")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded context")
				}
				return ports.RuntimeExecutionResult{Outcome: tc.outcome, Summary: tc.summary}, tc.failure
			})
			adapter := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime, WithExecutorExecutionLimits(limits), WithExecutorSessionLifecycle(NewExecutorSessionManager(nil), func(s ExecutorSession) { sessions = append(sessions, s) }))
			got, err := adapter.Execute(context.Background(), req)
			want := tc.want
			if tc.failure != nil {
				want = tc.failure
			}
			if !errors.Is(err, want) || len(sessions) != 2 || sessions[1].State() != tc.state || sessions[0].SessionID() != sessions[1].SessionID() {
				t.Fatalf("%#v %v sessions=%+v", got, err, sessions)
			}
			if want != nil && got != (ports.ExecutorResult{}) {
				t.Fatal("fabricated result")
			}
			if bounded.Err() != context.Canceled {
				t.Fatal("cancel not released")
			}
			if !reflect.DeepEqual(req, before) {
				t.Fatal("task spec mutated")
			}
		})
	}
}

func TestAdapterCancellationIntegration(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "parent", true: "timeout"}[timeout], func(t *testing.T) {
			req := packageRequest()
			repo := &packageProjects{project: domain.Project{ID: req.ProjectID, Workspace: t.TempDir()}, found: true}
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			duration := time.Second
			want := context.Canceled
			if timeout {
				duration = 20 * time.Millisecond
				want = context.DeadlineExceeded
			}
			limits, _ := NewExecutionLimits(duration, 10)
			var states []ExecutorSessionState
			runtime := adapterRuntime(func(ctx context.Context, _ ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				if !timeout {
					cancel()
				}
				select {
				case <-ctx.Done():
				case <-time.After(2 * time.Second):
					t.Fatal("runtime did not observe cancellation")
				}
				return ports.RuntimeExecutionResult{}, ctx.Err()
			})
			a := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime, WithExecutorExecutionLimits(limits), WithExecutorSessionLifecycle(NewExecutorSessionManager(nil), func(s ExecutorSession) { states = append(states, s.State()) }))
			_, err := a.Execute(parent, req)
			if !errors.Is(err, want) || !reflect.DeepEqual(states, []ExecutorSessionState{ExecutorSessionActive, ExecutorSessionCancelled}) {
				t.Fatalf("%v %v", err, states)
			}
		})
	}
}

func TestAdapterFailClosedIntegration(t *testing.T) {
	failure := errors.New("session creation failure")
	for _, stage := range []string{"limits", "session", "package", "cancelled_package"} {
		t.Run(stage, func(t *testing.T) {
			req := packageRequest()
			repo := &packageProjects{project: domain.Project{ID: req.ProjectID, Workspace: t.TempDir()}, found: true}
			limits, _ := NewExecutionLimits(time.Second, 10)
			manager := NewExecutorSessionManager(nil)
			want := ErrExecutionProjectNotFound
			state := ExecutorSessionFailed
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "limits":
				limits = ExecutionLimits{}
				want = ErrInvalidExecutionLimits
			case "session":
				manager = NewExecutorSessionManager(func() (domain.SessionID, error) { return "", failure })
				want = failure
			case "package":
				repo.found = false
			case "cancelled_package":
				repo.found = false
				cancel()
				state = ExecutorSessionCancelled
			}
			var sessions []ExecutorSession
			runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				t.Fatal("runtime called")
				return ports.RuntimeExecutionResult{}, nil
			})
			a := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime, WithExecutorExecutionLimits(limits), WithExecutorSessionLifecycle(manager, func(s ExecutorSession) { sessions = append(sessions, s) }))
			_, err := a.Execute(ctx, req)
			if !errors.Is(err, want) {
				t.Fatalf("%v want %v", err, want)
			}
			if stage == "session" {
				if len(sessions) != 0 {
					t.Fatal("session created after failure")
				}
			} else if len(sessions) != 2 || sessions[1].State() != state {
				t.Fatalf("sessions %+v", sessions)
			}
		})
	}
}

func TestAdapterDisposableSessions(t *testing.T) {
	req := packageRequest()
	repo := &packageProjects{project: domain.Project{ID: req.ProjectID, Workspace: t.TempDir()}, found: true}
	var sessions []ExecutorSession
	runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
		return ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeDone, Summary: "done"}, nil
	})
	a := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime, WithExecutorSessionLifecycle(NewExecutorSessionManager(nil), func(s ExecutorSession) { sessions = append(sessions, s) }))
	for i := 0; i < 2; i++ {
		if _, err := a.Execute(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if len(sessions) != 4 || sessions[0].SessionID() == sessions[2].SessionID() {
		t.Fatalf("reused session %+v", sessions)
	}
}

func TestAdapterReturnCancellationClassification(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancel    bool
		failure   error
		wantState ExecutorSessionState
		want      error
	}{
		{"wrapped_cancel", false, fmt.Errorf("wrapped: %w", context.Canceled), ExecutorSessionCancelled, context.Canceled},
		{"wrapped_deadline", false, fmt.Errorf("wrapped: %w", context.DeadlineExceeded), ExecutorSessionCancelled, context.DeadlineExceeded},
		{"cancelled_transport", true, errors.New("transport closed"), ExecutorSessionCancelled, nil},
		{"cancelled_valid_result", true, nil, ExecutorSessionCancelled, context.Canceled},
		{"ordinary_timeout_text", false, errors.New("timeout text"), ExecutorSessionFailed, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := packageRequest()
			repo := &packageProjects{project: domain.Project{ID: req.ProjectID, Workspace: t.TempDir()}, found: true}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var terminal ExecutorSession
			runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				if tc.cancel {
					cancel()
				}
				return ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeDone, Summary: "done"}, tc.failure
			})
			a := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime, WithExecutorSessionLifecycle(NewExecutorSessionManager(nil), func(s ExecutorSession) { terminal = s }))
			got, err := a.Execute(ctx, req)
			want := tc.want
			if want == nil {
				want = tc.failure
			}
			if !errors.Is(err, want) || terminal.State() != tc.wantState || got != (ports.ExecutorResult{}) {
				t.Fatalf("%#v %v %s", got, err, terminal.State())
			}
		})
	}
}

func TestAdapterInvalidLimitsMatrix(t *testing.T) {
	for _, limits := range []ExecutionLimits{{}, {timeout: -time.Second, maxOutputBytes: 1}, {timeout: time.Second, maxOutputBytes: -1}, {timeout: time.Second}, {maxOutputBytes: 1}} {
		runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
			t.Fatal("invalid limits reached runtime")
			return ports.RuntimeExecutionResult{}, nil
		})
		_, err := NewCodexProviderAdapter(nil, runtime, WithExecutorExecutionLimits(limits)).Execute(context.Background(), packageRequest())
		if !errors.Is(err, ErrInvalidExecutionLimits) {
			t.Fatal(err)
		}
	}
}

func TestAdapterTaskStateAndProviderIsolation(t *testing.T) {
	task := domain.Task{ID: "task", ProjectID: "project", Status: domain.TaskStatusInProgress}
	before := task
	req := packageRequest()
	repo := &packageProjects{project: domain.Project{ID: task.ProjectID, Workspace: t.TempDir()}, found: true}
	runtime := adapterRuntime(func(context.Context, ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
		return ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeFailed, Summary: "valid failure"}, nil
	})
	var terminal ExecutorSession
	result, err := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime, WithExecutorSessionLifecycle(NewExecutorSessionManager(nil), func(s ExecutorSession) { terminal = s })).Execute(context.Background(), req)
	if err != nil || task != before || terminal.State() != ExecutorSessionCompleted || result.Outcome != ports.ExecutorOutcomeFailed {
		t.Fatal("task state coupled to session", err)
	}
	for _, value := range []any{result, terminal} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			name := strings.ToLower(typ.Field(i).Name)
			if strings.Contains(name, "provider") || strings.Contains(name, "thread") || strings.Contains(name, "turn") {
				t.Fatal("provider metadata exposed")
			}
		}
	}
}
