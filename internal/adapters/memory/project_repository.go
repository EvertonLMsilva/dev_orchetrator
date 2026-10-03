package memory

import (
	"context"
	"sync"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type ProjectRepository struct {
	mu       sync.RWMutex
	projects map[domain.ProjectID]domain.Project
}

var _ ports.ProjectRepository = (*ProjectRepository)(nil)

func NewProjectRepository() *ProjectRepository {
	return &ProjectRepository{projects: make(map[domain.ProjectID]domain.Project)}
}

func (r *ProjectRepository) Save(ctx context.Context, project domain.Project) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projects[project.ID] = project
	return nil
}

func (r *ProjectRepository) FindByID(ctx context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Project{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	project, found := r.projects[id]
	return project, found, nil
}
