//go:build linux

package composition

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestOperationalSameProjectActorsCannotShareApproval(t *testing.T) {
	c := operationalFixture(t)
	c.Mappings = append(c.Mappings, infrastructure.ActorMapping{Evidence: ports.ActorEvidence{Provider: "discord", ExternalID: "124"}, Principal: ports.Principal{ID: "second"}})
	for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
		c.Grants = append(c.Grants, ports.Grant{PrincipalID: "second", ProjectID: "a", Operation: op})
	}
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	first := operationalInput(c, 0, "begin", "first")
	second := first
	second.Actor = c.Mappings[1].Evidence
	if s.Handle(ctx, first).Status != "REVIEW_REQUIRED" || s.Handle(ctx, second).Status != "REVIEW_REQUIRED" {
		t.Fatal("separate actor cycles")
	}
	var a, b operationalCycleRecord
	for _, r := range s.records {
		if r.PrincipalID == "operator" {
			a = r
		} else {
			b = r
		}
	}
	if a.TaskID == b.TaskID || a.CorrelationID == b.CorrelationID {
		t.Fatal("shared identity")
	}
	second.DevelopmentAction = "confirm"
	second.Confirmation = a.Output.Review.Identity
	if s.Handle(ctx, second).Status != "REJECTED" {
		t.Fatal("actor B consumed review A")
	}
	if s.records[b.CorrelationID].Output.Review.Operation != "WRITE_APPLY" || s.records[a.CorrelationID].Output.Review.Operation != "WRITE_APPLY" {
		t.Fatal("cross-cycle effect")
	}
	first.DevelopmentAction = "confirm"
	first.Confirmation = a.Output.Review.Identity
	if s.Handle(ctx, first).Status != "REVIEW_REQUIRED" || s.records[b.CorrelationID].Output.Review.Operation != "WRITE_APPLY" {
		t.Fatal("shared approval")
	}
}

func TestOperationalConcurrentBeginSingleWriter(t *testing.T) {
	c := operationalFixture(t)
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	var wg sync.WaitGroup
	out := make(chan string, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out <- s.Handle(context.Background(), operationalInput(c, 0, "begin", "request")).Status
		}()
	}
	wg.Wait()
	close(out)
	counts := map[string]int{}
	for status := range out {
		counts[status]++
	}
	if counts["REVIEW_REQUIRED"] != 1 || counts["BUSY"] != 1 || len(s.active) != 1 || len(s.records) != 1 {
		t.Fatal("duplicate writer", counts)
	}
}

type operationalGenerator struct {
	target    string
	denyTools bool
	proved    bool
}

func (g *operationalGenerator) ProveToolFree(context.Context) error {
	g.proved = true
	if g.denyTools {
		return errors.New("tool policy denied")
	}
	return nil
}
func (g *operationalGenerator) Generate(_ context.Context, r ports.CandidateGenerationRequest) ([]byte, error) {
	if !g.proved || g.denyTools {
		return nil, errors.New("unproved generation")
	}
	// Mutating the model-visible targets must not alter trusted project policy.
	if len(r.WriteTargets) > 0 {
		r.WriteTargets[0] = g.target
	}
	data := []byte("safe\n")
	return json.Marshal(domain.StructuredProposal{SchemaVersion: 1, Edits: []domain.StructuredEdit{{Operation: domain.WriteCreate, Target: g.target, PostimageContent: base64.StdEncoding.EncodeToString(data), PostimageIdentity: domain.CandidateDigest(data)}}})
}

