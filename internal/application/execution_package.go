package application

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var (
	ErrExecutionProjectNotFound = errors.New("execution project not found")
	ErrUnsafeExecutionPackage   = errors.New("unsafe execution workspace or scope")
)

// ExecutionPackage carries validated, declarative authority for a future provider.
// It does not enforce runtime writes or protect against later filesystem changes.
type ExecutionPackage struct {
	ProjectID          domain.ProjectID
	TaskID             domain.TaskID
	Workspace          string
	Objective          string
	Scope              []string
	Constraints        []string
	AcceptanceCriteria []string
}

type ExecutionPackageBuilder struct{ projects ports.ProjectRepository }

func NewExecutionPackageBuilder(projects ports.ProjectRepository) *ExecutionPackageBuilder {
	return &ExecutionPackageBuilder{projects: projects}
}

func (b *ExecutionPackageBuilder) Build(ctx context.Context, request ports.ExecutorRequest) (ExecutionPackage, error) {
	if err := request.Validate(); err != nil {
		return ExecutionPackage{}, err
	}
	if b == nil || b.projects == nil {
		return ExecutionPackage{}, ErrExecutionProjectNotFound
	}
	project, found, err := b.projects.FindByID(ctx, request.ProjectID)
	if err != nil {
		return ExecutionPackage{}, fmt.Errorf("resolve execution project: %w", err)
	}
	if !found || project.ID != request.ProjectID {
		return ExecutionPackage{}, ErrExecutionProjectNotFound
	}
	// A relative registry entry must never fall back to the process directory.
	if !filepath.IsAbs(project.Workspace) {
		return ExecutionPackage{}, ErrUnsafeExecutionPackage
	}
	sandbox := WorkspaceSandbox{}
	workspace, err := sandbox.Resolve(project.Workspace, ".")
	if err != nil {
		return ExecutionPackage{}, fmt.Errorf("%w: workspace: %w", ErrUnsafeExecutionPackage, err)
	}
	for _, path := range request.Spec.Scope {
		if _, err := sandbox.Resolve(workspace, path); err != nil {
			return ExecutionPackage{}, fmt.Errorf("%w: scope %q: %w", ErrUnsafeExecutionPackage, path, err)
		}
	}
	return ExecutionPackage{
		ProjectID: request.ProjectID, TaskID: request.TaskID, Workspace: workspace,
		Objective:          request.Spec.Objective,
		Scope:              append([]string(nil), request.Spec.Scope...),
		Constraints:        append([]string(nil), request.Spec.Constraints...),
		AcceptanceCriteria: append([]string(nil), request.Spec.AcceptanceCriteria...),
	}, nil
}
