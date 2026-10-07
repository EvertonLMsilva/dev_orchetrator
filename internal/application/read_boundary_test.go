package application

import (
	"context"
	fileadapter "dev-orchestrator/internal/adapters/file"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func TestReadBoundaryPersistentOperationalTasks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	repo, err := fileadapter.NewTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	projects := &readProjects{project: domain.Project{ID: "dev-orchestrator", Name: "Dev Orchestrator"}}
	query := func(r *fileadapter.TaskRepository, offset int) readcontracts.ProjectTasksResponse {
		t.Helper()
		got, err := newReadBoundary(projects, r, nil).dispatch(ctx, "project.tasks", readcontracts.ProjectTasksRequest{ProjectID: "dev-orchestrator", CorrelationID: "persistent-tasks", Limit: 1, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		return got.(readcontracts.ProjectTasksResponse)
	}
	if got := query(repo, 0); got.Tasks == nil || len(got.Tasks) != 0 || got.HasMore {
		t.Fatal(got)
	}
	for _, task := range []domain.Task{
		{ID: "b", ProjectID: "dev-orchestrator", Title: "Conversation request", Status: domain.TaskStatusCancelled},
		{ID: "a", ProjectID: "dev-orchestrator", Title: "Conversation request", Status: domain.TaskStatusPlanned},
		{ID: "0", ProjectID: "other", Title: "Conversation request", Status: domain.TaskStatusPlanned},
	} {
		if err := repo.Save(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening exercises the existing startup snapshot contract.
	restarted, err := fileadapter.NewTaskRepository(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*fileadapter.TaskRepository{repo, restarted} {
		if got := query(r, 0); len(got.Tasks) != 1 || got.Tasks[0].TaskID != "a" || !got.HasMore {
			t.Fatal(got)
		}
		if got := query(r, 1); len(got.Tasks) != 1 || got.Tasks[0].TaskID != "b" || got.HasMore {
			t.Fatal(got)
		}
		if got := query(r, 2); got.Tasks == nil || len(got.Tasks) != 0 || got.HasMore {
			t.Fatal(got)
		}
	}
}

func TestReadBoundaryAvailabilityDoesNotProbeGit(t *testing.T) {
	p := &readProjects{project: domain.Project{ID: "p", Name: "P"}}
	git := &readGit{err: errors.New("backend unavailable")}
	b := newReadBoundary(p, nil, NewLocalAgent(p, nil, NewReadOnlyActionAllowlist(), git))
	req := readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "availability"}
	for i := 0; i < 2; i++ {
		got, err := b.dispatch(context.Background(), "project.status", req)
		if err != nil || got.(readcontracts.ProjectStatusResponse).Queries.GitStatus != readcontracts.Available || git.calls != i {
			t.Fatal(got, err, git.calls)
		}
		if i == 0 {
			if _, err := b.dispatch(context.Background(), "git.status", readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "failure"}); err == nil {
				t.Fatal("expected Git failure")
			}
		}
	}
}

type readProjects struct {
	ports.ProjectRepository
	project domain.Project
	err     error
	calls   int
}

func (p *readProjects) FindByID(context.Context, domain.ProjectID) (domain.Project, bool, error) {
	p.calls++
	return p.project, p.project.ID != "", p.err
}

type readTasks struct {
	ports.TaskRepository
	tasks []domain.Task
	err   error
}

func (p *readTasks) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	return p.tasks, p.err
}

type readGit struct {
	LocalCapabilities
	calls     int
	workspace string
	err       error
}

func (g *readGit) GitStatus(_ context.Context, w string) (ports.GitStatusResult, error) {
	g.calls++
	g.workspace = w
	return ports.GitStatusResult{Entries: []ports.GitStatusEntry{{Path: "secret-path"}}}, g.err
}

func TestReadBoundary(t *testing.T) {
	ctx := context.Background()
	p := &readProjects{project: domain.Project{ID: "p", Name: "Project", Workspace: "trusted-root"}}
	tasks := &readTasks{tasks: []domain.Task{{ID: "b", ProjectID: "p", Title: "B", Status: domain.TaskStatusDone}, {ID: "a", ProjectID: "p", Title: "A", Status: domain.TaskStatusPlanned}}}
	git := &readGit{}
	agent := NewLocalAgent(p, nil, NewReadOnlyActionAllowlist(), git)
	b := newReadBoundary(p, tasks, agent)
	got, err := b.dispatch(ctx, "project.status", readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	status := got.(readcontracts.ProjectStatusResponse)
	if status.Name != "Project" || status.ProjectID != "p" || status.CorrelationID != "c" || status.ObservedAt.IsZero() || status.Queries.ExecutionStatus != readcontracts.Unavailable {
		t.Fatal(status)
	}
	got, err = b.dispatch(ctx, "project.tasks", readcontracts.ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	list := got.(readcontracts.ProjectTasksResponse)
	if len(list.Tasks) != 1 || list.Tasks[0].TaskID != "a" || !list.HasMore || tasks.tasks[0].ID != "b" {
		t.Fatal(list)
	}
	got, err = b.dispatch(ctx, "git.status", readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "c"})
	if err != nil || got.(readcontracts.GitStatusResponse).ChangedEntries != 1 || git.workspace != "trusted-root" {
		t.Fatal(got, err)
	}
	got, err = b.dispatch(ctx, "execution.status", readcontracts.ExecutionStatusRequest{ProjectID: "p", CorrelationID: "c"})
	if err != nil || got.(readcontracts.ExecutionStatusResponse).ExecutionObservation != readcontracts.Unavailable {
		t.Fatal(got, err)
	}
	before := p.calls
	for _, tc := range []struct {
		op  string
		req any
	}{{"write", readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}}, {"git.status", readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}}, {"project.status", readcontracts.ProjectStatusRequest{}}, {"project.status", nil}} {
		if out, e := b.dispatch(ctx, tc.op, tc.req); e == nil || out != nil {
			t.Fatal(tc, out, e)
		}
	}
	if p.calls != before {
		t.Fatal("invalid input accessed repository")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if out, e := b.dispatch(cancelled, "project.status", readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}); !errors.Is(e, context.Canceled) || out != nil {
		t.Fatal(out, e)
	}
}

func TestReadBoundaryFailClosed(t *testing.T) {
	ctx := context.Background()
	req := readcontracts.ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 1}
	p := &readProjects{project: domain.Project{ID: "p", Name: "P", Workspace: "root"}}
	tasks := &readTasks{}
	b := newReadBoundary(p, tasks, nil)
	for _, bad := range [][]domain.Task{
		{{ID: "x", ProjectID: "other", Title: "X", Status: domain.TaskStatusDone}},
		{{ID: "x", ProjectID: "p", Title: "X", Status: "RUNNING"}},
		{{ID: "x", ProjectID: "p", Title: "X", Status: domain.TaskStatusDone}, {ID: "x", ProjectID: "p", Title: "X", Status: domain.TaskStatusDone}},
	} {
		tasks.tasks = bad
		if out, e := b.dispatch(ctx, "project.tasks", req); e == nil || out != nil {
			t.Fatal(out, e)
		}
	}
	tasks.tasks = nil
	got, e := b.dispatch(ctx, "project.tasks", req)
	if e != nil || got.(readcontracts.ProjectTasksResponse).Tasks == nil {
		t.Fatal(got, e)
	}
	req.Offset = 100
	got, e = b.dispatch(ctx, "project.tasks", req)
	if e != nil || got.(readcontracts.ProjectTasksResponse).HasMore {
		t.Fatal(got, e)
	}
	p.project.ID = "other"
	if out, e := b.dispatch(ctx, "project.tasks", req); e == nil || out != nil {
		t.Fatal(out, e)
	}
	p.project.ID = "p"
	tasks.err = errors.New("secret backend detail")
	if out, e := b.dispatch(ctx, "project.tasks", req); e != errReadQuery || out != nil {
		t.Fatal(out, e)
	}
	p.err = tasks.err
	if out, e := b.dispatch(ctx, "project.tasks", req); e != errReadQuery || out != nil {
		t.Fatal(out, e)
	}
	p.err = nil
	p.project = domain.Project{}
	if out, e := b.dispatch(ctx, "project.tasks", req); e == nil || out != nil {
		t.Fatal(out, e)
	}
	p.project = domain.Project{ID: "p", Name: "P", Workspace: "root"}
	git := &readGit{}
	b = newReadBoundary(p, tasks, NewLocalAgent(p, nil, ActionAllowlist{}, git))
	if out, e := b.dispatch(ctx, "git.status", readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "c"}); e == nil || out != nil || git.calls != 0 {
		t.Fatal(out, e)
	}
	git.err = errors.New("secret git failure")
	b = newReadBoundary(p, tasks, NewLocalAgent(p, nil, NewReadOnlyActionAllowlist(), git))
	if out, e := b.dispatch(ctx, "git.status", readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "c"}); e != errReadQuery || out != nil || git.calls != 1 {
		t.Fatal(out, e)
	}
	p.project.Name = ""
	if out, e := b.dispatch(ctx, "project.status", readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}); e == nil || out != nil {
		t.Fatal("invalid response returned", out, e)
	}
	b = newReadBoundary(nil, nil, nil)
	if out, e := b.dispatch(ctx, "project.tasks", req); e == nil || out != nil {
		t.Fatal(out, e)
	}
}

func TestReadBoundaryOutputLimit(t *testing.T) {
	p := &readProjects{project: domain.Project{ID: "p", Name: "P"}}
	tasks := &readTasks{}
	for i := 0; i < readcontracts.MaxTasks; i++ {
		tasks.tasks = append(tasks.tasks, domain.Task{ID: domain.TaskID(fmt.Sprint(i)), ProjectID: "p", Title: strings.Repeat("\x01", readcontracts.MaxTextBytes), Status: domain.TaskStatusDone})
	}
	b := newReadBoundary(p, tasks, nil)
	if out, e := b.dispatch(context.Background(), "project.tasks", readcontracts.ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 100}); e == nil || out != nil {
		t.Fatal("accepted oversized encoded response", e)
	}
}
