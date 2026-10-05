package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var (
	ErrContextProjectNotFound     = errors.New("context project not found")
	ErrContextTaskNotFound        = errors.New("context task not found")
	ErrContextTaskProjectMismatch = errors.New("context task belongs to another project")
)

// PlannerContext contains only the current canonical project and task.
type PlannerContext = ports.PlannerContext

// ContextBuilder reads canonical state without changing it or invoking a planner.
type ContextBuilder struct {
	projects ports.ProjectRepository
	tasks    ports.TaskRepository
}

func NewContextBuilder(projects ports.ProjectRepository, tasks ports.TaskRepository) *ContextBuilder {
	return &ContextBuilder{projects: projects, tasks: tasks}
}

func (b *ContextBuilder) Build(ctx context.Context, projectID domain.ProjectID, taskID domain.TaskID) (PlannerContext, error) {
	if err := ctx.Err(); err != nil {
		return PlannerContext{}, err
	}
	if strings.TrimSpace(string(projectID)) == "" {
		return PlannerContext{}, domain.ErrProjectIDRequired
	}
	if strings.TrimSpace(string(taskID)) == "" {
		return PlannerContext{}, domain.ErrTaskIDRequired
	}
	project, found, err := b.projects.FindByID(ctx, projectID)
	if err != nil {
		return PlannerContext{}, fmt.Errorf("read context project: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PlannerContext{}, err
	}
	if !found {
		return PlannerContext{}, ErrContextProjectNotFound
	}
	task, found, err := b.tasks.FindByID(ctx, taskID)
	if err != nil {
		return PlannerContext{}, fmt.Errorf("read context task: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return PlannerContext{}, err
	}
	if !found {
		return PlannerContext{}, ErrContextTaskNotFound
	}
	if task.ProjectID != project.ID {
		return PlannerContext{}, ErrContextTaskProjectMismatch
	}
	return PlannerContext{Project: project, CurrentTask: task}, nil
}
