package domain

import (
	"errors"
	"strings"
)

var (
	ErrProjectIDRequired        = errors.New("project ID is required")
	ErrProjectNameRequired      = errors.New("project name is required")
	ErrProjectWorkspaceRequired = errors.New("project workspace is required")
)

type ProjectID string

type Project struct {
	ID        ProjectID
	Name      string
	Workspace string
}

func NewProject(id ProjectID, name string, workspace string) (Project, error) {
	if strings.TrimSpace(string(id)) == "" {
		return Project{}, ErrProjectIDRequired
	}
	if strings.TrimSpace(name) == "" {
		return Project{}, ErrProjectNameRequired
	}
	if strings.TrimSpace(workspace) == "" {
		return Project{}, ErrProjectWorkspaceRequired
	}
	return Project{ID: id, Name: name, Workspace: workspace}, nil
}
