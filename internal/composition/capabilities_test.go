package composition

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOnlyGitRejectsExecutableConfiguration(t *testing.T) {
	for _, configuration := range []string{
		"[filter \"attack\"]\n clean = touch sentinel\n",
		"[include]\n path = /outside/config\n",
		"[core]\n fsmonitor = touch sentinel\n",
		"[core]\n attributesFile = /outside/attributes\n",
		"[extensions]\n worktreeConfig = true\n",
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
