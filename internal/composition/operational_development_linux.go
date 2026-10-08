//go:build linux

package composition

import (
	"bytes"
	"context"
	"crypto/rand"
	filestore "dev-orchestrator/internal/adapters/file"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/infrastructure/provider"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The existing Config.Projects/Routes are the sole trusted project registry.
// Its READ-specific evidence settings are not required by this opt-in runtime.
type OperationalDevelopmentConfig struct {
	Registry             Config
	Mappings             []infrastructure.ActorMapping
	Grants               []ports.Grant
	ScratchRoot          string
	MaxActive, MaxCycles int
}

func privateOperationalDir(path string) bool {
	if _, err := canonicalDir(path); err != nil || filepath.Clean(path) != path || path == "/" || contains(path, "/var/lib/dev-orchestrator/runtime-auth") || contains("/var/lib/dev-orchestrator/runtime-auth", path) {
		return false
	}
	var st unix.Stat_t
	return unix.Lstat(path, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Mode&07777 == 0700 && st.Uid == uint32(os.Geteuid())
}
func (c OperationalDevelopmentConfig) Validate() error {
	if c.Registry.MCP != nil || c.MaxActive < 1 || c.MaxActive > 64 || c.MaxCycles < c.MaxActive || c.MaxCycles > 1024 || len(c.Registry.Projects) == 0 || len(c.Registry.Projects) > 64 || len(c.Registry.Routes) == 0 || len(c.Registry.Routes) > 256 {
		return ErrConfig
	}
	if _, err := infrastructure.NewActorAuthority(c.Mappings, c.Grants); err != nil {
		return ErrConfig
	}
	for _, m := range c.Mappings {
		if m.Evidence.Provider != "discord" || !validDiscordID(m.Evidence.ExternalID) {
			return ErrConfig
		}
	}
	roots := []string{c.Registry.StateDir, c.ScratchRoot}
	projects := map[domain.ProjectID]bool{}
	for _, p := range c.Registry.Projects {
		if _, err := domain.NewProject(p.ID, p.Name, p.Workspace); err != nil || projects[p.ID] || p.Development == nil {
			return ErrConfig
		}
		d := p.Development
		if d.Policy.Validate() != nil || (domain.CandidateContext{ProjectID: p.ID, TaskID: "validation", CorrelationID: d.PolicyVersion}).Validate() != nil || !strings.HasSuffix(d.BranchPrefix, "/") || !domain.GitBranchName(d.BranchPrefix+strings.Repeat("a", 32)) {
			return ErrConfig
		}
		projects[p.ID] = true
		roots = append(roots, p.Workspace)
	}
	for i, path := range roots {
		if !privateOperationalDir(path) {
			return ErrConfig
		}
		for _, other := range roots[:i] {
			if contains(path, other) || contains(other, path) {
				return ErrConfig
			}
		}
	}
	if _, err := application.NewConfiguredProjectRoutes(c.Registry.Routes); err != nil {
		return ErrConfig
	}
	routed := map[domain.ProjectID]bool{}
	for _, r := range c.Registry.Routes {
		if !projects[r.ProjectID] || !validDiscordID(r.Source.GuildID) || !validDiscordID(r.Source.ChannelID) {
			return ErrConfig
		}
		routed[r.ProjectID] = true
	}
	for id := range projects {
		if !routed[id] {
			return ErrConfig
		}
	}
	for _, g := range c.Grants {
		if !projects[domain.ProjectID(g.ProjectID)] {
			return ErrConfig
		}
	}
	return nil
}

func LoadOperationalDevelopmentConfig(path string) (OperationalDevelopmentConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return OperationalDevelopmentConfig{}, ErrConfig
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 || uniqueJSON(data) != nil {
		return OperationalDevelopmentConfig{}, ErrConfig
	}
	var c OperationalDevelopmentConfig
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Validate() != nil {
		return OperationalDevelopmentConfig{}, ErrConfig
	}
	return c, nil
}

type operationalCycleRecord struct {
	ProjectID                                          domain.ProjectID
	TaskID                                             domain.TaskID
	CorrelationID, Branch, PrincipalID, ConfigIdentity string
	Source                                             application.ConversationSource
	RecoveryRequired                                   bool
	Output                                             application.DevelopmentOutput
}
type OperationalDevelopmentService struct {
	mu        sync.Mutex
	config    OperationalDevelopmentConfig
	routes    *application.ConfiguredProjectRoutes
	projects  map[domain.ProjectID]ProjectConfig
	authority *infrastructure.ActorAuthority
	records   map[string]operationalCycleRecord
	active    map[string]*DevelopmentService
	root      *os.Root
	lock      *os.File
	planner   ports.Planner
	generator ports.CandidateGenerator
	stopped   bool
}

func operationalProjectIdentity(p ProjectConfig) string {
	data, _ := json.Marshal(p)
	return domain.CandidateDigest(data)
}
func newOperationalDevelopmentService(ctx context.Context, c OperationalDevelopmentConfig, p ports.Planner, g ports.CandidateGenerator) (*OperationalDevelopmentService, error) {
	if c.Validate() != nil || ctx.Err() != nil || (p == nil) != (g == nil) {
		return nil, ErrConfig
	}
	// Freeze caller-owned policies, routes and authority before accepting input.
	data, _ := json.Marshal(c)
	if json.Unmarshal(data, &c) != nil {
		return nil, ErrConfig
	}
	root, err := os.OpenRoot(c.Registry.StateDir)
	if err != nil {
		return nil, ErrStorage
	}
	fd, err := unix.Open(filepath.Join(c.Registry.StateDir, "operational.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		root.Close()
		return nil, ErrStorage
	}
	lock := os.NewFile(uintptr(fd), "operational.lock")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		lock.Close()
		root.Close()
		return nil, ErrStorage
	}
	routes, _ := application.NewConfiguredProjectRoutes(c.Registry.Routes)
	authority, _ := infrastructure.NewActorAuthority(c.Mappings, c.Grants)
	s := &OperationalDevelopmentService{config: c, root: root, lock: lock, routes: routes, authority: authority, projects: map[domain.ProjectID]ProjectConfig{}, records: map[string]operationalCycleRecord{}, active: map[string]*DevelopmentService{}, planner: p, generator: g}
	for _, project := range c.Registry.Projects {
		s.projects[project.ID] = project
	}
	if err = s.restore(ctx); err != nil {
		s.Shutdown(context.Background())
		return nil, err
	}
	return s, nil
}

func NewOperationalDevelopmentService(ctx context.Context, c OperationalDevelopmentConfig) (*OperationalDevelopmentService, error) {
	return newOperationalDevelopmentService(ctx, c, nil, nil)
}

const maxOperationalBytes = 4 * 1024 * 1024

func (s *OperationalDevelopmentService) save() error {
	data, err := json.Marshal(s.records)
	if err != nil || len(data) > maxOperationalBytes {
		return ErrStorage
	}
	f, err := s.root.OpenFile("operational.pending", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
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
	if s.root.Rename("operational.pending", "operational.json") != nil {
		return ErrStorage
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return ErrStorage
	}
	defer dir.Close()
	return dir.Sync()
}
func (s *OperationalDevelopmentService) restore(ctx context.Context) error {
	pending := false
	if info, err := s.root.Lstat("operational.pending"); !errors.Is(err, os.ErrNotExist) {
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxOperationalBytes {
			return ErrStorage
		}
		pending = true
	}
	info, err := s.root.Lstat("operational.json")
	if errors.Is(err, os.ErrNotExist) {
		// No cycle can have reached bootstrap before its first committed intent.
		if pending {
			if err := s.quarantinePending(); err != nil {
				return err
			}
			return s.save()
		}
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxOperationalBytes {
		return ErrStorage
	}
	f, err := s.root.Open("operational.json")
	if err != nil {
		return ErrStorage
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOperationalBytes+1))
	f.Close()
	if err != nil || len(data) > maxOperationalBytes || uniqueJSON(data) != nil {
		return ErrStorage
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&s.records) != nil || s.records == nil || len(s.records) > s.config.MaxCycles {
		return ErrStorage
	}
	tasks := map[domain.TaskID]bool{}
	for key, r := range s.records {
		project, found := s.projects[r.ProjectID]
		route, routeErr := s.routes.Resolve(ctx, r.Source)
		raw, hexErr := hex.DecodeString(key)
		if !found || routeErr != nil || route != r.ProjectID || hexErr != nil || len(raw) != 16 || hex.EncodeToString(raw) != key || key != r.CorrelationID || r.TaskID != domain.TaskID("task-"+key) || tasks[r.TaskID] || r.PrincipalID == "" || r.ConfigIdentity != operationalProjectIdentity(project) || r.Branch != project.Development.BranchPrefix+key || r.Output.Result.ProjectID != r.ProjectID || r.Output.Result.TaskID != r.TaskID || r.Output.Result.CorrelationID != key {
			return ErrStorage
		}
		tasks[r.TaskID] = true
		if r.RecoveryRequired || (r.Output.Result.TaskState != domain.TaskStatusDone && r.Output.Result.TaskState != domain.TaskStatusBlocked) {
			r.RecoveryRequired = true
			r.Output.Review = nil
			r.Output.Result.TaskState = domain.TaskStatusBlocked
			r.Output.Result.PlannerFailureStage = "RECOVERY_REQUIRED"
			// Reuse task persistence, but never reopen a workspace, reissue approval,
			// or call an effect during recovery. Missing bootstrap state is explicit.
			state := filepath.Join(s.config.Registry.StateDir, key)
			if info, err := os.Lstat(state); err == nil {
				if !info.IsDir() || !privateOperationalDir(state) {
					return ErrStorage
				}
				repo, err := filestore.NewDevelopmentTaskRepository(state)
				if err != nil {
					return ErrStorage
				}
				task, found, err := repo.FindByID(ctx, r.TaskID)
				if err != nil {
					return ErrStorage
				}
				if found {
					if task.ProjectID != r.ProjectID {
						return ErrStorage
					}
					if task.CanTransitionTo(domain.TaskStatusBlocked) {
						if _, err = application.NewWorkflowEngine(repo).Transition(ctx, r.TaskID, domain.TaskStatusBlocked); err != nil {
							return ErrStorage
						}
					}
					// Pre-analysis tasks have no domain transition to BLOCKED.
					// Preserve their state; the operational receipt prevents resume
					// and explicitly requires reconciliation without inventing one.
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return ErrStorage
			}
		}
		if r.Output.Review != nil || (r.Output.Result.TaskState != domain.TaskStatusDone && r.Output.Result.TaskState != domain.TaskStatusBlocked) {
			return ErrStorage
		}
		s.records[key] = r
	}
	if pending {
		if err := s.quarantinePending(); err != nil {
			return err
		}
	}
	return s.save()
}

// Retain interrupted bytes as evidence, never adopt them as state/authority.
// Committed records are authoritative; all nonterminal cycles are blocked.
func (s *OperationalDevelopmentService) quarantinePending() error {
	id, err := trustedOperationalID()
	if err != nil {
		return ErrStorage
	}
	name := "operational.recovery-" + id
	if _, err := s.root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return ErrStorage
	}
	return s.root.Rename("operational.pending", name)
}

func trustedOperationalID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func (s *OperationalDevelopmentService) factory(ctx context.Context, r operationalCycleRecord) (*DevelopmentService, error) {
	project := s.projects[r.ProjectID]
	c := DevelopmentConfig{ProjectID: r.ProjectID, TaskID: r.TaskID, CorrelationID: r.CorrelationID, Branch: r.Branch, Policy: project.Development.Policy.Clone(), PolicyVersion: project.Development.PolicyVersion, Route: r.Source, Mappings: s.config.Mappings, Grants: s.config.Grants, ControlRoot: filepath.Join(project.Workspace, r.CorrelationID), ScratchRoot: filepath.Join(s.config.ScratchRoot, r.CorrelationID), StateRoot: filepath.Join(s.config.Registry.StateDir, r.CorrelationID)}
	for _, dir := range []string{c.ControlRoot, c.ScratchRoot, c.StateRoot} {
		if err := os.Mkdir(dir, 0700); err != nil {
			return nil, err
		}
	}
	store, err := infrastructure.NewManagedWorkspaceStore(c.ControlRoot, r.CorrelationID)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			store.Close()
		}
	}()
	w, err := store.Provision(ctx, r.ProjectID, nil)
	if err != nil {
		return nil, err
	}
	repo, err := store.ProvisionRepository(ctx, w, c.Policy, c.PolicyVersion)
	if err != nil {
		return nil, err
	}
	p, g := s.planner, s.generator
	var home *provider.CodexRuntimeHome
	if p == nil {
		home, err = provider.NewCodexRuntimeHome()
		if err != nil {
			return nil, err
		}
		defer func() {
			if !success {
				home.Close()
			}
		}()
		p = infrastructure.NewCodexPlannerAdapter(provider.NewToolFreeCodexPlannerRuntime(home))
		g = provider.NewToolFreeCandidateGenerator(home)
	}
	cycle, err := composeDevelopment(ctx, c, p, g, s.authority, store, w, repo, c.ScratchRoot, c.StateRoot)
	if err != nil {
		return nil, err
	}
	cycle.home = home
	cycle.store = store
	success = true
	return cycle, nil
}

func (s *OperationalDevelopmentService) Handle(ctx context.Context, in application.ConversationInput) application.ConversationResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	denied := application.ConversationResponse{Status: "REJECTED", Message: "Solicitação negada."}
	if s.stopped || ctx.Err() != nil {
		return denied
	}
	project, err := s.routes.Resolve(ctx, in.Source)
	if err != nil {
		return denied
	}
	principal, err := s.authority.AuthenticateActor(ctx, in.Actor)
	if err != nil {
		return denied
	}
	if in.DevelopmentAction == "status" {
		r, found := s.records[in.Confirmation]
		if !found || r.ProjectID != project || r.Source != in.Source || r.PrincipalID != principal.ID {
			return denied
		}
		data, _ := json.Marshal(r.Output.Result)
		return application.ConversationResponse{Status: string(r.Output.Result.TaskState), Message: string(data)}
	}
	var key string
	for id := range s.active {
		r := s.records[id]
		if r.ProjectID == project && r.Source == in.Source && r.PrincipalID == principal.ID {
			key = id
			break
		}
	}
	if in.DevelopmentAction == "begin" {
		if key != "" || len(s.active) >= s.config.MaxActive || len(s.records) >= s.config.MaxCycles {
			return application.ConversationResponse{Status: "BUSY", Message: "Limite de ciclos atingido."}
		}
		if strings.TrimSpace(in.Text) == "" || len(in.Text) > 1024 {
			return denied
		}
		key, err = trustedOperationalID()
		if err != nil {
			return denied
		}
		if _, exists := s.records[key]; exists {
			return denied
		}
		p := s.projects[project]
		r := operationalCycleRecord{ProjectID: project, TaskID: domain.TaskID("task-" + key), CorrelationID: key, Branch: p.Development.BranchPrefix + key, PrincipalID: principal.ID, ConfigIdentity: operationalProjectIdentity(p), Source: in.Source, RecoveryRequired: true, Output: application.DevelopmentOutput{Result: application.DevelopmentResult{ProjectID: project, TaskID: domain.TaskID("task-" + key), CorrelationID: key}}}
		s.records[key] = r
		if s.save() != nil {
			s.stopped = true
			return application.ConversationResponse{Status: "BLOCKED", Message: "Persistência bloqueada."}
		}
		cycle, err := s.factory(ctx, r)
		if err != nil {
			r.Output.Result.TaskState = domain.TaskStatusBlocked
			r.Output.Result.PlannerFailureStage = "BOOTSTRAP"
			r.RecoveryRequired = false
			s.records[key] = r
			if s.save() != nil {
				s.stopped = true
			}
			return application.ConversationResponse{Status: "BLOCKED", Message: "Bootstrap bloqueado. Referência: " + key}
		}
		s.active[key] = cycle
	} else if in.DevelopmentAction != "confirm" && in.DevelopmentAction != "cancel" {
		return denied
	} else if key == "" {
		return denied
	}
	r := s.records[key]
	if in.DevelopmentAction == "confirm" && (r.Output.Review == nil || in.Confirmation != r.Output.Review.Identity) {
		return denied
	}
	// Durable intent BEFORE invoking any cycle operation. A crash never resumes.
	r.RecoveryRequired = true
	s.records[key] = r
	if s.save() != nil {
		s.stopped = true
		return application.ConversationResponse{Status: "BLOCKED", Message: "Persistência bloqueada."}
	}
	cycle := s.active[key]
	response := cycle.Handle(ctx, in)
	r.Output = cycle.Snapshot()
	r.RecoveryRequired = false
	s.records[key] = r
	if s.save() != nil {
		s.stopped = true
		return application.ConversationResponse{Status: "BLOCKED", Message: "Persistência bloqueada. Referência: " + key}
	}
	if r.Output.Review == nil && (r.Output.Result.TaskState == domain.TaskStatusDone || r.Output.Result.TaskState == domain.TaskStatusBlocked) {
		if cycle.Shutdown(context.WithoutCancel(ctx)) != nil {
			s.stopped = true
		}
		delete(s.active, key)
	}
	response.Message = fmt.Sprintf("%s\nCiclo: %s", response.Message, key)
	return response
}
func (s *OperationalDevelopmentService) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil {
		return nil
	}
	s.stopped = true
	var failures []error
	for _, cycle := range s.active {
		failures = append(failures, cycle.Shutdown(ctx))
	}
	s.active = map[string]*DevelopmentService{}
	failures = append(failures, s.root.Close(), s.lock.Close())
	s.root = nil
	return errors.Join(failures...)
}
