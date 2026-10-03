package application

import (
	"context"
	"errors"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrTaskNotFound = errors.New("task not found")

type WorkflowEngine struct {
	tasks ports.TaskRepository
}

func NewWorkflowEngine(tasks ports.TaskRepository) *WorkflowEngine {
	return &WorkflowEngine{tasks: tasks}
}

func (w *WorkflowEngine) Transition(ctx context.Context, taskID domain.TaskID, target domain.TaskStatus) (domain.Task, error) {
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	task, found, err := w.tasks.FindByID(ctx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if !found {
		return domain.Task{}, ErrTaskNotFound
	}
	updated, err := task.TransitionTo(target)
	if err != nil {
		return domain.Task{}, err
	}
	if err := w.tasks.Save(ctx, updated); err != nil {
		return domain.Task{}, err
	}
	return updated, nil
}
