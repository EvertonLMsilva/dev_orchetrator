package application

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type dispatcherRepo struct {
	found bool
	calls int
	id    domain.ProjectID
	err   error
}

func (r *dispatcherRepo) Save(context.Context, domain.Project) error { return nil }
func (r *dispatcherRepo) FindByID(ctx context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	r.calls++
	r.id = id
	return domain.Project{Workspace: "trusted"}, r.found, r.err
}

type dispatcherPolicy struct{ decision domain.PolicyDecision }

func (p dispatcherPolicy) Evaluate(domain.Action) (domain.PolicyResult, error) {
	return domain.PolicyResult{Decision: p.decision, Reason: "test policy"}, nil
}

type dispatcherCapabilities struct {
	calls     []domain.ActionType
	ctx       context.Context
	workspace string
	params    domain.ActionParams
	err       error
}

func (f *dispatcherCapabilities) record(ctx context.Context, w string, k domain.ActionType) error {
	f.calls = append(f.calls, k)
	f.ctx = ctx
	f.workspace = w
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return f.err
}
func (f *dispatcherCapabilities) Search(ctx context.Context, w string, p domain.SearchParams) (ports.SearchResult, error) {
	f.params.Search = &p
	return ports.SearchResult{}, f.record(ctx, w, domain.ActionTypeSearch)
}
func (f *dispatcherCapabilities) ReadFile(ctx context.Context, w string, p domain.ReadFileParams) (ports.ReadFileResult, error) {
	f.params.ReadFile = &p
	return ports.ReadFileResult{}, f.record(ctx, w, domain.ActionTypeReadFile)
}
func (f *dispatcherCapabilities) GitStatus(ctx context.Context, w string) (ports.GitStatusResult, error) {
	return ports.GitStatusResult{}, f.record(ctx, w, domain.ActionTypeGitStatus)
}
func (f *dispatcherCapabilities) GitDiff(ctx context.Context, w string, p domain.GitDiffParams) (ports.GitDiffResult, error) {
	f.params.GitDiff = &p
	return ports.GitDiffResult{}, f.record(ctx, w, domain.ActionTypeGitDiff)
}
func (f *dispatcherCapabilities) RunTests(ctx context.Context, w string, p domain.RunTestsParams) (ports.RunTestsResult, error) {
	f.params.RunTests = &p
	return ports.RunTestsResult{Target: p.Target}, f.record(ctx, w, domain.ActionTypeRunTests)
}
func dispatcherActions() []domain.Action {
	return []domain.Action{
		{Type: domain.ActionTypeSearch, ProjectID: "project", Params: domain.ActionParams{Search: &domain.SearchParams{Query: "needle", Path: "src"}}},
		{Type: domain.ActionTypeReadFile, ProjectID: "project", Params: domain.ActionParams{ReadFile: &domain.ReadFileParams{Path: "file"}}},
		{Type: domain.ActionTypeGitStatus, ProjectID: "project"},
		{Type: domain.ActionTypeGitDiff, ProjectID: "project", Params: domain.ActionParams{GitDiff: &domain.GitDiffParams{Path: "src"}}},
		{Type: domain.ActionTypeRunTests, ProjectID: "project", Params: domain.ActionParams{RunTests: &domain.RunTestsParams{Target: "domain"}}},
	}
}
func TestLocalAgentRoutes(t *testing.T) {
	for _, a := range dispatcherActions() {
		t.Run(string(a.Type), func(t *testing.T) {
			repo := &dispatcherRepo{found: true}
			caps := &dispatcherCapabilities{}
			agent := NewLocalAgent(repo, domain.ActionPolicy{}, NewDefaultActionAllowlist(), caps)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result, err := agent.Execute(ctx, a)
			if err != nil {
				t.Fatal(err)
			}
			if result.Type != a.Type || result.Validate() != nil {
				t.Fatalf("invalid result: %+v", result)
			}
			if len(caps.calls) != 1 || caps.calls[0] != a.Type || caps.ctx != ctx || caps.workspace != "trusted" || repo.id != a.ProjectID {
				t.Fatalf("incorrect routing: %+v %+v", caps, repo)
			}
			if a.Params.Search != nil && *caps.params.Search != *a.Params.Search || a.Params.ReadFile != nil && *caps.params.ReadFile != *a.Params.ReadFile || a.Params.GitDiff != nil && *caps.params.GitDiff != *a.Params.GitDiff || a.Params.RunTests != nil && *caps.params.RunTests != *a.Params.RunTests {
				t.Fatal("params changed")
			}
		})
	}
}
func TestLocalAgentGuards(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision domain.PolicyDecision
		found    bool
		allow    bool
		mutate   func(*domain.Action)
		want     error
		lookups  int
	}{
		{"blocked", domain.PolicyDecisionBlocked, true, true, nil, ErrPolicyBlocked, 0},
		{"approval", domain.PolicyDecisionApproval, true, true, nil, ErrPolicyRequiresApproval, 0},
		{"invalid policy", "UNKNOWN", true, true, nil, domain.ErrUnknownPolicyDecision, 0},
		{"allowlist", domain.PolicyDecisionAuto, true, false, nil, ErrActionNotAllowed, 0},
		{"missing project", domain.PolicyDecisionAuto, false, true, nil, ErrLocalAgentProjectNotFound, 1},
		{"params", domain.PolicyDecisionAuto, true, true, func(a *domain.Action) { a.Params = domain.ActionParams{} }, domain.ErrInvalidActionParams, 0},
		{"unknown", domain.PolicyDecisionAuto, true, true, func(a *domain.Action) { a.Type = "SHELL" }, domain.ErrUnknownActionType, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dispatcherRepo{found: tc.found}
			caps := &dispatcherCapabilities{}
			allow := ActionAllowlist{}
			if tc.allow {
				allow = NewDefaultActionAllowlist()
			}
			agent := NewLocalAgent(repo, dispatcherPolicy{tc.decision}, allow, caps)
			a := dispatcherActions()[0]
			if tc.mutate != nil {
				tc.mutate(&a)
			}
			_, err := agent.Execute(context.Background(), a)
			if !errors.Is(err, tc.want) || len(caps.calls) != 0 || repo.calls != tc.lookups {
				t.Fatalf("err=%v calls=%v lookups=%d", err, caps.calls, repo.calls)
			}
		})
	}
}
func TestLocalAgentPreservesErrorsAndContext(t *testing.T) {
	sentinel := errors.New("dependency failure")
	for _, a := range dispatcherActions() {
		t.Run(string(a.Type), func(t *testing.T) {
			repo := &dispatcherRepo{found: true}
			caps := &dispatcherCapabilities{err: sentinel}
			agent := NewLocalAgent(repo, domain.ActionPolicy{}, NewDefaultActionAllowlist(), caps)
			_, err := agent.Execute(context.Background(), a)
			if !errors.Is(err, sentinel) {
				t.Fatal(err)
			}
			repo.err = sentinel
			caps.calls = nil
			_, err = agent.Execute(context.Background(), a)
			if !errors.Is(err, sentinel) || len(caps.calls) != 0 {
				t.Fatal("repository failure executed capability")
			}
			repo.err = nil
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = agent.Execute(ctx, a)
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

var _ ports.LocalAgent = (*LocalAgentDispatcher)(nil)

func TestLocalExecutorsPreserveCancellation(t *testing.T) {
	for _, a := range dispatcherActions() {
		t.Run(string(a.Type), func(t *testing.T) {
			repo := &dispatcherRepo{found: true}
			agent := NewLocalAgent(repo, nil, NewDefaultActionAllowlist(), nil)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err := agent.Execute(ctx, a)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func TestLocalAgentPreservesDeadline(t *testing.T) {
	for _, a := range dispatcherActions() {
		t.Run(string(a.Type), func(t *testing.T) {
			repo := &dispatcherRepo{found: true}
			caps := &dispatcherCapabilities{}
			agent := NewLocalAgent(repo, nil, NewDefaultActionAllowlist(), caps)
			ctx, cancel := context.WithTimeout(context.Background(), -1)
			defer cancel()
			_, err := agent.Execute(ctx, a)
			if !errors.Is(err, context.DeadlineExceeded) || caps.ctx != ctx {
				t.Fatalf("deadline lost: %v", err)
			}
		})
	}
}
