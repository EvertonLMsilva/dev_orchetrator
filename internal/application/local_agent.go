package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"fmt"
)

var (
	ErrLocalAgentProjectNotFound = errors.New("local agent project not found")
	ErrPolicyRequiresApproval    = errors.New("action policy requires approval")
	ErrPolicyBlocked             = errors.New("action policy blocked")
	ErrActionNotAllowed          = errors.New("action is not allowlisted")
)

// LocalActionPolicy classifies actions; it cannot execute or create approvals.
type LocalActionPolicy interface {
	Evaluate(domain.Action) (domain.PolicyResult, error)
}

// LocalCapabilities provides only the five existing typed capabilities.
// Dependencies are supplied by application composition, never by Actions.
type LocalCapabilities interface {
	Search(context.Context, string, domain.SearchParams) (ports.SearchResult, error)
	ReadFile(context.Context, string, domain.ReadFileParams) (ports.ReadFileResult, error)
	GitStatus(context.Context, string) (ports.GitStatusResult, error)
	GitDiff(context.Context, string, domain.GitDiffParams) (ports.GitDiffResult, error)
	RunTests(context.Context, string, domain.RunTestsParams) (ports.RunTestsResult, error)
}

type LocalAgentDispatcher struct {
	projects     ports.ProjectRepository
	policy       LocalActionPolicy
	allowlist    ActionAllowlist
	capabilities LocalCapabilities
}

// NewLocalAgent uses existing executors when capabilities is nil and the
// domain policy when policy is nil. A zero allowlist continues to deny all.
func NewLocalAgent(projects ports.ProjectRepository, policy LocalActionPolicy, allowlist ActionAllowlist, capabilities LocalCapabilities) *LocalAgentDispatcher {
	if policy == nil {
		policy = domain.ActionPolicy{}
	}
	if capabilities == nil {
		capabilities = localExecutors{}
	}
	return &LocalAgentDispatcher{projects: projects, policy: policy, allowlist: allowlist, capabilities: capabilities}
}

func (d *LocalAgentDispatcher) Execute(ctx context.Context, action domain.Action) (ports.ActionResult, error) {
	if err := action.Validate(); err != nil {
		return ports.ActionResult{}, errors.Join(ErrPolicyBlocked, err)
	}
	decision, err := d.policy.Evaluate(action)
	if err != nil {
		return ports.ActionResult{}, fmt.Errorf("evaluate action policy: %w", err)
	}
	if err := decision.Validate(); err != nil {
		return ports.ActionResult{}, errors.Join(ErrPolicyBlocked, err)
	}
	switch decision.Decision {
	case domain.PolicyDecisionAuto:
	case domain.PolicyDecisionApproval:
		return ports.ActionResult{}, ErrPolicyRequiresApproval
	default:
		return ports.ActionResult{}, ErrPolicyBlocked
	}
	if !d.allowlist.Allows(action.Type) {
		return ports.ActionResult{}, ErrActionNotAllowed
	}
	if d.projects == nil {
		return ports.ActionResult{}, ErrLocalAgentProjectNotFound
	}
	project, found, err := d.projects.FindByID(ctx, action.ProjectID)
	if err != nil {
		return ports.ActionResult{}, fmt.Errorf("find action project: %w", err)
	}
	if !found {
		return ports.ActionResult{}, ErrLocalAgentProjectNotFound
	}
	result := ports.ActionResult{Type: action.Type}
	switch action.Type {
	case domain.ActionTypeSearch:
		value, e := d.capabilities.Search(ctx, project.Workspace, *action.Params.Search)
		result.SearchResult = &value
		err = e
	case domain.ActionTypeReadFile:
		value, e := d.capabilities.ReadFile(ctx, project.Workspace, *action.Params.ReadFile)
		result.ReadFileResult = &value
		err = e
	case domain.ActionTypeGitStatus:
		value, e := d.capabilities.GitStatus(ctx, project.Workspace)
		result.GitStatusResult = &value
		err = e
	case domain.ActionTypeGitDiff:
		value, e := d.capabilities.GitDiff(ctx, project.Workspace, *action.Params.GitDiff)
		result.GitDiffResult = &value
		err = e
	case domain.ActionTypeRunTests:
		value, e := d.capabilities.RunTests(ctx, project.Workspace, *action.Params.RunTests)
		result.RunTestsResult = &value
		err = e
	default:
		return ports.ActionResult{}, ErrPolicyBlocked
	}
	if validationErr := result.Validate(); validationErr != nil {
		return ports.ActionResult{}, validationErr
	}
	return result, err
}

// localExecutors delegates without constructing commands or changing limits.
type localExecutors struct{}

func (localExecutors) Search(ctx context.Context, w string, p domain.SearchParams) (ports.SearchResult, error) {
	return (SearchExecutor{}).ExecuteContext(ctx, w, p.Query, p.Path)
}
func (localExecutors) ReadFile(ctx context.Context, w string, p domain.ReadFileParams) (ports.ReadFileResult, error) {
	return (ReadFileExecutor{}).ExecuteContext(ctx, w, p.Path)
}
func (localExecutors) GitStatus(ctx context.Context, w string) (ports.GitStatusResult, error) {
	return (GitStatusExecutor{}).Execute(ctx, w)
}
func (localExecutors) GitDiff(ctx context.Context, w string, p domain.GitDiffParams) (ports.GitDiffResult, error) {
	return (GitDiffExecutor{}).Execute(ctx, w, p.Path)
}
func (localExecutors) RunTests(ctx context.Context, w string, p domain.RunTestsParams) (ports.RunTestsResult, error) {
	return RunTests(ctx, w, p)
}

var _ ports.LocalAgent = (*LocalAgentDispatcher)(nil)
