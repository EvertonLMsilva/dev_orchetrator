package application

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type fakeTaskRepository struct {
	task                 domain.Task
	found                bool
	findErr, saveErr     error
	findCalls, saveCalls int
	findCtx, saveCtx     context.Context
	findID               domain.TaskID
	saved                domain.Task
}

var _ ports.TaskRepository = (*fakeTaskRepository)(nil)

func (f *fakeTaskRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	f.findCalls++
	f.findCtx, f.findID = ctx, id
	return f.task, f.found, f.findErr
}

func (f *fakeTaskRepository) Save(ctx context.Context, task domain.Task) error {
	f.saveCalls++
	f.saveCtx, f.saved = ctx, task
	return f.saveErr
}

func (f *fakeTaskRepository) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	panic("unexpected FindByProject call")
}

func plannedTask(t *testing.T) domain.Task {
	t.Helper()
	task, err := domain.NewTask("task-1", "project-1", "Minimal workflow")
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestWorkflowTransition(t *testing.T) {
	original := plannedTask(t)
	repo := &fakeTaskRepository{task: original, found: true}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	updated, err := NewWorkflowEngine(repo).Transition(ctx, original.ID, domain.TaskStatusReadyForAnalysis)
	if err != nil {
		t.Fatal(err)
	}
	want := original
	want.Status = domain.TaskStatusReadyForAnalysis
	if updated != want {
		t.Fatalf("updated = %+v, want %+v", updated, want)
	}
	if repo.saved != updated {
		t.Fatalf("saved = %+v, want %+v", repo.saved, updated)
	}
	if repo.findCalls != 1 || repo.saveCalls != 1 {
		t.Fatalf("calls: find=%d save=%d", repo.findCalls, repo.saveCalls)
	}
	if repo.findID != original.ID {
		t.Fatalf("FindByID ID = %q", repo.findID)
	}
	if repo.findCtx != ctx || repo.saveCtx != ctx {
		t.Fatal("repository did not receive the same context")
	}
}

func TestWorkflowTransitionErrors(t *testing.T) {
	findErr := errors.New("find failure")
	saveErr := errors.New("save failure")
	for _, tc := range []struct {
		name             string
		found            bool
		findErr, saveErr error
		target           domain.TaskStatus
		wantErr          error
		wantSaveCalls    int
	}{
		{name: "missing", target: domain.TaskStatusReadyForAnalysis, wantErr: ErrTaskNotFound},
		{name: "find failure", findErr: findErr, target: domain.TaskStatusReadyForAnalysis, wantErr: findErr},
		{name: "invalid transition", found: true, target: domain.TaskStatusDone, wantErr: domain.ErrInvalidTaskTransition},
		{name: "save failure", found: true, saveErr: saveErr, target: domain.TaskStatusReadyForAnalysis, wantErr: saveErr, wantSaveCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := plannedTask(t)
			repo := &fakeTaskRepository{task: task, found: tc.found, findErr: tc.findErr, saveErr: tc.saveErr}
			got, err := NewWorkflowEngine(repo).Transition(context.Background(), task.ID, tc.target)
			if !errors.Is(err, tc.wantErr) || err != tc.wantErr {
				t.Fatalf("error = %v, want identical %v", err, tc.wantErr)
			}
			if got != (domain.Task{}) {
				t.Fatalf("task on error = %+v", got)
			}
			if repo.findCalls != 1 || repo.saveCalls != tc.wantSaveCalls {
				t.Fatalf("calls: find=%d save=%d", repo.findCalls, repo.saveCalls)
			}
		})
	}
}

func TestWorkflowTransitionCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &fakeTaskRepository{}
	got, err := NewWorkflowEngine(repo).Transition(ctx, "task-1", domain.TaskStatusReadyForAnalysis)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if got != (domain.Task{}) {
		t.Fatalf("task on error = %+v", got)
	}
	if repo.findCalls != 0 || repo.saveCalls != 0 {
		t.Fatalf("calls: find=%d save=%d", repo.findCalls, repo.saveCalls)
	}
}

func TestWorkflowTransitionMemoryIntegration(t *testing.T) {
	ctx := context.Background()
	task := plannedTask(t)
	repo := memory.NewTaskRepository()
	if err := repo.Save(ctx, task); err != nil {
		t.Fatal(err)
	}
	updated, err := NewWorkflowEngine(repo).Transition(ctx, task.ID, domain.TaskStatusReadyForAnalysis)
	if err != nil {
		t.Fatal(err)
	}
	persisted, found, err := repo.FindByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !found || persisted != updated || persisted.Status != domain.TaskStatusReadyForAnalysis {
		t.Fatalf("persisted=%+v found=%v updated=%+v", persisted, found, updated)
	}
}
