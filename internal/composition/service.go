package composition

import (
	"context"
	"crypto/rand"
	filestore "dev-orchestrator/internal/adapters/file"
	inbound "dev-orchestrator/internal/adapters/mcp"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/infrastructure/provider"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Service struct {
	config     Config
	routes     *application.ConfiguredProjectRoutes
	projects   ports.ProjectRepository
	tasks      *filestore.TaskRepository
	builder    *application.ContextBuilder
	planner    ports.Planner
	agent      *application.LocalAgentTransport
	localAgent *application.LocalAgentDispatcher
	audit      *auditStore
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	active     bool
	stopped    bool
	done       chan struct{}
	home       *provider.CodexRuntimeHome
	isolation  func(string) error
	failed     chan struct{}
	mcp        *inbound.Adapter
	mcpServer  *http.Server
	mcpAddress string
}

// NewService is the production composition: no caller-supplied Planner,
// Executor, process capability, credential source or isolation bypass.
func NewService(ctx context.Context, c Config) (*Service, error) {
	if c.Validate() != nil {
		return nil, ErrConfig
	}
	for _, p := range c.Projects {
		if e := requireReadOnly(p.Workspace); e != nil {
			return nil, e
		}
	}
	if c.DisableDiscord {
		return buildService(ctx, c, nil, requireReadOnly)
	}
	home, e := provider.NewCodexRuntimeHome()
	if e != nil {
		return nil, e
	}
	planner := infrastructure.NewCodexPlannerAdapter(provider.NewToolFreeCodexPlannerRuntime(home))
	s, e := buildService(ctx, c, planner, requireReadOnly)
	if e != nil {
		home.Close()
		return nil, e
	}
	s.home = home
	return s, nil
}

