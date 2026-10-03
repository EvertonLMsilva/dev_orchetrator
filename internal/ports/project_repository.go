package ports

import (
	"context"

	"dev-orchestrator/internal/domain"
)

// ProjectRepository defines the persistence contract for projects.
type ProjectRepository interface {
	// Save conceptually persists or updates a project.
	Save(ctx context.Context, project domain.Project) error

	// FindByID returns (project, true, nil) when found, (zero Project, false,
	// nil) when absent, and (zero Project, false, error) on repository failure.
	// The bool indicates only whether the project was found.
	FindByID(ctx context.Context, id domain.ProjectID) (domain.Project, bool, error)
}
