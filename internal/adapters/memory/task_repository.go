package memory

import (
	"context"
	"sync"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type TaskRepository struct {
	mu    sync.RWMutex
	tasks map[domain.TaskID]domain.Task
}

var _ ports.TaskRepository = (*TaskRepository)(nil)

func NewTaskRepository() *TaskRepository {
	return &TaskRepository{tasks: make(map[domain.TaskID]domain.Task)}
}

func (r *TaskRepository) Save(ctx context.Context, task domain.Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks[task.ID] = task
	return nil
}

func (r *TaskRepository) FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Task{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	task, found := r.tasks[id]
	return task, found, nil
}

func (r *TaskRepository) FindByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var tasks []domain.Task
	for _, task := range r.tasks {
		if task.ProjectID == projectID {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}