// Test injection is package-private; production always uses the constructor above.
func buildService(parent context.Context, c Config, planner ports.Planner, isolation func(string) error) (*Service, error) {
	if c.Validate() != nil || (!c.DisableDiscord && planner == nil) || isolation == nil || parent.Err() != nil {
		return nil, ErrConfig
	}
	for _, p := range c.Projects {
		if e := isolation(p.Workspace); e != nil {
			return nil, e
		}
		for kind := range p.Evidence {
			if kind == domain.ActionTypeGitStatus || kind == domain.ActionTypeGitDiff {
				if e := safeGitConfig(parent, p.Workspace); e != nil {
					return nil, e
				}
			}
		}
	}
	// Freeze caller-owned maps/slices so configuration cannot change authority.
	data, err := json.Marshal(c)
	var frozen Config
	if err != nil || json.Unmarshal(data, &frozen) != nil {
		return nil, ErrConfig
	}
	c = frozen
	audit, e := openAudit(c.StateDir)
	if e != nil {
		return nil, e
	}
	tasks, e := filestore.NewTaskRepository(c.StateDir)
	if e != nil {
		audit.close()
		return nil, e
	}
	projects := memory.NewProjectRepository()
	configured := map[domain.ProjectID]bool{}
	for _, p := range c.Projects {
		root, _ := canonicalDir(p.Workspace)
		project, _ := domain.NewProject(p.ID, p.Name, root)
		if projects.Save(parent, project) != nil {
			audit.close()
			return nil, ErrConfig
		}
		configured[p.ID] = true
	}
	// Reject persisted state for removed/unknown projects.
	if tasks.ValidateProjects(configured) != nil {
		audit.close()
		return nil, ErrStorage
	}
	for _, record := range audit.records {
		if !configured[record.ProjectID] {
			audit.close()
			return nil, ErrStorage
		}
	}
	routes, _ := application.NewConfiguredProjectRoutes(c.Routes)
	ctx, cancel := context.WithCancel(parent)
	localAgent := application.NewLocalAgent(projects, nil, application.NewReadOnlyActionAllowlist(), readOnlyCapabilities{})
	s := &Service{config: c, routes: routes, projects: projects, tasks: tasks, builder: application.NewContextBuilder(projects, tasks), planner: application.NewReadOnlyPlanner(planner), agent: application.NewLocalAgentTransport(localAgent), localAgent: localAgent, audit: audit, ctx: ctx, cancel: cancel, isolation: isolation, failed: make(chan struct{})}
	if c.MCP != nil {
		if err := s.composeMCP(isolation); err != nil {
			cancel()
			audit.close()
			return nil, err
		}
	}
	return s, nil
}
func rejected() application.ConversationResponse {
	return application.ConversationResponse{Status: "REJECTED", Message: "Não foi possível processar a solicitação."}
}
func (s *Service) Handle(parent context.Context, input application.ConversationInput) application.ConversationResponse {
	if s.config.DisableDiscord {
		return rejected()
	}
	if len(input.Text) > s.config.MaxIntentBytes || strings.TrimSpace(input.Text) == "" || !utf8.ValidString(input.Text) || parent.Err() != nil {
		return rejected()
	}
	project, e := s.routes.Resolve(parent, input.Source)
	if e != nil {
		return rejected()
	}
	s.mu.Lock()
	if s.stopped || s.ctx.Err() != nil {
		s.mu.Unlock()
		return rejected()
	}
	if s.active {
		s.mu.Unlock()
		return application.ConversationResponse{Status: "BUSY", Message: "Uma solicitação está em andamento."}
	}
	s.active = true
	s.done = make(chan struct{})
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.active = false; close(s.done); s.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(s.config.RequestTimeout))
	defer cancel()
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	var entropy [16]byte
	if _, e = rand.Read(entropy[:]); e != nil {
		return rejected()
	}
	id := hex.EncodeToString(entropy[:])
	record := auditRecord{Correlation: id, ProjectID: project, GuildID: input.Source.GuildID, ChannelID: input.Source.ChannelID, Stage: "START", Status: "PENDING", CreatedAt: time.Now().UTC()}
	if s.audit.append(record) != nil {
		s.failClosed()
		return rejected()
	}
	for _, p := range s.config.Projects {
		if p.ID == project && s.isolation(p.Workspace) != nil {
			s.failClosed()
			return rejected()
		}
	}
	round := &requestRound{service: s, record: &record}
	handler := application.NewConversationHandler(s.routes, s.projects, application.NewConversationTaskResolver(s.tasks), s.builder, round)
	result := handler.Handle(ctx, input)
	if s.tasks.Health() != nil {
		s.failClosed()
		result = rejected()
	}
	if result.Status == "ACCEPTED" {
		result = round.response
	}
	if ctx.Err() != nil {
		result = rejected()
	}
	record.Stage = "FINISH"
	record.Status = result.Status
	record.CreatedAt = time.Now().UTC()
	if s.audit.append(record) != nil {
		s.failClosed()
		return rejected()
	}
	result.Message += " Referência: " + id
	return result
}
func (s *Service) failClosed() {
	s.mu.Lock()
	if !s.stopped {
		s.stopped = true
		close(s.failed)
	}
	s.mu.Unlock()
	s.cancel()
	if s.mcp != nil {
		s.mcp.Stop()
	}
}
func (s *Service) Failed() <-chan struct{} { return s.failed }
func (s *Service) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	done := s.done
	active := s.active
	mcpServer := s.mcpServer
	s.mu.Unlock()
	s.cancel()
	if s.mcp != nil {
		s.mcp.Stop()
	}
	if mcpServer != nil {
		if err := mcpServer.Shutdown(ctx); err != nil {
			mcpServer.Close()
			return err
		}
	}
	if active {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.home != nil {
		if e := s.home.Close(); e != nil {
			return e
		}
	}
	return s.audit.close()
}

type requestRound struct {
	service  *Service
	record   *auditRecord
	response application.ConversationResponse
}

