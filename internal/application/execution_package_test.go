package application

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type packageProjects struct {
	project   domain.Project
	found     bool
	err       error
	requested domain.ProjectID
}

func (r *packageProjects) Save(context.Context, domain.Project) error { return nil }
func (r *packageProjects) FindByID(_ context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	r.requested = id
	return r.project, r.found, r.err
}
func packageRequest() ports.ExecutorRequest {
	return ports.ExecutorRequest{ProjectID: "project", TaskID: "task", Spec: ports.ExecutorTaskSpec{Objective: "Implement change", Scope: []string{"src/a.go", "src/b.go"}, Constraints: []string{"Keep contracts"}, AcceptanceCriteria: []string{"Tests pass"}}}
}

func TestExecutionPackageBuild(t *testing.T) {
	workspace := t.TempDir()
	repo := &packageProjects{project: domain.Project{ID: "project", Workspace: workspace}, found: true}
	request := packageRequest()
	got, err := NewExecutionPackageBuilder(repo).Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	want := ExecutionPackage{ProjectID: request.ProjectID, TaskID: request.TaskID, Workspace: root, Objective: request.Spec.Objective, Scope: append([]string(nil), request.Spec.Scope...), Constraints: append([]string(nil), request.Spec.Constraints...), AcceptanceCriteria: append([]string(nil), request.Spec.AcceptanceCriteria...)}
	if repo.requested != request.ProjectID || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	request.Spec.Scope[0] = "elsewhere"
	request.Spec.Constraints[0] = "changed"
	request.Spec.AcceptanceCriteria[0] = "changed"
	if !reflect.DeepEqual(got, want) {
		t.Fatal("caller mutation changed package")
	}
}

func TestExecutionPackageRejects(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("repository failure")
	for _, tc := range []struct {
		name   string
		change func(*ports.ExecutorRequest, *packageProjects)
		want   error
	}{
		{"project ID", func(r *ports.ExecutorRequest, p *packageProjects) { r.ProjectID = "" }, ports.ErrInvalidExecutorRequest},
		{"task ID", func(r *ports.ExecutorRequest, p *packageProjects) { r.TaskID = "" }, ports.ErrInvalidExecutorRequest},
		{"objective", func(r *ports.ExecutorRequest, p *packageProjects) { r.Spec.Objective = "" }, ports.ErrInvalidExecutorRequest},
		{"unknown", func(r *ports.ExecutorRequest, p *packageProjects) { p.found = false }, ErrExecutionProjectNotFound},
		{"repository error", func(r *ports.ExecutorRequest, p *packageProjects) { p.err = failure }, failure},
		{"mismatched project", func(r *ports.ExecutorRequest, p *packageProjects) { p.project.ID = "other" }, ErrExecutionProjectNotFound},
		{"empty workspace", func(r *ports.ExecutorRequest, p *packageProjects) { p.project.Workspace = "" }, ErrUnsafeExecutionPackage},
		{"relative workspace", func(r *ports.ExecutorRequest, p *packageProjects) { p.project.Workspace = "relative" }, ErrUnsafeExecutionPackage},
		{"missing workspace", func(r *ports.ExecutorRequest, p *packageProjects) {
			p.project.Workspace = filepath.Join(workspace, "missing")
		}, ErrUnsafeExecutionPackage},
		{"file workspace", func(r *ports.ExecutorRequest, p *packageProjects) { p.project.Workspace = file }, ErrUnsafeExecutionPackage},
		{"traversal", func(r *ports.ExecutorRequest, p *packageProjects) { r.Spec.Scope[1] = "../outside" }, ports.ErrInvalidExecutorRequest},
		{"absolute", func(r *ports.ExecutorRequest, p *packageProjects) { r.Spec.Scope[1] = "/outside" }, ports.ErrInvalidExecutorRequest},
		{"file ancestor", func(r *ports.ExecutorRequest, p *packageProjects) { r.Spec.Scope[1] = "file/child" }, ErrUnsafeExecutionPackage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := packageRequest()
			repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: workspace}, found: true}
			tc.change(&request, repo)
			got, err := NewExecutionPackageBuilder(repo).Build(context.Background(), request)
			if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, ExecutionPackage{}) {
				t.Fatalf("got %#v, %v; want zero package and %v", got, err, tc.want)
			}
		})
	}
}

func TestExecutionPackageSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	for _, tc := range []struct{ name, target string }{{"external", outside}, {"dangling", filepath.Join(outside, "missing")}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.Symlink(tc.target, filepath.Join(workspace, tc.name)); err != nil {
				t.Fatal(err)
			}
			request := packageRequest()
			request.Spec.Scope[1] = tc.name + "/new.go"
			repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: workspace}, found: true}
			got, err := NewExecutionPackageBuilder(repo).Build(context.Background(), request)
			if !errors.Is(err, ErrUnsafeExecutionPackage) || !reflect.DeepEqual(got, ExecutionPackage{}) {
				t.Fatalf("got %#v, %v", got, err)
			}
		})
	}
}
