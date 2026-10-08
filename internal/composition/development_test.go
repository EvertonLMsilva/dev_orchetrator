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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type developmentPlanner struct {
	block bool
	fail  func() error
}

func (p developmentPlanner) Plan(_ context.Context, q ports.PlannerRequest) (ports.PlannerDecision, error) {
	if p.fail != nil {
		return ports.PlannerDecision{}, p.fail()
	}
	kind := ports.PlannerDecisionPrepareExecutor
	if p.block {
		kind = ports.PlannerDecisionBlock
	}
	return ports.PlannerDecision{ProjectID: q.ProjectID, TaskID: q.TaskID, Type: kind, Reason: "controlled pilot"}, nil
}

func TestDevelopmentPlannerFailureAndBlockPersistence(t *testing.T) {
	for _, brokenStore := range []bool{false, true} {
		t.Run(fmt.Sprint(brokenStore), func(t *testing.T) {
			var service *DevelopmentService
			p := developmentPlanner{fail: func() error {
				if brokenStore {
					if err := os.Rename(filepath.Join(service.config.StateRoot, "tasks.json"), filepath.Join(service.config.StateRoot, "tasks.saved")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(service.config.StateRoot, "tasks.json"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				return errors.New("SECRET_PLANNER")
			}}
			service, _, _, file, e := developmentFixture(t, []string{"WRITE_APPLY"}, p, developmentGenerator{})
			out, err := service.Begin(context.Background(), e, application.DevelopmentIntent{Objective: "create note.txt", RequestedWriteTargets: []string{"note.txt"}})
			if err == nil || out.Review != nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("unsafe failure", err)
			}
			data, _ := json.Marshal(out.Result)
			if !strings.Contains(string(data), "PLANNER") {
				t.Fatal("planner stage lost", string(data))
			}
			if brokenStore {
				if !strings.Contains(err.Error(), "BLOCK_PERSISTENCE") {
					t.Fatal("block persistence lost", err)
				}
			} else {
				if out.Result.TaskState != domain.TaskStatusBlocked {
					t.Fatal("not blocked", out)
				}
				stored, _ := os.ReadFile(filepath.Join(service.config.StateRoot, "tasks.json"))
				if !strings.Contains(string(stored), "BLOCKED") {
					t.Fatal("block not persisted")
				}
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("effect after planner failure")
			}
			if _, err := service.Begin(context.Background(), e, application.DevelopmentIntent{Objective: "retry", RequestedWriteTargets: []string{"note.txt"}}); err == nil {
				t.Fatal("automatic retry")
			}
		})
	}
}

func TestDevelopmentPrePlannerDenialEvidence(t *testing.T) {
	service, _, _, _, e := developmentFixture(t, nil, developmentPlanner{fail: func() error { t.Fatal("pre-gate reached planner"); return nil }}, developmentGenerator{})
	for _, stage := range []string{"REQUEST_ROUTE", "REQUEST_ACTION", "SERVICE_STOPPED"} {
		in := application.ConversationInput{Source: service.config.Route, Actor: e, DevelopmentAction: "begin", RequestedWriteTargets: []string{"note.txt"}, Text: "create note.txt"}
		if stage == "REQUEST_ROUTE" {
			in.Source.ChannelID = "other"
		} else if stage == "REQUEST_ACTION" {
			in.DevelopmentAction = "invalid"
		} else {
			service.stopped = true
		}
		response := service.Handle(context.Background(), in)
		if response.Status != "REJECTED" || !strings.Contains(response.Message, stage) || !strings.Contains(response.Message, "corr") {
			t.Fatal("pre-gate diagnostic lost", response)
		}
		stored, _ := os.ReadFile(filepath.Join(service.config.StateRoot, "tasks.json"))
		if !strings.Contains(string(stored), "ANALYZING") {
			t.Fatal("pre-gate mutated task")
		}
		in.Actor.ExternalID = "unknown"
		if strings.Contains(service.Handle(context.Background(), in).Message, stage) {
			t.Fatal("unknown actor disclosure")
		}
	}
}

func TestDevelopmentPlannerFailureChannelEvidence(t *testing.T) {
	service, _, _, _, e := developmentFixture(t, nil, developmentPlanner{fail: func() error { return ports.NewPlannerFailure("HOST_START", errors.New("SECRET_DOCKER")) }}, developmentGenerator{})
	in := application.ConversationInput{Source: service.config.Route, Actor: e, DevelopmentAction: "begin", RequestedWriteTargets: []string{"note.txt"}, Text: "create note.txt"}
	response := service.Handle(context.Background(), in)
	if response.Status != "REJECTED" || !strings.Contains(response.Message, "HOST_START") || !strings.Contains(response.Message, "corr") || strings.Contains(response.Message, "SECRET") {
		t.Fatal("unsafe or missing evidence", response)
	}
	var out application.DevelopmentOutput
	data, err := os.ReadFile(filepath.Join(service.config.StateRoot, "development.json"))
	if err != nil || json.Unmarshal(data, &out) != nil || out.Result.PlannerFailureStage != "HOST_START" || out.Result.TaskState != domain.TaskStatusBlocked {
		t.Fatal("missing persisted failure evidence", err)
	}
	in.Actor.ExternalID = "unknown"
	if strings.Contains(service.Handle(context.Background(), in).Message, "HOST_START") {
		t.Fatal("unknown actor diagnostic disclosure")
	}
}

type developmentGenerator struct{ fail, wait bool }

func (developmentGenerator) ProveToolFree(context.Context) error { return nil }
func (g developmentGenerator) Generate(ctx context.Context, q ports.CandidateGenerationRequest) ([]byte, error) {
	if g.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if g.fail {
		return nil, domain.ErrCandidateDenied
	}
	return json.Marshal(domain.StructuredProposal{SchemaVersion: 1, Edits: []domain.StructuredEdit{{Operation: domain.WriteCreate, Target: "note.txt", PostimageContent: base64.StdEncoding.EncodeToString([]byte("hello\n")), PostimageIdentity: domain.CandidateDigest([]byte("hello\n"))}}})
}
func developmentFixture(t *testing.T, ops []string, p developmentPlanner, g developmentGenerator) (*DevelopmentService, *infrastructure.ManagedWorkspaceStore, infrastructure.ManagedWorkspace, string, ports.ActorEvidence) {
	t.Helper()
	ctx := context.Background()
	control := t.TempDir()
	os.Chmod(control, 0700)
	s, err := infrastructure.NewManagedWorkspaceStore(control, "pilot-instance")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	w, err := s.Provision(ctx, "pilot", nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := domain.CandidatePolicy{WriteTargets: []string{"note.txt"}, Limits: domain.CandidateLimits{MaxOperations: 1, MaxFileBytes: 1024, MaxTotalBytes: 1024, MaxPathBytes: 256, MaxOutputBytes: 8192}}
	repo, err := s.ProvisionRepository(ctx, w, policy, "v1")
	if err != nil {
		t.Fatal(err)
	}
	e := ports.ActorEvidence{Provider: "discord", ExternalID: "123"}
	grants := []ports.Grant{}
	for _, op := range ops {
		grants = append(grants, ports.Grant{PrincipalID: "operator", ProjectID: "pilot", Operation: op})
	}
	authority, err := infrastructure.NewActorAuthority([]infrastructure.ActorMapping{{Evidence: e, Principal: ports.Principal{ID: "operator"}}}, grants)
	if err != nil {
		t.Fatal(err)
	}
	config := DevelopmentConfig{ProjectID: "pilot", TaskID: "task", CorrelationID: "corr", Branch: "codex/pilot", PolicyVersion: "v1", Policy: policy, Route: application.ConversationSource{GuildID: "guild", ChannelID: "channel"}}
	scratch, state := t.TempDir(), t.TempDir()
	config.ControlRoot = control
	config.ScratchRoot = scratch
	config.StateRoot = state
	os.Chmod(state, 0700)
	service, err := composeDevelopment(ctx, config, p, g, authority, s, w, repo, scratch, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Shutdown(context.Background()) })
	return service, s, w, filepath.Join(control, w.WorkspaceID, "note.txt"), e
}
func TestDevelopmentE2ESeparateConfirmations(t *testing.T) {
	service, _, _, file, e := developmentFixture(t, []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"}, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	out, err := service.Begin(ctx, e, application.DevelopmentIntent{Objective: "create note.txt containing hello followed by newline", RequestedWriteTargets: []string{"note.txt"}})
	if err != nil || out.Review == nil {
		t.Fatal(out, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("mutation before approval")
	}
	writeIdentity := out.Review.Identity
	if _, err := service.Confirm(ctx, ports.ActorEvidence{Provider: "discord", ExternalID: "unknown"}, writeIdentity); err == nil {
		t.Fatal("unknown actor")
	}
	if _, err := service.Confirm(ctx, e, "divergent"); err == nil {
		t.Fatal("divergent confirmation")
	}
	for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
		if out.Review == nil || out.Review.Operation != op {
			t.Fatal("gate order", out)
		}
		out, err = service.Confirm(ctx, e, out.Review.Identity)
		if err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "hello\n" {
		t.Fatal("content", err)
	}
	if out.Result.TaskState != domain.TaskStatusDone || !domain.GitOID(out.Result.CommitOID) || out.Result.ApproverIdentity != "operator" || out.Result.WriteResult.State != domain.WriteApplied {
		t.Fatal(out)
	}
	if _, err := service.Confirm(ctx, e, writeIdentity); err == nil {
		t.Fatal("replay")
	}
	if err := service.Verify(ctx, out.Result); err != nil {
		t.Fatal("independent verification", err)
	}
	if err := os.WriteFile(file, []byte("tampered"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.Verify(ctx, out.Result); err == nil {
		t.Fatal("tampered physical state accepted")
	}
}
func TestDevelopmentConcurrentConfirmation(t *testing.T) {
	service, _, _, _, e := developmentFixture(t, []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"}, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	out, err := service.Begin(ctx, e, application.DevelopmentIntent{Objective: "create note.txt", RequestedWriteTargets: []string{"note.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := service.Confirm(ctx, e, out.Review.Identity); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("duplicate consumption", success)
	}
}
func TestDevelopmentChannelEvidenceAndRoute(t *testing.T) {
	service, _, _, file, e := developmentFixture(t, []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"}, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	in := application.ConversationInput{Source: service.config.Route, Actor: e, DevelopmentAction: "begin", RequestedWriteTargets: []string{"note.txt"}, Text: "create note.txt"}
	bad := in
	bad.Source.ChannelID = "other"
	if service.Handle(ctx, bad).Status != "REJECTED" {
		t.Fatal("route divergence")
	}
	response := service.Handle(ctx, in)
	if response.Status != "REVIEW_REQUIRED" {
		t.Fatal(response)
	}
	var saved application.DevelopmentOutput
	read := func() {
		data, err := os.ReadFile(filepath.Join(service.lease.root.Name(), "development.json"))
		if err != nil || json.Unmarshal(data, &saved) != nil {
			t.Fatal("persistent evidence", err)
		}
	}
	read()
	if !strings.Contains(response.Message, saved.Review.Identity) {
		t.Fatal("review identity missing from Channel", response)
	}
	for range 3 {
		in.DevelopmentAction = "confirm"
		in.Confirmation = saved.Review.Identity
		in.Text = ""
		response = service.Handle(ctx, in)
		if response.Status == "REJECTED" || response.Status == "BLOCKED" || len(response.Message) > 2000 {
			t.Fatal(response)
		}
		read()
		if saved.Review != nil && !strings.Contains(response.Message, saved.Review.Identity) {
			t.Fatal("review identity missing from Channel", response)
		}
	}
	if response.Status != "DONE" || saved.Result.TaskState != domain.TaskStatusDone {
		t.Fatal(response)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal(err)
	}
}
func TestDevelopmentChannelDeniedGateReturnsPartialReceipt(t *testing.T) {
	service, _, _, _, e := developmentFixture(t, []string{"WRITE_APPLY"}, developmentPlanner{}, developmentGenerator{})
	ctx := context.Background()
	out, err := service.Begin(ctx, e, application.DevelopmentIntent{Objective: "create note.txt", RequestedWriteTargets: []string{"note.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	out, err = service.Confirm(ctx, e, out.Review.Identity)
	if err != nil {
		t.Fatal(err)
	}
	in := application.ConversationInput{Source: service.config.Route, Actor: e, DevelopmentAction: "confirm", Confirmation: out.Review.Identity}
	response := service.Handle(ctx, in)
	if response.Status != "REJECTED" || !strings.Contains(response.Message, out.Result.WriteTransaction) || !strings.Contains(response.Message, "APPLIED") {
		t.Fatal("partial effect evidence omitted", response)
	}
	in.Actor.ExternalID = "unknown"
	response = service.Handle(ctx, in)
	if strings.Contains(response.Message, out.Result.CandidateIdentity) {
		t.Fatal("unknown actor result disclosure")
	}
}
func TestDevelopmentDeniedGates(t *testing.T) {
	for _, missing := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
		t.Run(missing, func(t *testing.T) {
			grants := []string{}
			for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
				if op != missing {
					grants = append(grants, op)
				}
			}
			service, _, _, file, e := developmentFixture(t, grants, developmentPlanner{}, developmentGenerator{})
			ctx := context.Background()
			out, err := service.Begin(ctx, e, application.DevelopmentIntent{Objective: "create note.txt", RequestedWriteTargets: []string{"note.txt"}})
			if err != nil {
				t.Fatal(err)
			}
			for out.Review.Operation != missing {
				out, err = service.Confirm(ctx, e, out.Review.Identity)
				if err != nil {
					t.Fatal(err)
				}
			}
			control := filepath.Dir(filepath.Dir(file))
			before, err := os.ReadFile(filepath.Join(control, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Confirm(ctx, e, out.Review.Identity); err == nil {
				t.Fatal("missing grant accepted")
			}
			after, err := os.ReadFile(filepath.Join(control, "state.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("state mutation after gate denied", err)
			}
			refPath := filepath.Join(control, service.effects.repository.RepositoryID, "refs", "heads", "codex", "pilot")
			ref, refErr := os.ReadFile(refPath)
			if missing == "GIT_COMMIT" {
				if refErr != nil || strings.TrimSpace(string(ref)) != service.effects.repository.InitialHead {
					t.Fatal("commit mutation after gate denied", refErr)
				}
			} else if !os.IsNotExist(refErr) {
				t.Fatal("branch mutation after gate denied", refErr)
			}
			if missing == "WRITE_APPLY" {
				if _, err := os.Stat(file); !os.IsNotExist(err) {
					t.Fatal("write after denial")
				}
			}
			if out.Result.CommitOID != "" {
				t.Fatal("commit after denial")
			}
		})
	}
	for _, failure := range []string{"planner-block", "provider", "cancel", "timeout"} {
		t.Run(failure, func(t *testing.T) {
			service, _, _, file, e := developmentFixture(t, []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"}, developmentPlanner{block: failure == "planner-block"}, developmentGenerator{fail: failure == "provider", wait: failure == "timeout"})
			timeout := time.Second
			if failure == "timeout" {
				timeout = 20 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			if _, err := service.Begin(ctx, e, application.DevelopmentIntent{Objective: "create note.txt", RequestedWriteTargets: []string{"note.txt"}}); err == nil {
				t.Fatal("failure accepted")
			}
			if _, err := os.Stat(file); !os.IsNotExist(err) {
				t.Fatal("mutation after failure")
			}
			entries, err := os.ReadDir(service.config.ScratchRoot)
			if err != nil || len(entries) != 0 {
				t.Fatal("candidate cleanup", err)
			}
		})
	}
}