func (r *requestRound) Run(ctx context.Context, input application.OrchestrationInput) (application.OrchestrationOutput, error) {
	s := r.service
	r.record.TaskID = input.TaskID
	r.record.Stage = "RUN"
	r.record.CreatedAt = time.Now().UTC()
	if s.audit.append(*r.record) != nil {
		s.failClosed()
		return application.OrchestrationOutput{}, ErrStorage
	}
	resolver := configuredEvidence{s.config.Projects}
	transport := &boundedEvidence{base: s.agent, max: s.config.MaxEvidenceBytes, record: r.record}
	planner := &countedPlanner{base: s.planner, record: r.record}
	metadata := application.BotCommandMetadata{ProtocolVersion: "1.0", MessageID: domain.MessageID(r.record.Correlation + ":command"), CorrelationID: domain.CorrelationID(r.record.Correlation), CreatedAt: time.Now().UTC()}
	out, e := application.NewOrchestrator(s.builder, planner, resolver, transport, metadata).Run(ctx, input)
	// Only analysis transitions are persisted; no executor-related state exists.
	if _, transitionErr := application.NewWorkflowEngine(s.tasks).Transition(ctx, input.TaskID, domain.TaskStatusBlocked); transitionErr != nil {
		return application.OrchestrationOutput{}, transitionErr
	}
	r.response = application.ConversationResponse{Status: "BLOCKED", Message: "Análise encerrada; execução bloqueada no modo somente leitura."}
	if r.record.EvidenceStatus == "SUCCESS" {
		r.response.Message = "Evidência " + string(r.record.EvidenceKind) + " coletada; análise encerrada com execução bloqueada."
	}
	if e != nil {
		if errors.Is(e, application.ErrReadOnlyDenied) {
			return application.OrchestrationOutput{}, nil
		}
		return application.OrchestrationOutput{}, e
	}
	if out.Decision.Type == ports.PlannerDecisionRequestEvidence {
		r.response = application.ConversationResponse{Status: "LIMITED", Message: "Limite de análise atingido; nova evidência não foi executada."}
	}
	return out, nil
}

type countedPlanner struct {
	base   ports.Planner
	record *auditRecord
}

func (p *countedPlanner) Plan(ctx context.Context, r ports.PlannerRequest) (ports.PlannerDecision, error) {
	if p.record.PlannerCalls >= 2 {
		return ports.PlannerDecision{}, application.ErrReadOnlyDenied
	}
	p.record.PlannerCalls++
	d, e := p.base.Plan(ctx, r)
	if e == nil {
		p.record.Decision = string(d.Type)
	} else if errors.Is(e, application.ErrReadOnlyDenied) {
		p.record.Decision = "DENIED"
	}
	return d, e
}

type configuredEvidence struct{ projects []ProjectConfig }

func (r configuredEvidence) Resolve(ctx context.Context, kind domain.ActionType, canonical application.PlannerContext) (domain.ActionParams, error) {
	for _, p := range r.projects {
		if p.ID == canonical.Project.ID {
			params, ok := p.Evidence[kind]
			if !ok {
				return domain.ActionParams{}, application.ErrReadOnlyDenied
			}
			return (application.TrustedEvidenceResolver{Params: map[domain.ActionType]domain.ActionParams{kind: params}}).Resolve(ctx, kind, canonical)
		}
	}
	return domain.ActionParams{}, application.ErrReadOnlyDenied
}

type boundedEvidence struct {
	base   application.EvidenceTransport
	max    int
	record *auditRecord
}

func (b *boundedEvidence) Handle(ctx context.Context, e domain.Envelope) (domain.Envelope, error) {
	if b.record.EvidenceCalls >= 1 {
		return domain.Envelope{}, application.ErrReadOnlyDenied
	}
	command, ok := e.Payload.(application.BotCommand)
	if !ok || !readOnlyKind(command.Action.Type) {
		return domain.Envelope{}, application.ErrReadOnlyDenied
	}
	b.record.EvidenceCalls++
	b.record.EvidenceKind = command.Action.Type
	out, err := b.base.Handle(ctx, e)
	if err != nil {
		return domain.Envelope{}, err
	}
	if payload, ok := out.Payload.(application.BotResult); ok {
		b.record.EvidenceStatus = string(payload.Status)
	}
	data, err := json.Marshal(out)
	if err != nil || len(data) > b.max {
		return domain.Envelope{}, application.ErrExecutionOutputLimit
	}
	return out, nil
}
