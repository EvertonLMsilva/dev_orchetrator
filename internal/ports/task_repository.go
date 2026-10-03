package ports

import (
	"context"

	"dev-orchestrator/internal/domain"
)

// TaskRepository defines the persistence contract for tasks.
type TaskRepository interface {
	// Save conceptually persists or updates a task. Concrete behavior belongs
	// to the adapter, which returns a repository error when applicable.
	Save(ctx context.Context, task domain.Task) error

	// FindByID returns (task, true, nil) when found, (zero Task, false, nil)
	// when absent, and (zero Task, false, error) on repository failure.
	FindByID(ctx context.Context, id domain.TaskID) (domain.Task, bool, error)

	// FindByProject returns tasks belonging to projectID, with no ordering
	// guarantee. No matches returns an empty or nil slice and a nil error.
	FindByProject(ctx context.Context, projectID domain.ProjectID) ([]domain.Task, error)
}
