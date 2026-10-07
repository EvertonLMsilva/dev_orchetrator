package composition

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReadOnlyGitLegitimateMetadata(t *testing.T) {
	for _, config := range []string{
		"[core]\n symlinks = false\n",
		"[branch \"codex/topic\"]\n vscode-merge-base = main\n",
		"[core]\n symlinks = false\n[branch \"main\"]\n vscode-merge-base = origin/main\n",
	} {
		dir := t.TempDir()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", "init", dir)
		if err := cmd.Run(); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n repositoryformatversion = 0\n bare = false\n"+config), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := (readOnlyCapabilities{}).GitStatus(context.Background(), dir); err != nil {
			t.Fatalf("legitimate config denied: %v", err)
		}
	}
}

func TestReadOnlyGitRejectsExecutableConfiguration(t *testing.T) {
	for _, configuration := range []string{
		"[filter \"attack\"]\n clean = touch sentinel\n",
		"[include]\n path = /outside/config\n",
		"[core]\n fsmonitor = touch sentinel\n",
		"[core]\n attributesFile = /outside/attributes\n",
		"[extensions]\n worktreeConfig = true\n",
		"[core]\n symlinks = false\n fsmonitor = touch sentinel\n",
		"[branch \"main\"]\n vscode-merge-base-command = touch sentinel\n",
		"[branch \"main\"]\n unknown = value\n",
	} {
		dir := t.TempDir()
		metadata := filepath.Join(dir, ".git")
		if e := os.Mkdir(metadata, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(metadata, "config"), []byte(configuration), 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := (readOnlyCapabilities{}).GitStatus(context.Background(), dir); !errors.Is(e, application.ErrReadOnlyDenied) {
			t.Fatalf("unsafe configuration accepted: %v", e)
		}
		if _, e := os.Stat(filepath.Join(dir, "sentinel")); !os.IsNotExist(e) {
			t.Fatal("configuration executed")
		}
	}
}
func TestSearchDeniesSensitiveDescendants(t *testing.T) {
	dir := t.TempDir()
	public := filepath.Join(dir, "public-evidence")
	if e := os.Mkdir(public, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(public, ".env"), []byte("secret credential"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := (readOnlyCapabilities{}).Search(context.Background(), dir, domain.SearchParams{Path: "public-evidence", Query: "secret"}); !errors.Is(e, application.ErrReadOnlyDenied) {
		t.Fatal("sensitive search allowed", e)
	}
}