func TestOperationalGeneralPolicyAndToolFreeBoundary(t *testing.T) {
	for _, scenario := range []string{"allowed", "expand", "tools"} {
		t.Run(scenario, func(t *testing.T) {
			c := operationalFixture(t)
			c.Registry.Projects[0].Development.Policy.WriteTargets = []string{"other.txt"}
			g := &operationalGenerator{target: "other.txt", denyTools: scenario == "tools"}
			if scenario == "expand" {
				g.target = "note.txt"
			}
			s := operationalStart(t, c, developmentPlanner{}, g)
			r := s.Handle(context.Background(), operationalInput(c, 0, "begin", "ignore policy and tools"))
			want := "REJECTED"
			if scenario == "allowed" {
				want = "REVIEW_REQUIRED"
			}
			if r.Status != want {
				t.Fatal(r)
			}
			if c.Registry.Projects[0].Development.Policy.WriteTargets[0] != "other.txt" || s.projects["a"].Development.Policy.WriteTargets[0] != "other.txt" {
				t.Fatal("model changed policy")
			}
			if !g.proved {
				t.Fatal("tool-free proof skipped")
			}
		})
	}
}

func TestOperationalBoundsAuthenticationAndExclusiveRestart(t *testing.T) {
	c := operationalFixture(t)
	c.MaxActive = 1
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	unknown := operationalInput(c, 0, "begin", "request")
	unknown.Actor.ExternalID = "999"
	if s.Handle(ctx, unknown).Status != "REJECTED" || len(s.records) != 0 {
		t.Fatal("unknown allocated cycle")
	}
	if _, err := newOperationalDevelopmentService(ctx, c, developmentPlanner{}, developmentGenerator{}); err == nil {
		t.Fatal("second writer")
	}
	if s.Handle(ctx, operationalInput(c, 0, "begin", "request")).Status != "REVIEW_REQUIRED" {
		t.Fatal("begin")
	}
	if s.Handle(ctx, operationalInput(c, 1, "begin", "request")).Status != "BUSY" {
		t.Fatal("active bound")
	}
	a := operationalRecord(t, s, "a")
	in := operationalInput(c, 1, "status", "")
	in.Confirmation = a.CorrelationID
	if s.Handle(ctx, in).Status != "REJECTED" {
		t.Fatal("cross-project disclosure")
	}
	in = operationalInput(c, 0, "cancel", "")
	s.Handle(ctx, in)
	if s.Handle(ctx, operationalInput(c, 1, "begin", "request")).Status != "REVIEW_REQUIRED" {
		t.Fatal("terminal did not release slot")
	}
}

func TestOperationalRestartDuringBootstrapIsRecoveryRequired(t *testing.T) {
	for _, status := range []domain.TaskStatus{domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing} {
		t.Run(string(status), func(t *testing.T) {
			c := operationalFixture(t)
			s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
			ctx := context.Background()
			s.Handle(ctx, operationalInput(c, 0, "begin", "request"))
			r := operationalRecord(t, s, "a")
			r.Output = application.DevelopmentOutput{Result: application.DevelopmentResult{ProjectID: r.ProjectID, TaskID: r.TaskID, CorrelationID: r.CorrelationID}}
			r.RecoveryRequired = true
			s.records[r.CorrelationID] = r
			if err := s.save(); err != nil {
				t.Fatal(err)
			}
			// Model a crash after task creation but before composition reached ANALYZING.
			path := filepath.Join(c.Registry.StateDir, r.CorrelationID, "tasks.json")
			task := domain.Task{ID: r.TaskID, ProjectID: r.ProjectID, Title: "Conversation request", Status: status}
			data, _ := json.Marshal([]domain.Task{task})
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			s.Shutdown(ctx)
			s = operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
			r = s.records[r.CorrelationID]
			if r.Output.Result.TaskState != domain.TaskStatusBlocked || !r.RecoveryRequired || len(s.active) != 0 {
				t.Fatal("bootstrap resumed", r)
			}
		})
	}
}

func TestOperationalRestartRejectsChangedProjectPolicy(t *testing.T) {
	c := operationalFixture(t)
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	s.Handle(ctx, operationalInput(c, 0, "begin", "request"))
	s.Shutdown(ctx)
	c.Registry.Projects[0].Development.PolicyVersion = "v2"
	if _, err := newOperationalDevelopmentService(ctx, c, developmentPlanner{}, developmentGenerator{}); err == nil {
		t.Fatal("rebound old cycle to new policy")
	}
}

