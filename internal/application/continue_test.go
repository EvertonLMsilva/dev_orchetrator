package application

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/domain"
)

type continueRepository struct {
	task             domain.Task
	found            bool
	findErr, saveErr error
	reads, saves     int
	secondTask       *domain.Task
}

func (r *continueRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	r.reads++
	if id != "task-123" {
		panic("wrong task ID")
	}
	if r.reads == 2 && r.secondTask != nil {
		return *r.secondTask, true, nil
	}
	return r.task, r.found, r.findErr
}
func (r *continueRepository) Save(ctx context.Context, task domain.Task) error {
	r.saves++
	r.task = task
	return r.saveErr
}
func (r *continueRepository) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	panic("unexpected query")
}

func TestContinueTaskStatuses(t *testing.T) {
	for _, tc := range []struct{ from, to domain.TaskStatus }{
		{domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis},
		{domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing},
		{domain.TaskStatusAnalyzing, domain.TaskStatusReadyForCodex},
		{domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress},
		{domain.TaskStatusInProgress, ""}, {domain.TaskStatusBlocked, ""},
		{domain.TaskStatusDone, ""}, {domain.TaskStatusFailed, ""}, {domain.TaskStatusCancelled, ""}, {"UNKNOWN", ""},
	} {
		t.Run(string(tc.from), func(t *testing.T) {
			r := &continueRepository{task: domain.Task{ID: "task-123", ProjectID: "p", Title: "title", Status: tc.from}, found: true}
			s := NewContinueService(r, NewWorkflowEngine(r))
			got, err := s.ContinueTask(context.Background(), "task-123")
			if tc.to == "" {
				if err != ErrTaskCannotContinue || got != (domain.Task{}) || r.reads != 1 || r.saves != 0 {
					t.Fatalf("got %+v, %v, reads=%d saves=%d", got, err, r.reads, r.saves)
				}
			} else {
				want := domain.Task{ID: "task-123", ProjectID: "p", Title: "title", Status: tc.to}
				if err != nil || got != want || r.task != want || r.reads != 2 || r.saves != 1 {
					t.Fatalf("got %+v, %v, reads=%d saves=%d", got, err, r.reads, r.saves)
				}
			}
		})
	}
}

func TestContinueTaskErrors(t *testing.T) {
	repositoryErr := errors.New("repository failure")
	saveErr := errors.New("save failure")
	changed := domain.Task{ID: "task-123", Status: domain.TaskStatusDone}
	for _, tc := range []struct {
		name             string
		found            bool
		findErr, saveErr error
		canceled         bool
		second           *domain.Task
		want             error
		reads, saves     int
	}{
		{name: "missing", want: ErrTaskNotFound, reads: 1},
		{name: "repository", findErr: repositoryErr, want: repositoryErr, reads: 1},
		{name: "canceled", canceled: true, want: context.Canceled},
		{name: "workflow save", found: true, saveErr: saveErr, want: saveErr, reads: 2, saves: 1},
		{name: "domain validates reread", found: true, second: &changed, want: domain.ErrInvalidTaskTransition, reads: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &continueRepository{task: domain.Task{ID: "task-123", Status: domain.TaskStatusPlanned}, found: tc.found, findErr: tc.findErr, saveErr: tc.saveErr, secondTask: tc.second}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			got, err := NewContinueService(r, NewWorkflowEngine(r)).ContinueTask(ctx, "task-123")
			if err != tc.want || got != (domain.Task{}) || r.reads != tc.reads || r.saves != tc.saves {
				t.Fatalf("got %+v, %v, reads=%d saves=%d", got, err, r.reads, r.saves)
			}
		})
	}
}
