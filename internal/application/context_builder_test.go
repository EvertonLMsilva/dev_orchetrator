package application_test

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
)

func TestContextBuilder(t *testing.T) {
	project := domain.Project{ID: "p", Name: "Project", Workspace: "/workspace"}
	task := domain.Task{ID: "t", ProjectID: "p", Title: "Task", Status: domain.TaskStatusPlanned}
	repositoryErr := errors.New("repository failed")
	for _, tc := range []struct {
		name                                                string
		projectID                                           domain.ProjectID
		taskID                                              domain.TaskID
		missingProject, missingTask, otherProject, canceled bool
		projectErr, taskErr, wantErr                        error
	}{
		{name: "valid", projectID: "p", taskID: "t"},
		{name: "empty project", taskID: "t", wantErr: domain.ErrProjectIDRequired},
		{name: "blank project", projectID: " \t", taskID: "t", wantErr: domain.ErrProjectIDRequired},
		{name: "empty task", projectID: "p", wantErr: domain.ErrTaskIDRequired},
		{name: "blank task", projectID: "p", taskID: " \t", wantErr: domain.ErrTaskIDRequired},
		{name: "missing project", projectID: "p", taskID: "t", missingProject: true, wantErr: application.ErrContextProjectNotFound},
		{name: "missing task", projectID: "p", taskID: "t", missingTask: true, wantErr: application.ErrContextTaskNotFound},
		{name: "other project", projectID: "p", taskID: "t", otherProject: true, wantErr: application.ErrContextTaskProjectMismatch},
		{name: "project error", projectID: "p", taskID: "t", projectErr: repositoryErr, wantErr: repositoryErr},
		{name: "task error", projectID: "p", taskID: "t", taskErr: repositoryErr, wantErr: repositoryErr},
		{name: "canceled", projectID: "p", taskID: "t", canceled: true, wantErr: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projects := memory.NewProjectRepository()
			tasks := memory.NewTaskRepository()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !tc.missingProject {
				if err := projects.Save(ctx, project); err != nil {
					t.Fatal(err)
				}
			}
			storedTask := task
			if tc.otherProject {
				storedTask.ProjectID = "other"
			}
			if !tc.missingTask {
				if err := tasks.Save(ctx, storedTask); err != nil {
					t.Fatal(err)
				}
			}
			if tc.canceled {
				cancel()
			}
			builder := application.NewContextBuilder(projectErrorRepository{projects, tc.projectErr}, taskErrorRepository{tasks, tc.taskErr})
			got, err := builder.Build(ctx, tc.projectID, tc.taskID)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if got != (application.PlannerContext{}) {
					t.Fatalf("partial context: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Project != project || got.CurrentTask != task {
				t.Fatalf("context = %+v", got)
			}
			persisted, _, err := tasks.FindByID(ctx, task.ID)
			if err != nil || persisted != task {
				t.Fatalf("task changed: %+v, %v", persisted, err)
			}
		})
	}
}

type projectErrorRepository struct {
	*memory.ProjectRepository
	err error
}

func (r projectErrorRepository) FindByID(ctx context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	if r.err != nil {
		return domain.Project{}, false, r.err
	}
	return r.ProjectRepository.FindByID(ctx, id)
}

type taskErrorRepository struct {
	*memory.TaskRepository
	err error
}

func (r taskErrorRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	if r.err != nil {
		return domain.Task{}, false, r.err
	}
	return r.TaskRepository.FindByID(ctx, id)
}