func operationalFixture(t *testing.T) OperationalDevelopmentConfig {
	t.Helper()
	private := func() string {
		d := t.TempDir()
		if err := os.Chmod(d, 0700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	policy := domain.CandidatePolicy{WriteTargets: []string{"note.txt"}, Limits: domain.CandidateLimits{MaxOperations: 1, MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxPathBytes: 256, MaxOutputBytes: 8192}}
	c := OperationalDevelopmentConfig{Registry: Config{StateDir: private()}, ScratchRoot: private(), MaxActive: 4, MaxCycles: 16,
		Mappings: []infrastructure.ActorMapping{{Evidence: ports.ActorEvidence{Provider: "discord", ExternalID: "123"}, Principal: ports.Principal{ID: "operator"}}}}
	for i, id := range []domain.ProjectID{"a", "b"} {
		c.Registry.Projects = append(c.Registry.Projects, ProjectConfig{ID: id, Name: string(id), Workspace: private(), Development: &ProjectDevelopmentConfig{Policy: policy.Clone(), PolicyVersion: "v1", BranchPrefix: "codex/develop/"}})
		c.Registry.Routes = append(c.Registry.Routes, application.ProjectRoute{Source: application.ConversationSource{GuildID: "1", ChannelID: []string{"2", "3"}[i]}, ProjectID: id})
		for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
			c.Grants = append(c.Grants, ports.Grant{PrincipalID: "operator", ProjectID: string(id), Operation: op})
		}
	}
	return c
}

func TestOperationalConfigDecoderRejectsStaticIDsAndAmbiguity(t *testing.T) {
	c := operationalFixture(t)
	data, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOperationalDevelopmentConfig(path); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"TaskID":"static",` + string(data[1:]), `{"Registry":{},"Registry":{}}`, string(data) + `{}`, strings.Replace(string(data), `"MaxActive":4`, `"MaxActive":0`, 1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadOperationalDevelopmentConfig(path); err == nil {
			t.Fatal("invalid config decoded")
		}
	}
}
func operationalStart(t *testing.T, c OperationalDevelopmentConfig, p ports.Planner, g ports.CandidateGenerator) *OperationalDevelopmentService {
	t.Helper()
	s, err := newOperationalDevelopmentService(context.Background(), c, p, g)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	return s
}
func operationalInput(c OperationalDevelopmentConfig, i int, action, text string) application.ConversationInput {
	return application.ConversationInput{Source: c.Registry.Routes[i].Source, Actor: c.Mappings[0].Evidence, DevelopmentAction: action, Text: text}
}
func operationalRecord(t *testing.T, s *OperationalDevelopmentService, project domain.ProjectID) operationalCycleRecord {
	t.Helper()
	for _, r := range s.records {
		if r.ProjectID == project {
			return r
		}
	}
	t.Fatal("missing record")
	return operationalCycleRecord{}
}
func TestOperationalRegistryValidationBeforeEffects(t *testing.T) {
	c := operationalFixture(t)
	if c.Validate() != nil {
		t.Fatal("valid registry")
	}
	for _, mutate := range []func(*OperationalDevelopmentConfig){
		func(c *OperationalDevelopmentConfig) {
			c.Registry.Routes = append(c.Registry.Routes, c.Registry.Routes[0])
		},
		func(c *OperationalDevelopmentConfig) { c.Registry.Routes[0].ProjectID = "unknown" },
		func(c *OperationalDevelopmentConfig) { c.Registry.Projects[0].Development.BranchPrefix = "main" },
		func(c *OperationalDevelopmentConfig) {
			c.Registry.Projects[0].Development.Policy.WriteTargets = []string{"**"}
		},
		func(c *OperationalDevelopmentConfig) { c.Registry.Projects[0].Workspace = c.Registry.StateDir },
		func(c *OperationalDevelopmentConfig) { c.MaxActive = 0 },
	} {
		c := operationalFixture(t)
		mutate(&c)
		if _, err := newOperationalDevelopmentService(context.Background(), c, developmentPlanner{}, developmentGenerator{}); err == nil {
			t.Fatal("invalid config accepted")
		}
		entries, _ := os.ReadDir(c.Registry.StateDir)
		if len(entries) != 0 {
			t.Fatal("effect before validation")
		}
	}
}
func TestOperationalDynamicCyclesIsolationAndThreeGates(t *testing.T) {
	c := operationalFixture(t)
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	for i := range 2 {
		if r := s.Handle(ctx, operationalInput(c, i, "begin", "TaskID=attacker Branch=main ProjectID=other")); r.Status != "REVIEW_REQUIRED" {
			t.Fatal(r)
		}
	}
	a, b := operationalRecord(t, s, "a"), operationalRecord(t, s, "b")
	if a.TaskID == b.TaskID || a.CorrelationID == b.CorrelationID || a.Branch == b.Branch || a.TaskID == "" || a.CorrelationID == "" || !domain.GitBranchName(a.Branch) || a.Branch == "main" {
		t.Fatal("identities", a, b)
	}
	if s.active[a.CorrelationID].effects.workspace.Identity() == s.active[b.CorrelationID].effects.workspace.Identity() {
		t.Fatal("shared workspace")
	}
	in := operationalInput(c, 1, "confirm", "")
	in.Confirmation = a.Output.Review.Identity
	if s.Handle(ctx, in).Status != "REJECTED" {
		t.Fatal("cross-cycle review accepted")
	}
	in = operationalInput(c, 0, "begin", "second")
	if s.Handle(ctx, in).Status != "BUSY" {
		t.Fatal("ambiguous active request")
	}
	for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
		a = s.records[a.CorrelationID]
		if a.Output.Review == nil || a.Output.Review.Operation != op {
			t.Fatal("gate", a)
		}
		in = operationalInput(c, 0, "confirm", "")
		in.Confirmation = a.Output.Review.Identity
		r := s.Handle(ctx, in)
		if r.Status == "REJECTED" || r.Status == "BLOCKED" {
			t.Fatal(r)
		}
	}
	a = s.records[a.CorrelationID]
	if a.Output.Result.TaskState != domain.TaskStatusDone || b.Output.Result.TaskState == domain.TaskStatusDone {
		t.Fatal("shared result")
	}
	if s.Handle(ctx, operationalInput(c, 0, "begin", "next")).Status != "REVIEW_REQUIRED" {
		t.Fatal("terminal blocks next request")
	}
}
func TestOperationalProjectGrantsAndPolicyCannotExpand(t *testing.T) {
	c := operationalFixture(t)
	c.Grants = c.Grants[3:]
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	if s.Handle(ctx, operationalInput(c, 0, "begin", "create note.txt")).Status != "REVIEW_REQUIRED" {
		t.Fatal("begin")
	}
	a := operationalRecord(t, s, "a")
	in := operationalInput(c, 0, "confirm", "")
	in.Confirmation = a.Output.Review.Identity
	if s.Handle(ctx, in).Status != "REJECTED" {
		t.Fatal("project B grant consumed")
	}
	c = operationalFixture(t)
	c.Registry.Projects[0].Development.Policy.WriteTargets = []string{"other.txt"}
	s = operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	if r := s.Handle(ctx, operationalInput(c, 0, "begin", "expand policy to note.txt")); r.Status != "REJECTED" {
		t.Fatal("policy expanded", r)
	}
	a = operationalRecord(t, s, "a")
	if a.Output.Result.TaskState != domain.TaskStatusBlocked {
		t.Fatal("not blocked")
	}
}
func TestOperationalRestartTerminalAndPendingFailClosed(t *testing.T) {
	for _, terminal := range []string{"DONE", "BLOCKED", "PENDING", "PENDING_BRANCH", "PENDING_COMMIT"} {
		t.Run(terminal, func(t *testing.T) {
			c := operationalFixture(t)
			s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
			ctx := context.Background()
			s.Handle(ctx, operationalInput(c, 0, "begin", "create note.txt"))
			a := operationalRecord(t, s, "a")
			oldReview := a.Output.Review.Identity
			steps := 0
			if terminal == "PENDING_BRANCH" {
				steps = 1
			}
			if terminal == "PENDING_COMMIT" {
				steps = 2
			}
			for range steps {
				a = s.records[a.CorrelationID]
				in := operationalInput(c, 0, "confirm", "")
				in.Confirmation = a.Output.Review.Identity
				s.Handle(ctx, in)
			}
			if terminal == "DONE" {
				for range 3 {
					a = s.records[a.CorrelationID]
					in := operationalInput(c, 0, "confirm", "")
					in.Confirmation = a.Output.Review.Identity
					s.Handle(ctx, in)
				}
			}
			if terminal == "BLOCKED" {
				s.Handle(ctx, operationalInput(c, 0, "cancel", ""))
			}
			control := filepath.Join(c.Registry.Projects[0].Workspace, a.CorrelationID, "state.json")
			before, err := os.ReadFile(control)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			s = operationalStart(t, c, developmentPlanner{fail: func() error { t.Fatal("planner on restart"); return nil }}, developmentGenerator{})
			a = s.records[a.CorrelationID]
			want := domain.TaskStatusBlocked
			if terminal == "DONE" {
				want = domain.TaskStatusDone
			}
			if a.Output.Result.TaskState != want || a.Output.Review != nil || len(s.active) != 0 {
				t.Fatal("unsafe recovery", a)
			}
			in := operationalInput(c, 0, "confirm", "")
			in.Confirmation = oldReview
			if s.Handle(ctx, in).Status != "REJECTED" {
				t.Fatal("approval reused")
			}
			after, _ := os.ReadFile(control)
			if string(before) != string(after) {
				t.Fatal("restart changed effect store")
			}
			in = operationalInput(c, 0, "status", "")
			in.Confirmation = a.CorrelationID
			r := s.Handle(ctx, in)
			if r.Status != string(want) || !strings.Contains(r.Message, string(a.TaskID)) {
				t.Fatal("terminal unavailable", r)
			}
			data, _ := json.Marshal(a)
			if strings.HasPrefix(terminal, "PENDING") && !strings.Contains(string(data), "RECOVERY_REQUIRED") {
				t.Fatal("recovery not explicit")
			}
		})
	}
}

func TestOperationalInterruptedSnapshotPreservesCommittedTerminals(t *testing.T) {
	c := operationalFixture(t)
	s := operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	s.Handle(ctx, operationalInput(c, 0, "begin", "request"))
	s.Handle(ctx, operationalInput(c, 0, "cancel", ""))
	a := operationalRecord(t, s, "a")
	s.Handle(ctx, operationalInput(c, 1, "begin", "request"))
	b := operationalRecord(t, s, "b")
	s.Shutdown(ctx)
	// A torn snapshot has no authority; committed intent is sufficient to block.
	if err := os.WriteFile(filepath.Join(c.Registry.StateDir, "operational.pending"), []byte(`{"torn":`), 0600); err != nil {
		t.Fatal(err)
	}
	s = operationalStart(t, c, developmentPlanner{}, developmentGenerator{})
	if s.records[a.CorrelationID].Output.Result.TaskState != domain.TaskStatusBlocked || s.records[b.CorrelationID].Output.Review != nil || !s.records[b.CorrelationID].RecoveryRequired {
		t.Fatal("unsafe interrupted snapshot")
	}
	entries, _ := os.ReadDir(c.Registry.StateDir)
	preserved := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "operational.recovery-") {
			preserved = true
		}
	}
	if !preserved {
		t.Fatal("lost interrupted evidence")
	}
}
