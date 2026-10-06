package composition

import (
	"context"
	"crypto/sha256"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/infrastructure"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if e := filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		rel, _ := filepath.Rel(root, path)
		info, e := d.Info()
		if e != nil {
			return e
		}
		out[rel] = info.Mode().String()
		if info.Mode().IsRegular() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			out[rel] += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	return out
}
func TestMountedReadOnlyCompositionAllCapabilities(t *testing.T) {
	pilot := os.Getenv("P6_TEST_PILOT")
	if pilot == "" {
		t.Skip("requires isolated read-only fixture mount")
	}
	if e := requireReadOnly(pilot); e != nil {
		t.Fatal(e)
	}
	before := snapshot(t, pilot)
	actions := map[domain.ActionType]domain.ActionParams{
		domain.ActionTypeReadFile:  {ReadFile: &domain.ReadFileParams{Path: "public-evidence/README.md"}},
		domain.ActionTypeSearch:    {Search: &domain.SearchParams{Query: "status", Path: "public-evidence"}},
		domain.ActionTypeGitStatus: {},
		domain.ActionTypeGitDiff:   {GitDiff: &domain.GitDiffParams{Path: "public-evidence/README.md"}},
	}
	for kind, params := range actions {
		c := testConfig(t)
		c.Projects[0].Workspace = pilot
		c.Projects[0].Evidence = map[domain.ActionType]domain.ActionParams{kind: params}
		r := &scriptedRuntime{outputs: []string{fmt.Sprintf(`{"type":"REQUEST_EVIDENCE","reason":"read","evidenceKind":%q}`, kind), `{"type":"BLOCK","reason":"complete"}`}}
		s, e := buildService(context.Background(), c, infrastructure.NewCodexPlannerAdapter(r), requireReadOnly)
		if e != nil {
			t.Fatal(e)
		}
		response := s.Handle(context.Background(), application.ConversationInput{Source: c.Routes[0].Source, Text: "analyze"})
		if response.Status != "BLOCKED" || len(r.requests) != 2 || r.requests[1].Evidence[0].BotResult.Status != "SUCCESS" {
			t.Fatalf("%s: %#v %#v", kind, response, r.requests)
		}
		if e = s.Shutdown(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.WriteFile(filepath.Join(pilot, "public-evidence/README.md"), []byte("mutation"), 0600); e == nil {
		t.Fatal("filesystem write succeeded")
	}
	if e := os.WriteFile(filepath.Join(pilot, ".git/index.lock"), []byte("mutation"), 0600); e == nil {
		t.Fatal("Git write succeeded")
	}
	if after := snapshot(t, pilot); !reflect.DeepEqual(before, after) {
		t.Fatal("workspace or Git metadata changed")
	}
}
func TestProductionRootWithoutAuthFailsClosed(t *testing.T) {
	pilot := os.Getenv("P6_TEST_PILOT")
	if pilot == "" {
		t.Skip("requires isolated read-only fixture mount and empty runtime store")
	}
	// This test runs in a disposable network-none container without Docker socket
	// or runtime-auth volume. It never touches the provisioned P6.4 session.
	if _, e := os.Stat("/var/lib/dev-orchestrator/runtime-auth/auth.json"); !os.IsNotExist(e) {
		t.Fatal("test container must have no auth material")
	}
	c := testConfig(t)
	c.Projects[0].Workspace = pilot
	c.Projects[0].Evidence = map[domain.ActionType]domain.ActionParams{domain.ActionTypeReadFile: {ReadFile: &domain.ReadFileParams{Path: "public-evidence/README.md"}}}
	s, e := NewService(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	if s.home == nil {
		t.Fatal("real provider was not composed")
	}
	response := s.Handle(context.Background(), application.ConversationInput{Source: c.Routes[0].Source, Text: "analyze"})
	if response.Status != "REJECTED" {
		t.Fatal("absent auth succeeded", response)
	}
	if e = s.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat("/var/lib/dev-orchestrator/runtime-auth/auth.json"); !os.IsNotExist(e) {
		t.Fatal("auth provisioned implicitly")
	}
}
