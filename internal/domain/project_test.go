package domain

import "testing"

func TestNewProjectPreservesOriginalValues(t *testing.T) {
	id := ProjectID(" \tproject-1\n")
	name := " \tMy Project\n"
	workspace := " \t./workspace\n"

	got, err := NewProject(id, name, workspace)
	if err != nil {
		t.Fatalf("NewProject() error = %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.Name != name {
		t.Errorf("Name = %q, want %q", got.Name, name)
	}
	if got.Workspace != workspace {
		t.Errorf("Workspace = %q, want %q", got.Workspace, workspace)
	}
}

func TestNewProjectRequiredFields(t *testing.T) {
	tests := []struct {
		name        string
		id          ProjectID
		projectName string
		workspace   string
		wantErr     error
	}{
		{"empty ID", "", "project", "workspace", ErrProjectIDRequired},
		{"whitespace ID", " \t\r\n\u2003", "project", "workspace", ErrProjectIDRequired},
		{"empty Name", "id", "", "workspace", ErrProjectNameRequired},
		{"whitespace Name", "id", " \t\r\n\u2003", "workspace", ErrProjectNameRequired},
		{"empty Workspace", "id", "project", "", ErrProjectWorkspaceRequired},
		{"whitespace Workspace", "id", "project", " \t\r\n\u2003", ErrProjectWorkspaceRequired},
		{"ID precedes Name and Workspace", "", "", "", ErrProjectIDRequired},
		{"Name precedes Workspace", "id", "", "", ErrProjectNameRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewProject(tt.id, tt.projectName, tt.workspace)
			if err != tt.wantErr {
				t.Errorf("NewProject() error = %v, want sentinel %v", err, tt.wantErr)
			}
		})
	}
}
