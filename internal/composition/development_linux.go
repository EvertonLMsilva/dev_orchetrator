//go:build linux

package composition

import (
	"context"
	filestore "dev-orchestrator/internal/adapters/file"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/infrastructure/provider"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type DevelopmentConfig struct {
	ProjectID                            domain.ProjectID
	TaskID                               domain.TaskID
	CorrelationID, Branch, PolicyVersion string
	Policy                               domain.CandidatePolicy
	Route                                application.ConversationSource
	Mappings                             []infrastructure.ActorMapping
	Grants                               []ports.Grant
	ControlRoot, ScratchRoot, StateRoot  string
}
type developmentEffects struct {
	store      *infrastructure.ManagedWorkspaceStore
	workspace  infrastructure.ManagedWorkspace
	repository domain.ManagedRepositoryIdentity
	policy     domain.CandidatePolicy
	version    string
}

func (e *developmentEffects) Apply(ctx context.Context, a domain.CandidateArtifact, b domain.WriteBinding, id domain.ApprovalID, tx string) (domain.WriteTerminalResult, error) {
	reservation, err := application.NewWriteAuthorizationService(e.store, time.Now).Reserve(ctx, application.WriteAuthorizationRequest{ApprovalID: id, TransactionID: tx, Candidate: a.Candidate(), Context: b})
	if err != nil {
		return domain.WriteTerminalResult{}, err
	}
	if !reservation.Granted {
		return domain.WriteTerminalResult{}, domain.ErrWriteDenied
	}
	return e.store.Apply(ctx, infrastructure.MediatedWriteRequest{Workspace: e.workspace, Policy: e.policy, Artifact: a, Context: b, ApprovalID: id, TransactionID: tx})
}
func (e *developmentEffects) BranchBinding(c domain.CandidateContext, branch string) (domain.GitBinding, error) {
	b := domain.GitBinding{OperationKind: domain.GitBranch, ProjectID: c.ProjectID, TaskID: c.TaskID, CorrelationID: c.CorrelationID, WorkspaceIdentity: e.workspace.Identity(), RepositoryIdentity: e.repository.Identity(), ExpectedHead: e.repository.InitialHead, StartPoint: e.repository.InitialHead, BranchName: branch, PolicyVersion: e.version}
	b.OperationParametersIdentity = b.ParametersIdentity()
	return b, b.Validate()
}
func (e *developmentEffects) Git(ctx context.Context, op, id string, b domain.GitBinding) (application.DevelopmentGitResult, error) {
	r, err := e.store.MutateGit(ctx, e.workspace, e.repository, op, id, b)
	return application.DevelopmentGitResult{State: r.State, OID: r.OID, Tree: r.Tree}, err
}
func (e *developmentEffects) CommitBinding(ctx context.Context, write, branch string, b domain.GitBinding) (domain.GitBinding, error) {
	actor := domain.GitActor{Name: "Dev Orchestrator", Email: "orchestrator@local.invalid", UnixSeconds: time.Now().Unix()}
	return e.store.PlanGitCommit(ctx, e.workspace, e.repository, write, branch, b, "Controlled pilot change\n", actor, actor)
}
func (e *developmentEffects) Verify(ctx context.Context, r application.DevelopmentResult) error {
	return e.store.VerifyManagedCommit(ctx, e.workspace, e.repository, r.WriteTransaction, r.Branch, r.CommitOID, r.Tree, r.CandidateIdentity)
}

// DevelopmentService is a separately selected composition of the existing
// application / P10 boundaries. P6 and MCP constructors remain READ-only.
type DevelopmentService struct {
	*application.DevelopmentCycle
	mu      sync.Mutex
	config  DevelopmentConfig
	effects *developmentEffects
	auth    ports.ActorAuthenticationPort
	home    *provider.CodexRuntimeHome
	store   *infrastructure.ManagedWorkspaceStore
	stopped bool
	lease   *auditStore
}

