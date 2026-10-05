package application

import (
	"context"
	"crypto/rand"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"errors"
	"strings"
)

var ErrConversationTask = errors.New("ambiguous or invalid conversation task")

type ConversationTaskResolver struct{ tasks ports.TaskRepository }

func NewConversationTaskResolver(tasks ports.TaskRepository) *ConversationTaskResolver {
	return &ConversationTaskResolver{tasks: tasks}
}

// Resolve has no CAS/claim: concurrent messages can create competing tasks.
// Ambiguity observed in repository state is rejected; failures are never retried.
func (r *ConversationTaskResolver) Resolve(ctx context.Context, projectID domain.ProjectID) (domain.Task, error) {
	if err := ctx.Err(); err != nil {
		return domain.Task{}, err
	}
	if r == nil || r.tasks == nil || strings.TrimSpace(string(projectID)) == "" {
		return domain.Task{}, ErrConversationTask
	}
	tasks, err := r.tasks.FindByProject(ctx, projectID)
	if err != nil {
		return domain.Task{}, err
	}
	var active *domain.Task
	for _, task := range tasks {
		if task.ProjectID != projectID || strings.TrimSpace(string(task.ID)) == "" {
			return domain.Task{}, ErrConversationTask
		}
		switch task.Status {
		case domain.TaskStatusDone, domain.TaskStatusFailed, domain.TaskStatusCancelled:
			continue
		case domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing, domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress, domain.TaskStatusBlocked:
		default:
			return domain.Task{}, ErrConversationTask
		}
		if active != nil {
			return domain.Task{}, ErrConversationTask
		}
		copy := task
		active = &copy
	}
	if active != nil {
		return *active, nil
	}
	// There is no existing TaskID generator. Random IDs avoid an ordering contract.
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return domain.Task{}, err
	}
	id := domain.TaskID(hex.EncodeToString(entropy[:]))
	_, exists, err := r.tasks.FindByID(ctx, id)
	if err != nil {
		return domain.Task{}, err
	}
	if exists {
		return domain.Task{}, ErrConversationTask
	}
	task, err := domain.NewTask(id, projectID, "Conversation request")
	if err != nil {
		return domain.Task{}, err
	}
	if err := r.tasks.Save(ctx, task); err != nil {
		return domain.Task{}, err
	}
	return task, nil
}

// Prepare requests only the explicit analysis transitions approved for messages.
func (r *ConversationTaskResolver) Prepare(ctx context.Context, task domain.Task) (domain.Task, error) {
	if task.Status == domain.TaskStatusPlanned || task.Status == domain.TaskStatusBlocked {
		updated, err := r.transition(ctx, task, domain.TaskStatusReadyForAnalysis)
		if err != nil {
			return domain.Task{}, err
		}
		task = updated
	}
	if task.Status == domain.TaskStatusReadyForAnalysis {
		return r.transition(ctx, task, domain.TaskStatusAnalyzing)
	}
	if task.Status != domain.TaskStatusAnalyzing {
		return domain.Task{}, ErrConversationTask
	}
	return task, nil
}
func (r *ConversationTaskResolver) transition(ctx context.Context, task domain.Task, target domain.TaskStatus) (domain.Task, error) {
	guarded := executorCycleRepository{TaskRepository: r.tasks, identity: ExecutorTaskIdentity{ProjectID: task.ProjectID, TaskID: task.ID}, expected: task.Status}
	return NewWorkflowEngine(guarded).Transition(ctx, task.ID, target)
}
