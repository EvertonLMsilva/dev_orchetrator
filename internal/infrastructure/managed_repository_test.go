//go:build linux

package infrastructure

import (
	"bytes"
	"compress/zlib"
	"context"
	"dev-orchestrator/internal/domain"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type gitFixture struct {
	s      *ManagedWorkspaceStore
	w      ManagedWorkspace
	r      domain.ManagedRepositoryIdentity
	write  MediatedWriteRequest
	branch domain.GitBinding
	commit domain.GitBinding
}

func controlledGitFixture(t *testing.T) gitFixture {
	t.Helper()
	ctx := context.Background()
	s, w, q := managedFixture(t)
	if err := os.WriteFile(filepath.Join(s.rootPath(w), "tracked.txt"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.rootPath(w), "secret.txt"), []byte("excluded-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	baseline := q.Policy.Clone()
	baseline.InputTargets = append(baseline.InputTargets, "tracked.txt")
	baseline.ExcludedTargets = []string{"secret.txt"}
	r, err := s.ProvisionRepository(ctx, w, baseline, "policy-v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, q); err != nil {
		t.Fatal(err)
	}
	b := domain.GitBinding{OperationKind: domain.GitBranch, ProjectID: w.ProjectID, TaskID: "task", CorrelationID: "corr", WorkspaceIdentity: w.Identity(), RepositoryIdentity: r.Identity(), ExpectedHead: r.InitialHead, BranchName: "codex/task", StartPoint: r.InitialHead, PolicyVersion: r.PolicyVersion}
	b.OperationParametersIdentity = b.ParametersIdentity()
	issueGit(t, s, b, "branch-approval")
	if _, err = s.MutateGit(ctx, w, r, "branch-op", "branch-approval", b); err != nil {
		t.Fatal(err)
	}
	actor := domain.GitActor{Name: "Test Author", Email: "test@example.invalid", UnixSeconds: 1700000000}
	c, err := s.PlanGitCommit(ctx, w, r, q.TransactionID, "branch-op", b, "approved message\n", actor, actor)
	if err != nil {
		t.Fatal(err)
	}
	issueGit(t, s, c, "commit-approval")
	return gitFixture{s, w, r, q, b, c}
}
func gitFixtureDir(f gitFixture) string { return filepath.Join(f.s.controlPath, f.r.RepositoryID) }
func gitFixtureRef(t *testing.T, f gitFixture) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(gitFixtureDir(f), "refs/heads", f.branch.BranchName))
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(string(b))
}
func gitFixtureDigest(t *testing.T, root string) string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		files[strings.TrimPrefix(path, root)] = domain.CandidateDigest(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(files)
	return domain.CandidateDigest(b)
}

func TestControlledGitIndexAndUnrelatedPreserved(t *testing.T) {
	f := controlledGitFixture(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(f.s.rootPath(f.w), "tracked.txt"), []byte("working-unrelated"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.s.rootPath(f.w), "untracked.txt"), []byte("untracked"), 0644); err != nil {
		t.Fatal(err)
	}
	err := f.s.exclusive(ctx, func(db *managedDatabase) error {
		root, e := f.s.openWorkspace(db, f.w)
		if e != nil {
			return e
		}
		defer root.Close()
		repo, e := f.s.openRepository(db, f.w, f.r)
		if e != nil {
			return e
		}
		defer repo.Close()
		_, e = gitBuildTree(ctx, root, repo, "index", f.r.InitialHead, map[string]domain.CandidateFile{"tracked.txt": {Target: "tracked.txt", Mode: 0644, Content: []byte("staged-unrelated")}})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	before, e := os.ReadFile(filepath.Join(gitFixtureDir(f), "index"))
	if e != nil {
		t.Fatal(e)
	}
	result, e := f.s.MutateGit(ctx, f.w, f.r, "commit-op", "commit-approval", f.commit)
	if e != nil {
		t.Fatal(e)
	}
	after, e := os.ReadFile(filepath.Join(gitFixtureDir(f), "index"))
	if e != nil || string(after) != string(before) {
		t.Fatal("original index changed")
	}
	for path, want := range map[string]string{"tracked.txt": "working-unrelated", "untracked.txt": "untracked", "secret.txt": "excluded-fixture"} {
		b, e := os.ReadFile(filepath.Join(f.s.rootPath(f.w), path))
		if e != nil || string(b) != want {
			t.Fatal("unrelated content changed", path)
		}
	}
	assertGitCommit(t, f.s, f.w, f.r, result, f.commit)
	err = f.s.exclusive(ctx, func(db *managedDatabase) error {
		root, e := f.s.openWorkspace(db, f.w)
		if e != nil {
			return e
		}
		defer root.Close()
		repo, e := f.s.openRepository(db, f.w, f.r)
		if e != nil {
			return e
		}
		defer repo.Close()
		tree, e := managedGitTree(ctx, root, repo, result.OID)
		if e != nil {
			return e
		}
		if tree["tracked.txt"].OID != gitObjectID("blob", []byte("original")) || len(tree) != 3 {
			t.Fatal("unrelated/excluded content committed")
		}
		out, e := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "remote")
		if e != nil || len(out) != 0 {
			t.Fatal("remote configured")
		}
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestControlledGitGateDenialsZeroMetadataMutation(t *testing.T) {
	for _, kind := range []string{"kind", "task", "correlation", "workspace", "repository", "generation", "parent", "tree", "message", "actor", "policy", "candidate", "branch", "operation", "head", "ref", "postimage", "index-lock", "missing", "identity", "config"} {
		t.Run(kind, func(t *testing.T) {
			f := controlledGitFixture(t)
			w, r, b := f.w, f.r, f.commit
			operation := "commit-op"
			switch kind {
			case "kind":
				b.OperationKind = "WRITE_APPLY"
			case "task":
				b.TaskID = "other"
			case "correlation":
				b.CorrelationID = "other"
			case "workspace":
				w.WorkspaceID = "external"
			case "repository":
				r.RepositoryID = "external"
			case "generation":
				w.Generation++
			case "parent":
				b.Parent = strings.Repeat("b", 40)
			case "tree":
				b.ExpectedTree = strings.Repeat("c", 40)
			case "message":
				b.Message = "changed\n"
				b.MessageIdentity = domain.CandidateDigest([]byte(b.Message))
			case "actor":
				b.Author.Name = "changed"
			case "policy":
				b.PolicyVersion = "other"
			case "candidate":
				b.CandidateIdentity = domain.CandidateDigest(nil)
			case "branch":
				b.BranchName = "--inject"
			case "operation":
				operation = "branch-op"
			case "head":
				if e := os.WriteFile(filepath.Join(gitFixtureDir(f), "refs/heads/main"), []byte(strings.Repeat("b", 40)+"\n"), 0644); e != nil {
					t.Fatal(e)
				}
			case "ref":
				if e := os.WriteFile(filepath.Join(gitFixtureDir(f), "refs/heads", f.branch.BranchName), []byte(strings.Repeat("b", 40)+"\n"), 0644); e != nil {
					t.Fatal(e)
				}
			case "postimage":
				if e := os.WriteFile(filepath.Join(f.s.rootPath(w), "old.txt"), []byte("changed"), 0644); e != nil {
					t.Fatal(e)
				}
			case "index-lock":
				if e := os.WriteFile(filepath.Join(gitFixtureDir(f), "index.lock"), []byte("existing"), 0600); e != nil {
					t.Fatal(e)
				}
			case "missing":
				if e := os.Rename(gitFixtureDir(f), gitFixtureDir(f)+".moved"); e != nil {
					t.Fatal(e)
				}
			case "identity":
				if e := os.Rename(gitFixtureDir(f), gitFixtureDir(f)+".moved"); e != nil {
					t.Fatal(e)
				}
				if e := os.Mkdir(gitFixtureDir(f), 0700); e != nil {
					t.Fatal(e)
				}
			case "config":
				if e := os.WriteFile(filepath.Join(gitFixtureDir(f), "config"), []byte(managedGitConfig+"[alias]\n update-ref = !touch forbidden\n[core]\n hooksPath = /tmp/evil\n"), 0644); e != nil {
					t.Fatal(e)
				}
			}
			b.OperationParametersIdentity = b.ParametersIdentity()
			digest := ""
			if kind != "missing" {
				digest = gitFixtureDigest(t, gitFixtureDir(f))
			}
			if _, e := f.s.MutateGit(context.Background(), w, r, operation, "commit-approval", b); e == nil {
				t.Fatal("gate/state mismatch accepted")
			}
			if kind != "missing" && gitFixtureDigest(t, gitFixtureDir(f)) != digest {
				t.Fatal("Git mutation after denial")
			}
		})
	}
}
func TestControlledGitHooksEnvAndMessageIsolation(t *testing.T) {
	f := controlledGitFixture(t)
	marker := filepath.Join(f.s.controlPath, "hook-ran")
	hooks := filepath.Join(gitFixtureDir(f), "hooks")
	if e := os.MkdirAll(hooks, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"reference-transaction", "pre-commit", "commit-msg", "post-commit"} {
		if e := os.WriteFile(filepath.Join(hooks, name), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0755); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("GIT_DIR", t.TempDir())
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", hooks)
	b := f.commit
	b.Message = "literal $(touch '" + marker + "')\n"
	b.MessageIdentity = domain.CandidateDigest([]byte(b.Message))
	b.OperationParametersIdentity = b.ParametersIdentity()
	issueGit(t, f.s, b, "literal-approval")
	result, e := f.s.MutateGit(context.Background(), f.w, f.r, "literal-op", "literal-approval", b)
	if e != nil {
		t.Fatal(e)
	}
	assertGitCommit(t, f.s, f.w, f.r, result, b)
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("hook or shell executed")
	}
}
func TestControlledGitBranchReplayAndApprovalReuse(t *testing.T) {
	f := controlledGitFixture(t)
	before := gitFixtureDigest(t, gitFixtureDir(f))
	r, e := f.s.MutateGit(context.Background(), f.w, f.r, "branch-op", "branch-approval", f.branch)
	if e != nil || r.State != "APPLIED" || before != gitFixtureDigest(t, gitFixtureDir(f)) {
		t.Fatalf("branch replay %+v %v", r, e)
	}
	if _, e = f.s.MutateGit(context.Background(), f.w, f.r, "different-op", "branch-approval", f.branch); e == nil {
		t.Fatal("approval reused")
	}
	if _, e = f.s.MutateGit(context.Background(), f.w, f.r, "commit-op", "commit-approval", f.commit); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.MutateGit(context.Background(), f.w, f.r, "branch-op", "branch-approval", f.branch); !errors.Is(e, ErrManagedWriteBlocked) {
		t.Fatal("replay accepted after branch moved", e)
	}
}
func TestControlledGitCrashReconciliation(t *testing.T) {
	for _, kind := range []string{"branch", "commit", "object", "uncertain", "store"} {
		t.Run(kind, func(t *testing.T) {
			f := controlledGitFixture(t)
			b := f.commit
			operation, approval := "commit-op", "commit-approval"
			if kind == "branch" {
				b = f.branch
				b.BranchName = "codex/recovery"
				b.OperationParametersIdentity = b.ParametersIdentity()
				operation, approval = "recovery-branch", "recovery-approval"
				issueGit(t, f.s, b, approval)
			}
			if kind == "store" {
				count := 0
				f.s.persistFault = func() error {
					count++
					if count == 3 {
						return errors.New("disk failure")
					}
					return nil
				}
			} else {
				f.s.fail = func(at string, n int) error {
					wanted := "git-ref"
					if kind == "object" {
						wanted = "git-object"
					}
					if at != wanted {
						return nil
					}
					if kind == "uncertain" {
						if e := os.WriteFile(filepath.Join(gitFixtureDir(f), "refs/heads", b.BranchName), []byte(strings.Repeat("b", 40)+"\n"), 0644); e != nil {
							return e
						}
					}
					return errors.New("crash")
				}
			}
			if _, e := f.s.MutateGit(context.Background(), f.w, f.r, operation, approval, b); e == nil {
				t.Fatal("failure injection ignored")
			}
			second, e := NewManagedWorkspaceStore(f.s.controlPath, "restart")
			if e != nil {
				t.Fatal(e)
			}
			defer second.Close()
			result, e := second.RecoverGit(context.Background(), f.w, operation)
			if kind == "object" || kind == "uncertain" {
				if !errors.Is(e, ErrManagedWriteBlocked) || result.State != "RECOVERY_REQUIRED" {
					t.Fatalf("uncertain recovery %+v %v", result, e)
				}
				if _, e = second.MutateGit(context.Background(), f.w, f.r, operation, approval, b); !errors.Is(e, ErrManagedWriteBlocked) {
					t.Fatal("recovery replay", e)
				}
			} else {
				if e != nil || result.State != "APPLIED" {
					t.Fatalf("recovery %+v %v", result, e)
				}
				before := gitFixtureDigest(t, gitFixtureDir(f))
				if _, e = second.MutateGit(context.Background(), f.w, f.r, operation, approval, b); e != nil || before != gitFixtureDigest(t, gitFixtureDir(f)) {
					t.Fatal("replay mutated", e)
				}
			}
		})
	}
}
func TestControlledGitProvisionBoundaryAndReplay(t *testing.T) {
	ctx := context.Background()
	s, w, q := managedFixture(t)
	r, e := s.ProvisionRepository(ctx, w, q.Policy, "policy-v1")
	if e != nil {
		t.Fatal(e)
	}
	if r.InitialBaseIdentity != q.Artifact.Candidate().BaseIdentity || r.WorkspaceGeneration != w.Generation || r.WorkspaceID != w.WorkspaceID {
		t.Fatal("baseline identity")
	}
	if _, e = os.Lstat(filepath.Join(s.rootPath(w), ".git")); !os.IsNotExist(e) {
		t.Fatal("content metadata")
	}
	if _, e = s.ProvisionRepository(ctx, w, q.Policy, "policy-v1"); e == nil {
		t.Fatal("repository reprovisioned")
	}
	if _, e = s.Apply(ctx, q); e != nil {
		t.Fatal("Git metadata polluted content snapshot", e)
	}
	c := q.Artifact.Candidate()
	c.Manifest.Entries[0].Target = ".git/config"
	if _, e = domain.NewCandidateArtifact(c, map[string][]byte{}); e == nil {
		t.Fatal("metadata target accepted")
	}
}
func TestControlledGitProvisionUnregisteredBlocked(t *testing.T) {
	s, w, q := managedFixture(t)
	if e := os.Mkdir(filepath.Join(s.controlPath, gitRepoName(w)), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ProvisionRepository(context.Background(), w, q.Policy, "policy-v1"); !errors.Is(e, ErrManagedWriteBlocked) {
		t.Fatal("unexpected repo adopted", e)
	}
}
func TestControlledGitSharedAuthority(t *testing.T) {
	for _, first := range []string{"git", "write", "provision"} {
		t.Run(first, func(t *testing.T) {
			ctx := context.Background()
			f := controlledGitFixture(t)
			s, w, q := f.s, f.w, f.write
			var writeRequest MediatedWriteRequest
			if first != "provision" {
				a, e := s.BuildFixtureCandidate(ctx, w, q.Policy, domain.CandidateContext{ProjectID: "project", TaskID: "second", CorrelationID: "second"}, map[string][]byte{"old.txt": []byte("next")})
				if e != nil {
					t.Fatal(e)
				}
				writeRequest = mediatedRequest(t, s, w, q.Policy, a, "second-write", "second-write-approval")
			} else {
				s, w, q = managedFixture(t)
			}
			second, e := NewManagedWorkspaceStore(s.controlPath, "second")
			if e != nil {
				t.Fatal(e)
			}
			defer second.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			stage := "git-lease"
			if first == "write" {
				stage = "lease"
			}
			s.fail = func(at string, n int) error {
				if at == stage {
					close(entered)
					<-release
				}
				return nil
			}
			var wg sync.WaitGroup
			wg.Add(1)
			var firstErr error
			go func() {
				defer wg.Done()
				switch first {
				case "git":
					_, firstErr = s.MutateGit(ctx, w, f.r, "commit-op", "commit-approval", f.commit)
				case "write":
					_, firstErr = s.Apply(ctx, writeRequest)
				case "provision":
					_, firstErr = s.ProvisionRepository(ctx, w, q.Policy, "policy-v1")
				}
			}()
			<-entered
			switch first {
			case "git":
				_, e = second.Apply(ctx, writeRequest)
			case "write":
				_, e = second.MutateGit(ctx, w, f.r, "commit-op", "commit-approval", f.commit)
			case "provision":
				_, e = second.ProvisionRepository(ctx, w, q.Policy, "policy-v1")
			}
			if !errors.Is(e, ErrManagedWriteBlocked) {
				close(release)
				wg.Wait()
				t.Fatalf("authority overlap: %v", e)
			}
			close(release)
			wg.Wait()
			if firstErr != nil {
				t.Fatal(firstErr)
			}
		})
	}
}
func TestControlledGitProcessCrash(t *testing.T) {
	if dir := os.Getenv("P10_GIT_CRASH_CONTROL"); dir != "" {
		raw, e := os.ReadFile(filepath.Join(dir, "git-fixture.json"))
		if e != nil {
			os.Exit(91)
		}
		var fixture struct {
			W ManagedWorkspace
			R domain.ManagedRepositoryIdentity
			B domain.GitBinding
		}
		if json.Unmarshal(raw, &fixture) != nil {
			os.Exit(92)
		}
		s, e := NewManagedWorkspaceStore(dir, "child")
		if e != nil {
			os.Exit(93)
		}
		s.fail = func(at string, n int) error {
			if at == "git-ref" {
				os.Exit(77)
			}
			return nil
		}
		_, _ = s.MutateGit(context.Background(), fixture.W, fixture.R, "crash-op", "crash-approval", fixture.B)
		os.Exit(94)
	}
	for _, kind := range []string{"branch", "commit"} {
		t.Run(kind, func(t *testing.T) {
			f := controlledGitFixture(t)
			b := f.commit
			if kind == "branch" {
				b = f.branch
				b.BranchName = "codex/crash"
				b.OperationParametersIdentity = b.ParametersIdentity()
			}
			issueGit(t, f.s, b, "crash-approval")
			raw, _ := json.Marshal(struct {
				W ManagedWorkspace
				R domain.ManagedRepositoryIdentity
				B domain.GitBinding
			}{f.w, f.r, b})
			if e := os.WriteFile(filepath.Join(f.s.controlPath, "git-fixture.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlledGitProcessCrash$")
			cmd.Env = append(os.Environ(), "P10_GIT_CRASH_CONTROL="+f.s.controlPath)
			e := cmd.Run()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("child crash %v", e)
			}
			s, e := NewManagedWorkspaceStore(f.s.controlPath, "restart")
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			r, e := s.RecoverGit(context.Background(), f.w, "crash-op")
			if e != nil || r.State != "APPLIED" {
				t.Fatalf("restart %+v %v", r, e)
			}
		})
	}
}

func TestControlledGitCASDivergence(t *testing.T) {
	f := controlledGitFixture(t)
	foreign := strings.Repeat("b", 40)
	f.s.fail = func(at string, n int) error {
		if at == "git-object" {
			return os.WriteFile(filepath.Join(gitFixtureDir(f), "refs/heads", f.branch.BranchName), []byte(foreign+"\n"), 0644)
		}
		return nil
	}
	if _, err := f.s.MutateGit(context.Background(), f.w, f.r, "commit-op", "commit-approval", f.commit); err == nil {
		t.Fatal("CAS divergence accepted")
	}
	if gitFixtureRef(t, f) != foreign {
		t.Fatal("divergent ref overwritten")
	}
}
func TestControlledGitJournalCorruptionBlocked(t *testing.T) {
	for _, kind := range []string{"message", "result", "lease"} {
		t.Run(kind, func(t *testing.T) {
			f := controlledGitFixture(t)
			f.s.fail = func(at string, n int) error {
				if at == "git-prepared" {
					return errors.New("crash")
				}
				return nil
			}
			if _, err := f.s.MutateGit(context.Background(), f.w, f.r, "commit-op", "commit-approval", f.commit); err == nil {
				t.Fatal("crash ignored")
			}
			f.s.fail = nil
			err := f.s.exclusive(context.Background(), func(db *managedDatabase) error {
				j := db.GitOperations["commit-op"]
				switch kind {
				case "message":
					j.CommitBytes = gitCommitBytes(j.Binding.ExpectedTree, j.Binding.Parent, "unapproved\n", j.Binding.Author, j.Binding.Committer)
					j.ExpectedCommit = gitObjectID("commit", j.CommitBytes)
				case "result":
					j.ExpectedTree = strings.Repeat("b", 40)
				case "lease":
					w := db.Workspaces[f.w.WorkspaceID]
					w.Lease.Kind = "GIT_BRANCH"
					db.Workspaces[f.w.WorkspaceID] = w
				}
				db.GitOperations[j.OperationID] = j
				return f.s.save(db)
			})
			if err != nil {
				t.Fatal(err)
			}
			before := gitFixtureDigest(t, gitFixtureDir(f))
			if _, err = f.s.RecoverGit(context.Background(), f.w, "commit-op"); !errors.Is(err, ErrManagedWriteBlocked) {
				t.Fatal("corrupt journal accepted", err)
			}
			if before != gitFixtureDigest(t, gitFixtureDir(f)) {
				t.Fatal("corrupt journal caused Git mutation")
			}
		})
	}
}
func TestControlledGitProvisionCrashAndPersistenceFailure(t *testing.T) {
	for _, kind := range []string{"crash", "persistence", "before-effects"} {
		t.Run(kind, func(t *testing.T) {
			s, w, q := managedFixture(t)
			if kind == "persistence" {
				count := 0
				s.persistFault = func() error {
					count++
					if count == 4 {
						return errors.New("disk failure")
					}
					return nil
				}
			} else {
				s.fail = func(at string, n int) error {
					stage := "git-provisioned"
					if kind == "before-effects" {
						stage = "git-lease"
					}
					if at == stage {
						return errors.New("crash")
					}
					return nil
				}
			}
			if _, err := s.ProvisionRepository(context.Background(), w, q.Policy, "policy-v1"); err == nil {
				t.Fatal("provision failure ignored")
			}
			second, err := NewManagedWorkspaceStore(s.controlPath, "restart")
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			result, err := second.RecoverGit(context.Background(), w, "provision-"+w.WorkspaceID)
			if kind == "before-effects" {
				if !errors.Is(err, ErrManagedWriteBlocked) {
					t.Fatal("missing repository auto recovered", err)
				}
			} else if err != nil || result.State != "APPLIED" {
				t.Fatalf("baseline recovery %+v %v", result, err)
			}
		})
	}
}

func TestControlledGitCorruptBlobDeniedBeforeMutation(t *testing.T) {
	f := controlledGitFixture(t)
	id := gitObjectID("blob", []byte("after"))
	path := filepath.Join(gitFixtureDir(f), "objects", id[:2], id[2:])
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write([]byte("blob 7\x00corrupt")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, compressed.Bytes(), 0644); err != nil {
		t.Fatal(err)
	}
	before := gitFixtureDigest(t, gitFixtureDir(f))
	if _, err := f.s.MutateGit(context.Background(), f.w, f.r, "commit-op", "commit-approval", f.commit); err == nil {
		t.Fatal("corrupt reused object accepted")
	}
	if gitFixtureDigest(t, gitFixtureDir(f)) != before {
		t.Fatal("Git mutated after corrupt blob denial")
	}
}
