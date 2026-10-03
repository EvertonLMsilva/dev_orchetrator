package application

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/domain"
)

type queryProjects struct {
	value        domain.Project
	found        bool
	err          error
	ctx          context.Context
	id           domain.ProjectID
	calls, saves int
}

func (r *queryProjects) Save(context.Context, domain.Project) error { r.saves++; return nil }
func (r *queryProjects) FindByID(ctx context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	r.calls++
	r.ctx, r.id = ctx, id
	return r.value, r.found, r.err
}

type queryTasks struct {
	value        domain.Task
	found        bool
	err          error
	ctx          context.Context
	id           domain.TaskID
	calls, saves int
}

func (r *queryTasks) Save(context.Context, domain.Task) error { r.saves++; return nil }
func (r *queryTasks) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	r.calls++
	r.ctx, r.id = ctx, id
	return r.value, r.found, r.err
}
func (r *queryTasks) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	panic("unexpected FindByProject")
}

func TestQueryProject(t *testing.T) {
	repositoryErr := errors.New("repository failure")
	value := domain.Project{ID: "p", Name: "Project", Workspace: "workspace"}
	for _, tc := range []struct {
		name             string
		found            bool
		repoErr, wantErr error
		canceled         bool
	}{
		{name: "found", found: true}, {name: "missing", wantErr: ErrProjectNotFound},
		{name: "repository error", found: true, repoErr: repositoryErr, wantErr: repositoryErr},
		{name: "canceled", canceled: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			projects := &queryProjects{value: value, found: tc.found, err: tc.repoErr}
			tasks := &queryTasks{}
			got, err := NewQueryService(projects, tasks).Project(ctx, "p")
			if err != tc.wantErr {
				t.Fatalf("error = %v, want same %v", err, tc.wantErr)
			}
			want := domain.Project{}
			if tc.wantErr == nil {
				want = value
			}
			if got != want {
				t.Fatalf("project = %+v, want %+v", got, want)
			}
			if tc.canceled {
				if projects.calls != 0 {
					t.Fatal("canceled query reached repository")
				}
			} else if projects.calls != 1 || projects.ctx != ctx || projects.id != "p" {
				t.Fatal("ID/context not forwarded")
			}
			if projects.saves != 0 || tasks.saves != 0 || tasks.calls != 0 {
				t.Fatal("query wrote or accessed unrelated repository")
			}
		})
	}
}

func TestQueryTask(t *testing.T) {
	repositoryErr := errors.New("repository failure")
	value := domain.Task{ID: "t", ProjectID: "p", Title: "Task", Status: domain.TaskStatusInProgress}
	for _, tc := range []struct {
		name             string
		found            bool
		repoErr, wantErr error
		canceled         bool
	}{
		{name: "found", found: true}, {name: "missing", wantErr: ErrTaskNotFound},
		{name: "repository error", found: true, repoErr: repositoryErr, wantErr: repositoryErr},
		{name: "canceled", canceled: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			projects := &queryProjects{}
			tasks := &queryTasks{value: value, found: tc.found, err: tc.repoErr}
			got, err := NewQueryService(projects, tasks).Task(ctx, "t")
			if err != tc.wantErr {
				t.Fatalf("error = %v, want same %v", err, tc.wantErr)
			}
			want := domain.Task{}
			if tc.wantErr == nil {
				want = value
			}
			if got != want {
				t.Fatalf("task = %+v, want %+v", got, want)
			}
			if tc.canceled {
				if tasks.calls != 0 {
					t.Fatal("canceled query reached repository")
				}
			} else if tasks.calls != 1 || tasks.ctx != ctx || tasks.id != "t" {
				t.Fatal("ID/context not forwarded")
			}
			if projects.saves != 0 || tasks.saves != 0 || projects.calls != 0 {
				t.Fatal("query wrote or accessed unrelated repository")
			}
		})
	}
}
