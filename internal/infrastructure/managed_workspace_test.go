//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func managedFixture(t *testing.T) (*ManagedWorkspaceStore, ManagedWorkspace, MediatedWriteRequest) {
	t.Helper()
	control := t.TempDir()
	if err := os.Chmod(control, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewManagedWorkspaceStore(control, "first")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	w, err := s.Provision(ctx, "project", map[string][]byte{"old.txt": []byte("before")})
	if err != nil {
		t.Fatal(err)
	}
	p := mediatedPolicy()
	a, err := s.BuildFixtureCandidate(ctx, w, p, domain.CandidateContext{ProjectID: "project", TaskID: "task", CorrelationID: "corr"}, map[string][]byte{"old.txt": []byte("after"), "new.txt": []byte("created")})
	if err != nil {
		t.Fatal(err)
	}
	return s, w, mediatedRequest(t, s, w, p, a, "tx", "approval")
}
func assertManagedOriginal(t *testing.T, s *ManagedWorkspaceStore, w ManagedWorkspace) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(s.rootPath(w), "old.txt"))
	if e != nil || string(b) != "before" {
		t.Fatalf("preimage changed: %q %v", b, e)
	}
	if _, e = os.Lstat(filepath.Join(s.rootPath(w), "new.txt")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("create effect: %v", e)
	}
	if s.effects != 0 {
		t.Fatalf("effects %d", s.effects)
	}
}
func TestMediatedDenialsZeroEffects(t *testing.T) {
	for _, kind := range []string{"external", "generation", "identity", "project", "task", "correlation", "policy", "targets", "diff", "artifact", "post", "parameters", "transaction", "base", "blob", "extra", "exposed", "hardlink", "symlink", "fifo", "git", "traversal", "absolute", "case-alias"} {
		t.Run(kind, func(t *testing.T) {
			s, w, q := managedFixture(t)
			ctx := context.Background()
			c := q.Artifact.Candidate()
			switch kind {
			case "external":
				q.Workspace.WorkspaceID = "external"
			case "generation":
				q.Workspace.Generation++
			case "identity":
				q.Workspace.RootIdentity = domain.CandidateDigest([]byte("other"))
			case "project":
				q.Context.ProjectID = "other"
			case "task":
				q.Context.TaskID = "other"
			case "correlation":
				q.Context.CorrelationID = "other"
			case "policy":
				q.Context.PolicyVersion = "other"
			case "targets":
				q.Context.AllowedTargets = []string{"extra"}
			case "diff":
				q.Context.ApprovedDiffIdentity = domain.CandidateDigest(nil)
			case "artifact":
				q.Context.ApprovedArtifactID = "other"
			case "post":
				q.Context.ExpectedPostIdentity = domain.CandidateDigest(nil)
			case "parameters":
				q.Context.OperationParametersIdentity = domain.CandidateDigest(nil)
			case "transaction":
				q.TransactionID = "other"
			case "base":
				if e := os.WriteFile(filepath.Join(s.rootPath(w), "old.txt"), []byte("external change"), 0644); e != nil {
					t.Fatal(e)
				}
			case "blob":
				q.Artifact = domain.CandidateArtifact{}
			case "extra", "git", "traversal", "absolute":
				path := map[string]string{"extra": "extra.txt", "git": ".git/config", "traversal": "../escape", "absolute": "/escape"}[kind]
				c.Manifest.Entries[0].Target = path
				a, e := domain.NewCandidateArtifact(c, map[string][]byte{domain.CandidateDigest([]byte("created")): []byte("created"), domain.CandidateDigest([]byte("after")): []byte("after")})
				if e != nil {
					assertManagedOriginal(t, s, w)
					return
				}
				q.Artifact = a
			case "exposed":
				if e := s.ReportWritableExposure(ctx, w); e != nil {
					t.Fatal(e)
				}
			case "hardlink":
				if e := os.Link(filepath.Join(s.rootPath(w), "old.txt"), filepath.Join(s.rootPath(w), "alias")); e != nil {
					t.Fatal(e)
				}
			case "symlink", "fifo":
				p := filepath.Join(s.rootPath(w), "new.txt")
				var e error
				if kind == "symlink" {
					e = os.Symlink("old.txt", p)
				} else {
					e = unix.Mkfifo(p, 0600)
				}
				if e != nil {
					t.Fatal(e)
				}
			case "case-alias":
				if e := os.WriteFile(filepath.Join(s.rootPath(w), "NEW.txt"), []byte("alias"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.Apply(ctx, q); e == nil {
				t.Fatal("denial accepted")
			}
			if s.effects != 0 {
				t.Fatal("real promotion after denial")
			}
		})
	}
}

func TestMediatedFailureRestart(t *testing.T) {
	for _, stage := range []string{"lease", "prepared", "before-target", "staged", "promoted", "progress", "terminal"} {
		t.Run(stage, func(t *testing.T) {
			s, w, q := managedFixture(t)
			s.fail = func(at string, n int) error {
				if at == stage && (n == 0 || stage == "terminal") {
					return errors.New("crash")
				}
				return nil
			}
			if _, e := s.Apply(context.Background(), q); e == nil {
				t.Fatal("missing injected crash")
			}
			second, e := NewManagedWorkspaceStore(s.controlPath, "second")
			if e != nil {
				t.Fatal(e)
			}
			defer second.Close()
			if _, e = second.Apply(context.Background(), q); e == nil && stage != "terminal" && stage != "staged" {
				t.Fatal("incomplete lease granted authority")
			}
			r, e := second.Recover(context.Background(), w)
			if stage == "staged" {
				r, e = second.Apply(context.Background(), q)
			}
			switch stage {
			case "lease", "prepared", "before-target", "staged":
				if e != nil || r.State != domain.WriteAborted {
					t.Fatalf("before-effect recovery: %+v %v", r, e)
				}
			case "promoted", "progress":
				if !errors.Is(e, ErrManagedWriteBlocked) || r.State != domain.WriteRecoveryRequired {
					t.Fatalf("partial recovery: %+v %v", r, e)
				}
				if _, e = second.Apply(context.Background(), q); !errors.Is(e, ErrManagedWriteBlocked) {
					t.Fatalf("recovery replay %v", e)
				}
			case "terminal":
				if e != nil || r.State != domain.WriteApplied {
					t.Fatalf("terminal recovery: %+v %v", r, e)
				}
			}
		})
	}
}
func TestMediatedCrashAllPromoted(t *testing.T) {
	s, w, q := managedFixture(t)
	s.fail = func(at string, n int) error {
		if at == "promoted" && n == 1 {
			return errors.New("crash")
		}
		return nil
	}
	if _, e := s.Apply(context.Background(), q); e == nil {
		t.Fatal("missing crash")
	}
	second, e := NewManagedWorkspaceStore(s.controlPath, "second")
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	r, e := second.Recover(context.Background(), w)
	if e != nil || r.State != domain.WriteApplied {
		t.Fatalf("reconcile all post: %+v %v", r, e)
	}
	if _, e = second.Apply(context.Background(), q); e != nil || second.effects != 0 {
		t.Fatalf("replay effects %d %v", second.effects, e)
	}
}
func TestMediatedPersistenceFailureClosed(t *testing.T) {
	for failAt := 1; failAt <= 7; failAt++ {
		t.Run(fmtGeneration(uint64(failAt)), func(t *testing.T) {
			s, _, q := managedFixture(t)
			calls := 0
			s.persistFault = func() error {
				calls++
				if calls == failAt {
					return errors.New("disk error")
				}
				return nil
			}
			_, e := s.Apply(context.Background(), q)
			if e == nil {
				t.Fatal("persistence failure ignored")
			}
			if failAt <= 3 && s.effects != 0 {
				t.Fatal("effects without prepared journal")
			}
		})
	}
}
func TestMediatedConcurrentInstances(t *testing.T) {
	s, w, q := managedFixture(t)
	secondQ := mediatedRequest(t, s, w, q.Policy, q.Artifact, "other-transaction", "other-approval")
	second, e := NewManagedWorkspaceStore(s.controlPath, "second")
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	s.fail = func(stage string, n int) error {
		if stage == "lease" {
			close(entered)
			<-release
		}
		return nil
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr error
	go func() { defer wg.Done(); _, firstErr = s.Apply(context.Background(), q) }()
	<-entered
	if _, e = second.Apply(context.Background(), secondQ); !errors.Is(e, ErrManagedWriteBlocked) {
		t.Fatalf("second writer: %v", e)
	}
	close(release)
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if second.effects != 0 || s.effects != 2 {
		t.Fatalf("effects first=%d second=%d", s.effects, second.effects)
	}
	_ = w
}

func TestMediatedJournalSemanticCorruption(t *testing.T) {
	for _, kind := range []string{"blob", "lease", "progress", "post"} {
		t.Run(kind, func(t *testing.T) {
			s, _, q := managedFixture(t)
			s.fail = func(at string, n int) error {
				if at == "prepared" {
					return errors.New("crash")
				}
				return nil
			}
			if _, err := s.Apply(context.Background(), q); err == nil {
				t.Fatal("missing crash")
			}
			s.fail = nil
			err := s.exclusive(context.Background(), func(db *managedDatabase) error {
				j := db.Journals["tx"]
				switch kind {
				case "blob":
					f := j.Preimages["old.txt"]
					f.Content = []byte("tampered")
					j.Preimages["old.txt"] = f
				case "lease":
					w := db.Workspaces[j.Workspace.WorkspaceID]
					w.Lease.TransactionID = "wrong"
					db.Workspaces[j.Workspace.WorkspaceID] = w
				case "progress":
					j.Completed = 999
				case "post":
					delete(j.Postimages, "new.txt")
				}
				db.Journals["tx"] = j
				return s.save(db)
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Apply(context.Background(), q); !errors.Is(err, ErrManagedWriteBlocked) || s.effects != 0 {
				t.Fatalf("semantic corruption %v", err)
			}
		})
	}
}
func TestMediatedJournalCorruption(t *testing.T) {
	for _, bad := range []string{"{", "{}", "missing", "checksum", "incomplete"} {
		t.Run(bad, func(t *testing.T) {
			s, _, q := managedFixture(t)
			path := filepath.Join(s.controlPath, "state.json")
			switch bad {
			case "missing":
				if e := os.Rename(path, path+".lost"); e != nil {
					t.Fatal(e)
				}
			case "checksum":
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				b[len(b)/2] ^= 1
				if e = os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			case "incomplete":
				payload := []byte(`{"Version":1,"Workspaces":{},"Approvals":{},"Journals":null}`)
				b, _ := json.Marshal(managedEnvelope{domain.CandidateDigest(payload), payload})
				if e := os.WriteFile(path, b, 0600); e != nil {
					t.Fatal(e)
				}
			default:
				if e := os.WriteFile(path, []byte(bad), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := s.Apply(context.Background(), q); e == nil {
				t.Fatal("corrupt state accepted")
			}
			if s.effects != 0 {
				t.Fatal("corrupt state produced effects")
			}
		})
	}
}
func TestMediatedAdversarialSwap(t *testing.T) {
	for _, kind := range []string{"root", "parent", "symlink", "external-divergence"} {
		t.Run(kind, func(t *testing.T) {
			s, w, q := managedFixture(t)
			s.fail = func(at string, n int) error {
				if at != "staged" || n != 0 {
					return nil
				}
				switch kind {
				case "root":
					if e := os.Rename(s.rootPath(w), s.rootPath(w)+".moved"); e != nil {
						return e
					}
					return os.Mkdir(s.rootPath(w), 0700)
				case "parent":
					return os.Chmod(s.rootPath(w), 0755)
				case "symlink":
					return os.Symlink("old.txt", filepath.Join(s.rootPath(w), "new.txt"))
				case "external-divergence":
					return os.WriteFile(filepath.Join(s.rootPath(w), "old.txt"), []byte("external"), 0644)
				}
				return nil
			}
			if _, e := s.Apply(context.Background(), q); e == nil {
				t.Fatal("attack accepted")
			}
			if s.effects != 0 {
				t.Fatal("attack promoted")
			}
			if kind == "external-divergence" {
				b, e := os.ReadFile(filepath.Join(s.rootPath(w), "old.txt"))
				if e != nil || string(b) != "external" {
					t.Fatal("divergence overwritten")
				}
			}
		})
	}
}
func TestMediatedProcessCrash(t *testing.T) {
	if dir := os.Getenv("P10_CRASH_CONTROL"); dir != "" {
		stage := os.Getenv("P10_CRASH_STAGE")
		b, e := os.ReadFile(filepath.Join(dir, "fixture.json"))
		if e != nil {
			os.Exit(91)
		}
		var fixture struct {
			W     ManagedWorkspace
			P     domain.CandidatePolicy
			C     domain.WriteCandidate
			B     domain.WriteBinding
			Blobs map[string][]byte
		}
		if json.Unmarshal(b, &fixture) != nil {
			os.Exit(92)
		}
		s, e := NewManagedWorkspaceStore(dir, "child")
		if e != nil {
			os.Exit(93)
		}
		a, e := domain.NewCandidateArtifact(fixture.C, fixture.Blobs)
		if e != nil {
			os.Exit(94)
		}
		s.fail = func(at string, n int) error {
			if at == stage && n == 0 {
				os.Exit(77)
			}
			return nil
		}
		_, _ = s.Apply(context.Background(), MediatedWriteRequest{Workspace: fixture.W, Policy: fixture.P, Artifact: a, Context: fixture.B, ApprovalID: "approval", TransactionID: "tx"})
		os.Exit(95)
	}
	for _, stage := range []string{"prepared", "promoted"} {
		t.Run(stage, func(t *testing.T) {
			s, w, q := managedFixture(t)
			c := q.Artifact.Candidate()
			blobs := map[string][]byte{}
			for _, e := range c.Manifest.Entries {
				blobs[e.Postimage] = q.Artifact.Blob(e.Postimage)
			}
			data, _ := json.Marshal(struct {
				W     ManagedWorkspace
				P     domain.CandidatePolicy
				C     domain.WriteCandidate
				B     domain.WriteBinding
				Blobs map[string][]byte
			}{w, q.Policy, c, q.Context, blobs})
			if e := os.WriteFile(filepath.Join(s.controlPath, "fixture.json"), data, 0600); e != nil {
				t.Fatal(e)
			}
			childCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestMediatedProcessCrash$")
			child.Env = append(os.Environ(), "P10_CRASH_CONTROL="+s.controlPath, "P10_CRASH_STAGE="+stage)
			e := child.Run()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("child crash: %v", e)
			}
			second, e := NewManagedWorkspaceStore(s.controlPath, "restart")
			if e != nil {
				t.Fatal(e)
			}
			defer second.Close()
			r, e := second.Recover(context.Background(), w)
			if stage == "prepared" {
				if e != nil || r.State != domain.WriteAborted {
					t.Fatalf("pre crash: %+v %v", r, e)
				}
			} else if !errors.Is(e, ErrManagedWriteBlocked) || r.State != domain.WriteRecoveryRequired {
				t.Fatalf("partial crash: %+v %v", r, e)
			}
		})
	}
}

func TestMediatedParentReplacementAndNestedReplace(t *testing.T) {
	for _, attack := range []bool{false, true} {
		t.Run(fmtGeneration(uint64(map[bool]int{false: 0, true: 1}[attack])), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			s, err := NewManagedWorkspaceStore(dir, "instance")
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			ctx := context.Background()
			w, err := s.Provision(ctx, "project", map[string][]byte{"dir/old.txt": []byte("before")})
			if err != nil {
				t.Fatal(err)
			}
			p := mediatedPolicy()
			p.InputTargets = []string{"dir/old.txt"}
			p.WriteTargets = []string{"dir/old.txt"}
			a, err := s.BuildFixtureCandidate(ctx, w, p, domain.CandidateContext{ProjectID: "project", TaskID: "task", CorrelationID: "corr"}, map[string][]byte{"dir/old.txt": []byte("after")})
			if err != nil {
				t.Fatal(err)
			}
			q := mediatedRequest(t, s, w, p, a, "tx", "approval")
			if attack {
				s.fail = func(at string, n int) error {
					if at != "staged" {
						return nil
					}
					path := filepath.Join(s.rootPath(w), "dir")
					if err := os.Rename(path, path+".moved"); err != nil {
						return err
					}
					if err := os.Mkdir(path, 0700); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(path, "old.txt"), []byte("before"), 0644)
				}
			}
			r, err := s.Apply(ctx, q)
			if attack {
				if err == nil || s.effects != 0 {
					t.Fatalf("parent swap: %+v %v", r, err)
				}
			} else if err != nil || r.State != domain.WriteApplied {
				t.Fatalf("nested replace: %+v %v", r, err)
			}
		})
	}
}
func TestMediatedManagedProvisionDeniesInvalidFixtures(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	s, err := NewManagedWorkspaceStore(dir, "instance")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, path := range []string{"../escape", "/absolute", ".git/config", "a/../b", "CON"} {
		if _, err = s.Provision(context.Background(), "project", map[string][]byte{path: []byte("denied")}); err == nil {
			t.Fatalf("fixture %s accepted", path)
		}
	}
	a, err := s.Provision(context.Background(), "project", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Provision(context.Background(), "project", nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.WorkspaceID == b.WorkspaceID || a.RootIdentity == b.RootIdentity {
		t.Fatal("root reused")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err = os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = NewManagedWorkspaceStore(alias, "alias"); err == nil {
		t.Fatal("control symlink accepted")
	}
}
func TestMediatedBlobMutationDenied(t *testing.T) {
	s, _, q := managedFixture(t)
	c := q.Artifact.Candidate()
	blobs := map[string][]byte{}
	for _, e := range c.Manifest.Entries {
		blobs[e.Postimage] = []byte("tampered")
	}
	if _, err := domain.NewCandidateArtifact(c, blobs); err == nil {
		t.Fatal("tampered blob accepted")
	}
	b := q.Artifact.Blob(c.Manifest.Entries[0].Postimage)
	b[0] ^= 1
	if _, err := s.Apply(context.Background(), q); err != nil {
		t.Fatal("detached bytes corrupted immutable artifact", err)
	}
}
func TestMediatedBaseValidatedAfterLease(t *testing.T) {
	s, w, q := managedFixture(t)
	s.fail = func(at string, n int) error {
		if at == "lease" {
			return os.WriteFile(filepath.Join(s.rootPath(w), "old.txt"), []byte("changed-after-lease"), 0644)
		}
		return nil
	}
	r, err := s.Apply(context.Background(), q)
	if err == nil || r.State != domain.WriteAborted || s.effects != 0 {
		t.Fatalf("base after lease: %+v %v", r, err)
	}
}
func TestMediatedRecoveryBlocksNewTransaction(t *testing.T) {
	s, w, q := managedFixture(t)
	secondQ := mediatedRequest(t, s, w, q.Policy, q.Artifact, "second-tx", "second-approval")
	s.fail = func(at string, n int) error {
		if at == "promoted" && n == 0 {
			return errors.New("crash")
		}
		return nil
	}
	if _, err := s.Apply(context.Background(), q); err == nil {
		t.Fatal("crash ignored")
	}
	s.fail = nil
	if _, err := s.Recover(context.Background(), w); !errors.Is(err, ErrManagedWriteBlocked) {
		t.Fatal(err)
	}
	count := s.effects
	if _, err := s.Apply(context.Background(), secondQ); !errors.Is(err, ErrManagedWriteBlocked) || s.effects != count {
		t.Fatalf("new authority: %v", err)
	}
}

func TestMediatedParentSwapBeforeTarget(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewManagedWorkspaceStore(dir, "instance")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	w, err := s.Provision(ctx, "project", map[string][]byte{"dir/old.txt": []byte("before")})
	if err != nil {
		t.Fatal(err)
	}
	p := mediatedPolicy()
	p.InputTargets = []string{"dir/old.txt"}
	p.WriteTargets = []string{"dir/old.txt"}
	a, err := s.BuildFixtureCandidate(ctx, w, p, domain.CandidateContext{ProjectID: "project", TaskID: "task", CorrelationID: "corr"}, map[string][]byte{"dir/old.txt": []byte("after")})
	if err != nil {
		t.Fatal(err)
	}
	q := mediatedRequest(t, s, w, p, a, "tx", "approval")
	s.fail = func(at string, n int) error {
		if at != "before-target" {
			return nil
		}
		path := filepath.Join(s.rootPath(w), "dir")
		if err := os.Rename(path, path+".moved"); err != nil {
			return err
		}
		if err := os.Mkdir(path, 0700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(path, "old.txt"), []byte("before"), 0644)
	}
	r, err := s.Apply(ctx, q)
	if err == nil || r.State != domain.WriteRecoveryRequired || s.effects != 0 {
		t.Fatalf("parent before target: %+v %v", r, err)
	}
}
