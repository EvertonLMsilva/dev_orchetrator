package application

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func testGit(t *testing.T, root string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("test git: %v: %.1000s", err, out)
	}
}

func gitFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	testGit(t, root, "init", "--quiet")
	putCapabilityFile(t, root, "file", "before\n")
	putCapabilityFile(t, root, "-option", "before\n")
	testGit(t, root, "add", "--", "file", "-option")
	testGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	return root
}

func TestGitStatus(t *testing.T) {
	root := gitFixture(t)
	got, err := (GitStatusExecutor{}).Execute(context.Background(), root)
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("clean: %+v %v", got, err)
	}
	putCapabilityFile(t, root, "untracked", "new")
	putCapabilityFile(t, root, "file", "after\n")
	got, err = (GitStatusExecutor{}).Execute(context.Background(), root)
	if err != nil || len(got.Entries) != 2 {
		t.Fatalf("changed: %+v %v", got, err)
	}
	if got.Entries[0].Path != "file" || got.Entries[0].WorkTreeStatus != "M" || got.Entries[1].IndexStatus != "?" {
		t.Fatalf("entries: %+v", got)
	}
}

func TestGitInvalidRepository(t *testing.T) {
	parent := gitFixture(t)
	child := filepath.Join(parent, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{t.TempDir(), child} {
		if _, err := (GitStatusExecutor{}).Execute(context.Background(), root); !errors.Is(err, ErrGitRepository) {
			t.Fatalf("status: %v", err)
		}
		if _, err := (GitDiffExecutor{}).Execute(context.Background(), root, ""); !errors.Is(err, ErrGitRepository) {
			t.Fatalf("diff: %v", err)
		}
	}
}

func TestGitContext(t *testing.T) {
	root := gitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (GitStatusExecutor{}).Execute(ctx, root); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (GitDiffExecutor{}).Execute(ctx, root, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := (GitDiffExecutor{}).Execute(ctx, root, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}

func TestGitUnavailable(t *testing.T) {
	root := gitFixture(t)
	t.Setenv("PATH", t.TempDir())
	if _, err := (GitStatusExecutor{}).Execute(context.Background(), root); !errors.Is(err, ErrGitUnavailable) {
		t.Fatal(err)
	}
}

func TestGitIgnoresEnvironment(t *testing.T) {
	root := gitFixture(t)
	outside := gitFixture(t)
	putCapabilityFile(t, outside, "outside", "private")
	t.Setenv("GIT_DIR", filepath.Join(outside, ".git"))
	t.Setenv("GIT_WORK_TREE", outside)
	got, err := (GitStatusExecutor{}).Execute(context.Background(), root)
	if err != nil || len(got.Entries) != 0 {
		t.Fatalf("environment escaped: %+v %v", got, err)
	}
}

func TestGitMetadataEscape(t *testing.T) {
	root := t.TempDir()
	outside := gitFixture(t)
	if err := os.Symlink(filepath.Join(outside, ".git"), filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := (GitStatusExecutor{}).Execute(context.Background(), root); !errors.Is(err, ErrGitRepository) {
		t.Fatal(err)
	}
}

func TestGitStatusRename(t *testing.T) {
	root := gitFixture(t)
	testGit(t, root, "mv", "--", "file", "new name\nwith newline")
	got, err := (GitStatusExecutor{}).Execute(context.Background(), root)
	if err != nil || len(got.Entries) != 1 || got.Entries[0].OriginalPath != "file" || got.Entries[0].Path != "new name\nwith newline" {
		t.Fatalf("rename: %+v %v", got, err)
	}
}
