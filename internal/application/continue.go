package application

import (
	"context"
	"errors"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrTaskCannotContinue = errors.New("task cannot continue")

type ContinueService struct {
	tasks    ports.TaskRepository
	workflow *WorkflowEngine
}

func NewContinueService(tasks ports.TaskRepository, workflow *WorkflowEngine) *ContinueService {
	return &ContinueService{tasks: tasks, workflow: workflow}
}

func (s *ContinueService) ContinueTask(ctx context.Context, taskID domain.TaskID) (domain.Task, error) {
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	task, found, err := s.tasks.FindByID(ctx, taskID)
	if err != nil {
		return domain.Task{}, err
	}
	if !found {
		return domain.Task{}, ErrTaskNotFound
	}
	var target domain.TaskStatus
	switch task.Status {
	case domain.TaskStatusPlanned:
		target = domain.TaskStatusReadyForAnalysis
	case domain.TaskStatusReadyForAnalysis:
		target = domain.TaskStatusAnalyzing
	case domain.TaskStatusAnalyzing:
		target = domain.TaskStatusReadyForCodex
	case domain.TaskStatusReadyForCodex:
		target = domain.TaskStatusInProgress
	default:
		return domain.Task{}, ErrTaskCannotContinue
	}
	return s.workflow.Transition(ctx, taskID, target)
}
