package ports

import (
	"context"

	"dev-orchestrator/internal/domain"
)

// testProjectRepository exists only to check the interface at compile time.
type testProjectRepository struct{}

var _ ProjectRepository = (*testProjectRepository)(nil)

func (*testProjectRepository) Save(context.Context, domain.Project) error {
	return nil
}

func (*testProjectRepository) FindByID(context.Context, domain.ProjectID) (domain.Project, bool, error) {
	return domain.Project{}, false, nil
}
