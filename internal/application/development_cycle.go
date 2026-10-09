package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	"log"
	"sync"
	"time"
)

// DevelopmentEffects is trusted wiring for the existing P10 services. No
// capability in this interface is given to Planner or CandidateGenerator.
type DevelopmentEffects interface {
	Apply(context.Context, domain.CandidateArtifact, domain.WriteBinding, domain.ApprovalID, string) (domain.WriteTerminalResult, error)
	BranchBinding(domain.CandidateContext, string) (domain.GitBinding, error)
	Git(context.Context, string, string, domain.GitBinding) (DevelopmentGitResult, error)
	CommitBinding(context.Context, string, string, domain.GitBinding) (domain.GitBinding, error)
	Verify(context.Context, DevelopmentResult) error
}
type DevelopmentGitResult struct{ State, OID, Tree string }
type DevelopmentResult struct {
	PlannerFailureStage                                                                           string `json:",omitempty"`
	BlockPersistenceFailed                                                                        bool   `json:",omitempty"`
	ProjectID                                                                                     domain.ProjectID
	TaskID                                                                                        domain.TaskID
	CorrelationID, ApproverIdentity, CandidateIdentity, WriteTransaction, Branch, CommitOID, Tree string
	WriteResult                                                                                   domain.WriteTerminalResult
	TaskState                                                                                     domain.TaskStatus
}
type DevelopmentReview struct {
	Operation, Identity string
	Write               *domain.WriteBinding
	Git                 *domain.GitBinding
	// Actual proposed bytes are returned for human review, never interpreted.
	Files     map[string]string
	ExpiresAt time.Time
}
type DevelopmentOutput struct {
	Review *DevelopmentReview
	Result DevelopmentResult
}
type DevelopmentCycleConfig struct {
	Context                                  domain.CandidateContext
	WorkspaceIdentity, PolicyVersion, Branch string
	Policy                                   domain.CandidatePolicy
	Timeout, ApprovalTTL                     time.Duration
}

// One task and one controlled workspace per cycle; confirmations are separate
// requests. Restart never auto-resumes or reissues a pending approval.
type DevelopmentCycle struct {
	mu                sync.Mutex
	config            DevelopmentCycleConfig
	planner           *Orchestrator
	tasks             ports.TaskRepository
	auth              ports.ActorAuthenticationPort
	pipeline          *CandidatePipeline
	issuer            *ApprovalIssuer
	effects           DevelopmentEffects
	now               func() time.Time
	artifact          domain.CandidateArtifact
	pending           *DevelopmentReview
	result            DevelopmentResult
	started, terminal bool
}

func NewDevelopmentCycle(c DevelopmentCycleConfig, p *Orchestrator, t ports.TaskRepository, a ports.ActorAuthenticationPort, g *CandidatePipeline, i *ApprovalIssuer, e DevelopmentEffects, now func() time.Time) (*DevelopmentCycle, error) {
	if c.Context.Validate() != nil || c.Policy.Validate() != nil || c.PolicyVersion == "" || c.WorkspaceIdentity == "" || !domain.GitBranchName(c.Branch) || c.Timeout <= 0 || c.Timeout > time.Minute || c.ApprovalTTL <= 0 || c.ApprovalTTL > 15*time.Minute || p == nil || t == nil || a == nil || g == nil || i == nil || e == nil || now == nil {
		return nil, domain.ErrWriteDenied
	}
	c.Policy = c.Policy.Clone()
	return &DevelopmentCycle{config: c, planner: p, tasks: t, auth: a, pipeline: g, issuer: i, effects: e, now: now, result: DevelopmentResult{ProjectID: c.Context.ProjectID, TaskID: c.Context.TaskID, CorrelationID: c.Context.CorrelationID}}, nil
}
func (s *DevelopmentCycle) output() DevelopmentOutput {
	out := DevelopmentOutput{Result: s.result}
	if s.pending != nil {
		data, _ := json.Marshal(s.pending)
		var r DevelopmentReview
		json.Unmarshal(data, &r)
		out.Review = &r
	}
	return out
}

