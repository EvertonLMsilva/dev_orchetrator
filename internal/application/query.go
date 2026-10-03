package application

import (
	"context"
	"errors"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrProjectNotFound = errors.New("project not found")

// QueryService provides read-only project and task queries.
type QueryService struct {
	projects ports.ProjectRepository
	tasks    ports.TaskRepository
}

func NewQueryService(projects ports.ProjectRepository, tasks ports.TaskRepository) *QueryService {
	return &QueryService{projects: projects, tasks: tasks}
}

func (q *QueryService) Project(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	if err := ctx.Err(); err != nil {
		return domain.Project{}, err
	}
	project, found, err := q.projects.FindByID(ctx, id)
	if err != nil {
		return domain.Project{}, err
	}
	if !found {
		return domain.Project{}, ErrProjectNotFound
	}
	return project, nil
}

func (q *QueryService) Task(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	task, found, err := q.tasks.FindByID(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if !found {
		return domain.Task{}, ErrTaskNotFound
	}
	return task, nil
}
