package ports

import (
	"context"

	"dev-orchestrator/internal/domain"
)

// testTaskRepository exists only to check the interface at compile time.
type testTaskRepository struct{}

var _ TaskRepository = (*testTaskRepository)(nil)

func (*testTaskRepository) Save(context.Context, domain.Task) error {
	return nil
}

func (*testTaskRepository) FindByID(context.Context, domain.TaskID) (domain.Task, bool, error) {
	return domain.Task{}, false, nil
}

func (*testTaskRepository) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	return nil, nil
}