// Snapshot returns a detached receipt for trusted operational persistence.
func (s *DevelopmentCycle) Snapshot() DevelopmentOutput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.output()
}
func (s *DevelopmentCycle) setReview(r DevelopmentReview) {
	r.ExpiresAt = s.now().Add(s.config.ApprovalTTL)
	r.Identity = ""
	data, _ := json.Marshal(r)
	r.Identity = domain.CandidateDigest(data)
	s.pending = &r
}
func (s *DevelopmentCycle) transition(ctx context.Context, state domain.TaskStatus) error {
	task, found, err := s.tasks.FindByID(ctx, s.config.Context.TaskID)
	if err != nil {
		return err
	}
	if !found || task.ProjectID != s.config.Context.ProjectID {
		return domain.ErrWriteDenied
	}
	updated, err := NewWorkflowEngine(s.tasks).Transition(ctx, task.ID, state)
	if err == nil {
		s.result.TaskState = updated.Status
	}
	return err
}

var ErrBlockPersistence = errors.New("BLOCK_PERSISTENCE")

func (s *DevelopmentCycle) block(ctx context.Context) error {
	s.pending = nil
	s.terminal = true
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if s.transition(cleanup, domain.TaskStatusBlocked) != nil {
		s.result.BlockPersistenceFailed = true
		return ErrBlockPersistence
	}
	return nil
}
func (s *DevelopmentCycle) Begin(ctx context.Context, e ports.ActorEvidence, intent DevelopmentIntent) (DevelopmentOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started || s.terminal {
		return s.output(), domain.ErrWriteDenied
	}
	if _, err := s.auth.AuthenticateActor(ctx, e); err != nil {
		return s.output(), err
	}
	if intent.Objective == "" || len(intent.Objective) > 1024 {
		return s.output(), domain.ErrWriteDenied
	}
	task, found, err := s.tasks.FindByID(ctx, s.config.Context.TaskID)
	if err != nil || !found || task.ProjectID != s.config.Context.ProjectID || task.Status != domain.TaskStatusAnalyzing {
		return s.output(), domain.ErrWriteDenied
	}
	s.started = true
	s.result.TaskState = task.Status
	intent.RequestedWriteTargets = append([]string(nil), intent.RequestedWriteTargets...)
	if err := intent.ValidateWriteTargets(s.config.Policy); err != nil {
		blockErr := s.block(ctx)
		return s.output(), errors.Join(domain.ErrWriteDenied, err, blockErr)
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout)
	defer cancel()
	req := OrchestrationInput{ProjectID: task.ProjectID, TaskID: task.ID, Context: ports.PlannerContext{Project: domain.Project{ID: task.ProjectID}, CurrentTask: task}, UserIntent: intent.Objective}
	plan, err := s.planner.Run(ctx, req)
	d := plan.Decision
	if err != nil || ctx.Err() != nil || d.Validate() != nil || d.ProjectID != task.ProjectID || d.TaskID != task.ID {
		stage := "PLANNER"
		var failure *ports.PlannerFailure
		if errors.As(err, &failure) {
			stage = failure.FailureStage()
		} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			stage = "TIMEOUT"
		} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			stage = "CANCELLED"
		}
		s.result.PlannerFailureStage = stage
		blockErr := s.block(ctx)
		return s.output(), errors.Join(domain.ErrWriteDenied, ports.NewPlannerFailure(stage, err), blockErr)
	}
	if d.Type != ports.PlannerDecisionPrepareExecutor {
		s.block(ctx)
		return s.output(), domain.ErrWriteDenied
	}
	if _, err := NewTaskRefiner(s.tasks).Refine(ctx, d); err != nil {
		s.block(ctx)
		return s.output(), err
	}
	if err := s.transition(ctx, domain.TaskStatusInProgress); err != nil {
		s.block(ctx)
		return s.output(), err
	}
	s.artifact, err = s.pipeline.Generate(ctx, CandidateRequest{Context: s.config.Context, Objective: intent.Objective, RequestedWriteTargets: intent.RequestedWriteTargets, WorkspaceIdentity: s.config.WorkspaceIdentity, Policy: s.config.Policy, Timeout: s.config.Timeout})
	if err != nil {
		s.block(ctx)
		return s.output(), err
	}
	candidate := s.artifact.Candidate()
	if candidate.WorkspaceIdentity != s.config.WorkspaceIdentity {
		s.block(ctx)
		return s.output(), domain.ErrWriteDenied
	}
	s.result.CandidateIdentity, err = candidate.Identity()
	if err != nil {
		s.block(ctx)
		return s.output(), err
	}
	binding, err := candidate.Binding(s.config.PolicyVersion, s.config.Context.CorrelationID+"-write")
	if err != nil {
		s.block(ctx)
		return s.output(), err
	}
	files := map[string]string{}
	for _, entry := range candidate.Manifest.Entries {
		data := s.artifact.Blob(entry.Postimage)
		if domain.CandidateDigest(data) != entry.Postimage {
			s.block(ctx)
			return s.output(), domain.ErrWriteDenied
		}
		files[entry.Target] = string(data)
	}
	s.setReview(DevelopmentReview{Operation: string(domain.WriteApply), Write: &binding, Files: files})
	return s.output(), nil
}
func (s *DevelopmentCycle) Confirm(ctx context.Context, e ports.ActorEvidence, identity string) (DevelopmentOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || s.pending == nil || identity != s.pending.Identity {
		log.Print("CONFIRM_LOOKUP_DENIED")
		return s.output(), domain.ErrWriteDenied
	}
	if !s.now().Before(s.pending.ExpiresAt) {
		log.Print("CONFIRM_EXPIRED")
		return s.output(), domain.ErrWriteDenied
	}
	if err := ctx.Err(); err != nil {
		return s.output(), err
	}
	r := s.pending
	id := s.config.Context.CorrelationID + "-" + r.Operation
	if r.Write != nil {
		a, err := s.issuer.IssueWrite(ctx, e, domain.ApprovalID(id), *r.Write, *r.Write, r.ExpiresAt)
		if err != nil {
			log.Print("CONFIRM_APPROVAL_DENIED")
			return s.output(), err
		}
		log.Print("CONFIRM_APPROVED")
		tx := s.config.Context.CorrelationID + "-write-tx"
		result, err := s.effects.Apply(ctx, s.artifact, *r.Write, a.ApprovalID, tx)
		s.result.ApproverIdentity = a.ApproverIdentity
		s.result.WriteTransaction = tx
		s.result.WriteResult = result
		if err != nil || result.State != domain.WriteApplied {
			s.block(ctx)
			return s.output(), domain.ErrWriteDenied
		}
		b, err := s.effects.BranchBinding(s.config.Context, s.config.Branch)
		if err != nil {
			s.block(ctx)
			return s.output(), err
		}
		s.setReview(DevelopmentReview{Operation: string(domain.GitBranch), Git: &b})
		return s.output(), nil
	}
	if r.Git == nil {
		return s.output(), domain.ErrWriteDenied
	}
	a, err := s.issuer.IssueGit(ctx, e, id, *r.Git, *r.Git, r.ExpiresAt)
	if err != nil {
		log.Print("CONFIRM_APPROVAL_DENIED")
		return s.output(), err
	}
	log.Print("CONFIRM_APPROVED")
	opID := id + "-op"
	result, err := s.effects.Git(ctx, opID, a.ApprovalID, *r.Git)
	s.result.ApproverIdentity = a.ApproverIdentity
	if err != nil || result.State != "APPLIED" || !domain.GitOID(result.OID) {
		s.block(ctx)
		return s.output(), domain.ErrWriteDenied
	}
	if r.Git.OperationKind == domain.GitBranch {
		s.result.Branch = r.Git.BranchName
		b, err := s.effects.CommitBinding(ctx, s.result.WriteTransaction, opID, *r.Git)
		if err != nil {
			s.block(ctx)
			return s.output(), err
		}
		s.setReview(DevelopmentReview{Operation: string(domain.GitCommit), Git: &b})
		return s.output(), nil
	}
	if r.Git.OperationKind != domain.GitCommit || result.Tree != r.Git.ExpectedTree {
		s.block(ctx)
		return s.output(), domain.ErrWriteDenied
	}
	s.result.CommitOID = result.OID
	s.result.Tree = result.Tree
	if err := s.effects.Verify(ctx, s.result); err != nil {
		s.block(ctx)
		return s.output(), err
	}
	if err := s.transition(ctx, domain.TaskStatusDone); err != nil {
		s.block(ctx)
		return s.output(), err
	}
	s.pending = nil
	s.terminal = true
	return s.output(), nil
}
func (s *DevelopmentCycle) Cancel(ctx context.Context, e ports.ActorEvidence) (DevelopmentOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.auth.AuthenticateActor(ctx, e); err != nil {
		return s.output(), err
	}
	if s.terminal {
		return s.output(), domain.ErrWriteDenied
	}
	err := s.block(ctx)
	return s.output(), err
}