func composeDevelopment(ctx context.Context, c DevelopmentConfig, p ports.Planner, g ports.CandidateGenerator, authority *infrastructure.ActorAuthority, store *infrastructure.ManagedWorkspaceStore, w infrastructure.ManagedWorkspace, repo domain.ManagedRepositoryIdentity, scratch, state string, existingLease ...*auditStore) (*DevelopmentService, error) {
	var lease *auditStore
	var err error
	if len(existingLease) == 1 {
		lease = existingLease[0]
	} else {
		lease, err = openAudit(state)
		if err != nil {
			return nil, err
		}
	}
	completed := false
	defer func() {
		if !completed {
			lease.close()
		}
	}()
	tasks, err := filestore.NewDevelopmentTaskRepository(state)
	if err != nil {
		return nil, err
	}
	if _, found, err := tasks.FindByID(ctx, c.TaskID); err != nil || found {
		return nil, domain.ErrWriteDenied
	}
	task, err := domain.NewTask(c.TaskID, c.ProjectID, "Conversation request")
	if err != nil {
		return nil, err
	}
	if err := tasks.Save(ctx, task); err != nil {
		return nil, err
	}
	workflow := application.NewWorkflowEngine(tasks)
	for _, status := range []domain.TaskStatus{domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing} {
		if _, err := workflow.Transition(ctx, c.TaskID, status); err != nil {
			return nil, err
		}
	}
	effects := &developmentEffects{store: store, workspace: w, repository: repo, policy: c.Policy.Clone(), version: c.PolicyVersion}
	factory := infrastructure.NewManagedCandidateWorkspaceFactory(store, w, scratch)
	projects := memory.NewProjectRepository()
	project, err := domain.NewProject(c.ProjectID, "Controlled pilot", "managed:"+w.Identity())
	if err != nil {
		return nil, err
	}
	if err := projects.Save(ctx, project); err != nil {
		return nil, err
	}
	orchestrator := application.NewCandidatePlanningOrchestrator(application.NewContextBuilder(projects, tasks), p)
	cycle, err := application.NewDevelopmentCycle(application.DevelopmentCycleConfig{Context: domain.CandidateContext{ProjectID: c.ProjectID, TaskID: c.TaskID, CorrelationID: c.CorrelationID}, WorkspaceIdentity: w.Identity(), PolicyVersion: c.PolicyVersion, Branch: c.Branch, Policy: c.Policy, Timeout: time.Minute, ApprovalTTL: 10 * time.Minute}, orchestrator, tasks, authority, application.NewCandidatePipeline(g, factory), application.NewApprovalIssuer(authority, authority, store, time.Now), effects, time.Now)
	if err != nil {
		return nil, err
	}
	completed = true
	return &DevelopmentService{DevelopmentCycle: cycle, config: c, effects: effects, lease: lease, auth: authority}, nil
}
func NewDevelopmentService(ctx context.Context, c DevelopmentConfig) (*DevelopmentService, error) {
	if c.Validate() != nil {
		return nil, ErrConfig
	}
	for _, path := range []string{c.ControlRoot, c.ScratchRoot, c.StateRoot} {
		entries, err := os.ReadDir(path)
		if err != nil || len(entries) != 0 {
			return nil, ErrConfig
		}
	}
	authority, err := infrastructure.NewActorAuthority(c.Mappings, c.Grants)
	if err != nil {
		return nil, err
	}
	// A pilot is provisioned once into fresh private roots. Restart / replay is
	// denied before bootstrap effects; P10 recovery stays explicit.
	lease, err := openAudit(c.StateRoot)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			lease.close()
		}
	}()
	for _, path := range []string{filepath.Join(c.StateRoot, "tasks.json"), filepath.Join(c.ControlRoot, "state.json")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return nil, ErrConfig
		}
	}
	store, err := infrastructure.NewManagedWorkspaceStore(c.ControlRoot, c.CorrelationID)
	if err != nil {
		return nil, err
	}
	// Provision creates a fresh owned pilot, never imports a configured repo.
	w, err := store.Provision(ctx, c.ProjectID, nil)
	if err != nil {
		store.Close()
		return nil, err
	}
	repo, err := store.ProvisionRepository(ctx, w, c.Policy, c.PolicyVersion)
	if err != nil {
		store.Close()
		return nil, err
	}
	home, err := provider.NewCodexRuntimeHome()
	if err != nil {
		store.Close()
		return nil, err
	}
	s, err := composeDevelopment(ctx, c, infrastructure.NewCodexPlannerAdapter(provider.NewToolFreeCodexPlannerRuntime(home)), provider.NewToolFreeCandidateGenerator(home), authority, store, w, repo, c.ScratchRoot, c.StateRoot, lease)
	if err != nil {
		home.Close()
		store.Close()
		return nil, err
	}
	transferred = true
	s.home = home
	s.store = store
	return s, nil
}
func (s *DevelopmentService) Verify(ctx context.Context, r application.DevelopmentResult) error {
	if r.ProjectID != s.config.ProjectID || r.TaskID != s.config.TaskID || r.CorrelationID != s.config.CorrelationID || r.TaskState != domain.TaskStatusDone {
		return domain.ErrWriteDenied
	}
	return s.effects.store.VerifyManagedCommit(ctx, s.effects.workspace, s.effects.repository, r.WriteTransaction, r.Branch, r.CommitOID, r.Tree, r.CandidateIdentity)
}
func (s *DevelopmentService) Handle(ctx context.Context, input application.ConversationInput) application.ConversationResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return s.denied(ctx, input.Actor, "SERVICE_STOPPED")
	}
	if input.Source != s.config.Route {
		return s.denied(ctx, input.Actor, "REQUEST_ROUTE")
	}
	var out application.DevelopmentOutput
	var err error
	switch input.DevelopmentAction {
	case "begin":
		out, err = s.Begin(ctx, input.Actor, input.Text)
	case "confirm":
		out, err = s.Confirm(ctx, input.Actor, input.Confirmation)
	case "cancel":
		out, err = s.Cancel(ctx, input.Actor)
	default:
		return s.denied(ctx, input.Actor, "REQUEST_ACTION")
	}
	if err != nil {
		// Authenticated actors receive trusted partial receipts after a failed
		// gate. Unknown identities receive no candidate / result disclosure.
		if _, authErr := s.auth.AuthenticateActor(context.WithoutCancel(ctx), input.Actor); authErr == nil && (out.Result.CandidateIdentity != "" || out.Result.TaskState == domain.TaskStatusBlocked || out.Result.PlannerFailureStage != "") {
			if out.Result.TaskState == domain.TaskStatusBlocked && out.Review == nil {
				if persistErr := s.persistOutput(out); persistErr != nil {
					s.stopped = true
				}
			}
			data, _ := json.Marshal(out.Result)
			return application.ConversationResponse{Status: "REJECTED", Message: "Gate negado; efeitos anteriores preservados. Evidência: " + string(data)}
		}
		return application.ConversationResponse{Status: "REJECTED", Message: "Solicitação negada. Referência: " + s.config.CorrelationID}
	}
	if err := s.persistOutput(out); err != nil {
		s.stopped = true
		return application.ConversationResponse{Status: "BLOCKED", Message: "Persistência de evidência bloqueada."}
	}
	if out.Review != nil {
		data, _ := json.Marshal(out.Review)
		if len(data) > 1800 {
			return application.ConversationResponse{Status: "REVIEW_REQUIRED", Message: "Proposta excede limite de apresentação; nenhum efeito autorizado."}
		}
		return application.ConversationResponse{Status: "REVIEW_REQUIRED", Message: "Revise a operação e confirme sua identidade com /develop-confirm.\n" + string(data)}
	}
	if out.Result.TaskState == domain.TaskStatusDone && s.Verify(ctx, out.Result) != nil {
		return application.ConversationResponse{Status: "BLOCKED", Message: "Verificação final bloqueada."}
	}
	data, _ := json.Marshal(out.Result)
	return application.ConversationResponse{Status: string(out.Result.TaskState), Message: fmt.Sprintf("Resultado verificado: %s", data)}
}
func (s *DevelopmentService) denied(ctx context.Context, actor ports.ActorEvidence, stage string) application.ConversationResponse {
	message := "Solicitação negada."
	if _, err := s.auth.AuthenticateActor(context.WithoutCancel(ctx), actor); err == nil {
		message += " Estágio: " + stage + ". Referência: " + s.config.CorrelationID
	}
	return application.ConversationResponse{Status: "REJECTED", Message: message}
}
func (s *DevelopmentService) persistOutput(out application.DevelopmentOutput) error {
	if s.lease == nil || s.lease.root == nil {
		return ErrStorage
	}
	data, err := json.Marshal(out)
	if err != nil || len(data) > 64*1024 {
		return ErrStorage
	}
	f, err := s.lease.root.OpenFile("development.pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrStorage
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	if err := s.lease.root.Rename("development.pending", "development.json"); err != nil {
		return ErrStorage
	}
	dir, err := s.lease.root.Open(".")
	if err != nil {
		return ErrStorage
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *DevelopmentService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	var failures []error
	if s.home != nil {
		failures = append(failures, s.home.Close())
	}
	if s.store != nil {
		failures = append(failures, s.store.Close())
	}
	if s.lease != nil {
		failures = append(failures, s.lease.close())
	}
	return errors.Join(append(failures, ctx.Err())...)
}
