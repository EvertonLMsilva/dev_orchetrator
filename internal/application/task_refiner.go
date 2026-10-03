package application

import (
	"context"
	"errors"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var (
	ErrPlannerTaskIdentityMismatch = errors.New("planner decision does not match task identity")
	ErrTaskNotAnalyzing            = errors.New("task is not analyzing")
)

type TaskRefiner struct {
	tasks ports.TaskRepository
}

func NewTaskRefiner(tasks ports.TaskRepository) *TaskRefiner {
	return &TaskRefiner{tasks: tasks}
}

// Refine applies a closed planner intention. The workflow's own lookup is
// guarded so identity and analysis state are checked on the task it transitions.
func (r *TaskRefiner) Refine(ctx context.Context, decision ports.PlannerDecision) (domain.Task, error) {
	if err := decision.Validate(); err != nil {
		return domain.Task{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	tasks := refinementTaskRepository{TaskRepository: r.tasks, decision: decision}
	switch decision.Type {
	case ports.PlannerDecisionRequestEvidence:
		// More evidence is still analysis: no transition or persistence.
		task, _, err := tasks.FindByID(ctx, decision.TaskID)
		return task, err
	case ports.PlannerDecisionPrepareExecutor:
		return NewWorkflowEngine(tasks).Transition(ctx, decision.TaskID, domain.TaskStatusReadyForCodex)
	case ports.PlannerDecisionBlock:
		return NewWorkflowEngine(tasks).Transition(ctx, decision.TaskID, domain.TaskStatusBlocked)
	default:
		return domain.Task{}, ports.ErrInvalidPlannerDecision
	}
}

// Save remains the repository implementation and is called only by WorkflowEngine.
type refinementTaskRepository struct {
	ports.TaskRepository
	decision ports.PlannerDecision
}

func (r refinementTaskRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	task, found, err := r.TaskRepository.FindByID(ctx, id)
	if err != nil {
		return domain.Task{}, false, err
	}
	if !found {
		return domain.Task{}, false, ErrTaskNotFound
	}
	if task.ID != r.decision.TaskID || task.ProjectID != r.decision.ProjectID {
		return domain.Task{}, false, ErrPlannerTaskIdentityMismatch
	}
	if task.Status != domain.TaskStatusAnalyzing {
		return domain.Task{}, false, ErrTaskNotAnalyzing
	}
	return task, true, nil
}
