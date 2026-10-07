package composition

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func worktreeGit(t *testing.T, root string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture Git: %v %.1000s", err, out)
	}
}

func worktreeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	worktreeGit(t, root, "init", "--quiet")
	writeSecurity(t, filepath.Join(root, "tracked"), "before\n")
	writeSecurity(t, filepath.Join(root, ".gitattributes"), "tracked filter=attack\n")
	worktreeGit(t, root, "add", "--", "tracked", ".gitattributes")
	worktreeGit(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "fixture")
	// Create a real linked worktree; READ still observes only the primary root.
	worktreeGit(t, root, "worktree", "add", "--quiet", "-b", "linked", filepath.Join(t.TempDir(), "linked"))
	worktreeGit(t, root, "config", "extensions.worktreeConfig", "true")
	writeSecurity(t, filepath.Join(root, "tracked"), "after!\n")
	return root
}

func worktreeAgent(t *testing.T, root string) (*application.LocalAgentDispatcher, ports.ProjectRepository) {
	t.Helper()
	p := memory.NewProjectRepository()
	project, err := domain.NewProject("p", "Pilot", root)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Save(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	return application.NewLocalAgent(p, nil, application.NewReadOnlyActionAllowlist(), readOnlyCapabilities{}), p
}

func TestReadOnlyGitWorktreeLegitimate(t *testing.T) {
	root := worktreeFixture(t)
	before, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	agent, projects := worktreeAgent(t, root)
	result, err := agent.Execute(context.Background(), domain.Action{Type: domain.ActionTypeGitStatus, ProjectID: "p"})
	if err != nil || result.GitStatusResult == nil || len(result.GitStatusResult.Entries) != 1 {
		t.Fatalf("legitimate worktree READ: %+v %v", result, err)
	}
	// Exercise the same secure boundary composed by the MCP runtime.
	c, token := inboundConfig(t)
	runtime, err := NewMCPReadRuntime(c.MCP.Security, projects, nil, agent)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runtime.Query(context.Background(), ports.AuthenticationEvidence{Material: []byte(token)}, "git.status", readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "worktree-legitimate"})
	if err != nil || out == nil {
		t.Fatalf("secure READ: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "config"))
	if err != nil || string(before) != string(after) {
		t.Fatal("READ changed repository configuration")
	}
}

func TestGitReadWorktreeConfigIsActive(t *testing.T) {
	root := worktreeFixture(t)
	worktreeGit(t, root, "config", "--file", filepath.Join(root, ".git", "config.worktree"), "filter.attack.clean", "touch filter-ran; cat")
	if _, err := (application.GitStatusExecutor{}).Execute(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "filter-ran")); err != nil {
		t.Fatal("expected existing controlled Git READ to consult config.worktree and execute filter", err)
	}
	// A command-scope false value does not prevent Git loading this file.
	other := worktreeFixture(t)
	worktreeGit(t, other, "config", "--file", filepath.Join(other, ".git", "config.worktree"), "filter.attack.clean", "touch filter-ran; cat")
	worktreeGit(t, other, "-c", "extensions.worktreeConfig=false", "status", "--porcelain=v1", "-z")
	if _, err := os.Stat(filepath.Join(other, "filter-ran")); err != nil {
		t.Fatal("command override unexpectedly suppressed worktree config", err)
	}
}

func TestReadOnlyGitWorktreeConfigDenied(t *testing.T) {
	for _, kind := range []string{"filter", "include", "includeIf", "inert", "malformed", "directory", "symlink", "dangling"} {
		t.Run(kind, func(t *testing.T) {
			root := worktreeFixture(t)
			config := filepath.Join(root, ".git", "config.worktree")
			switch kind {
			case "filter":
				worktreeGit(t, root, "config", "--file", config, "filter.attack.clean", "touch filter-ran; cat")
			case "include", "includeIf":
				target := filepath.Join(t.TempDir(), "included")
				writeSecurity(t, target, "[filter \"attack\"]\n clean = touch filter-ran; cat\n")
				key := "include.path"
				if kind == "includeIf" {
					key = "includeIf.gitdir:**.path"
				}
				worktreeGit(t, root, "config", "--file", config, key, target)
			case "inert":
				writeSecurity(t, config, "[core]\n filemode = false\n")
			case "malformed":
				writeSecurity(t, config, "[broken\n")
			case "directory":
				if err := os.Mkdir(config, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling":
				target := filepath.Join(t.TempDir(), "alternate")
				if kind == "symlink" {
					writeSecurity(t, target, "[filter \"attack\"]\n clean = touch filter-ran; cat\n")
				}
				if err := os.Symlink(target, config); err != nil {
					t.Fatal(err)
				}
			}
			agent, _ := worktreeAgent(t, root)
			if _, err := agent.Execute(context.Background(), domain.Action{Type: domain.ActionTypeGitStatus, ProjectID: "p"}); !errors.Is(err, application.ErrReadOnlyDenied) {
				t.Fatalf("alternate config accepted: %v", err)
			}
			if _, err := (readOnlyCapabilities{}).GitDiff(context.Background(), root, domain.GitDiffParams{}); !errors.Is(err, application.ErrReadOnlyDenied) {
				t.Fatalf("diff alternate config accepted: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(root, "filter-ran")); !os.IsNotExist(err) {
				t.Fatal("READ executed alternate configuration")
			}
		})
	}
}

func TestReadOnlyGitWorktreeStillDeniesDangerousCommonConfig(t *testing.T) {
	for _, key := range []string{"filter.attack.clean", "include.path", "core.fsmonitor", "core.attributesFile", "extensions.partialClone"} {
		t.Run(key, func(t *testing.T) {
			root := worktreeFixture(t)
			worktreeGit(t, root, "config", key, "touch filter-ran")
			if err := safeGitConfig(context.Background(), root); !errors.Is(err, application.ErrReadOnlyDenied) {
				t.Fatalf("unsafe common config accepted: %v", err)
			}
		})
	}
	if inertGitKey("extensions.worktreeconfig") {
		t.Fatal("extension must not become inert allowlist entry")
	}
}

func TestReadOnlyGitWorktreeTopologyDenied(t *testing.T) {
	for _, kind := range []string{"commondir", "missing-head", "head-symlink", "invalid-value", "implicit-value", "false-value"} {
		t.Run(kind, func(t *testing.T) {
			root := worktreeFixture(t)
			metadata := filepath.Join(root, ".git")
			switch kind {
			case "commondir":
				writeSecurity(t, filepath.Join(metadata, "commondir"), t.TempDir())
			case "missing-head", "head-symlink":
				// Move only fixture-owned metadata, never a host repository.
				if err := os.Rename(filepath.Join(metadata, "HEAD"), filepath.Join(metadata, "fixture-head")); err != nil {
					t.Fatal(err)
				}
				if kind == "head-symlink" {
					if err := os.Symlink("fixture-head", filepath.Join(metadata, "HEAD")); err != nil {
						t.Fatal(err)
					}
				}
			case "invalid-value", "false-value":
				value := "invalid"
				if kind == "false-value" {
					value = "false"
				}
				worktreeGit(t, root, "config", "extensions.worktreeConfig", value)
			case "implicit-value":
				writeSecurity(t, filepath.Join(metadata, "config"), "[extensions]\n worktreeConfig\n")
			}
			if err := safeGitConfig(context.Background(), root); !errors.Is(err, application.ErrReadOnlyDenied) {
				t.Fatalf("unsupported topology/value accepted: %v", err)
			}
		})
	}
}

func TestReadOnlyGitIgnoresLinkedWorktreeConfig(t *testing.T) {
	root := worktreeFixture(t)
	writeSecurity(t, filepath.Join(root, ".git", "worktrees", "linked", "config.worktree"), "[filter \"attack\"]\n clean = touch filter-ran; cat\n")
	agent, _ := worktreeAgent(t, root)
	result, err := agent.Execute(context.Background(), domain.Action{Type: domain.ActionTypeGitStatus, ProjectID: "p"})
	if err != nil || result.GitStatusResult == nil || len(result.GitStatusResult.Entries) != 1 {
		t.Fatalf("primary repository read: %+v %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "filter-ran")); !os.IsNotExist(err) {
		t.Fatal("READ consulted another worktree configuration")
	}
}

func TestMCPReadWorktreeRealWorkspace(t *testing.T) {
	root := os.Getenv("MCP7_TEST_WORKSPACE")
	if root == "" {
		t.Skip("requires an explicitly mounted read-only real workspace")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	direct, err := (application.GitStatusExecutor{}).Execute(ctx, root)
	if err != nil {
		t.Fatalf("executor: %v", err)
	}
	agent, projects := worktreeAgent(t, root)
	c, token := inboundConfig(t)
	runtime, err := NewMCPReadRuntime(c.MCP.Security, projects, nil, agent)
	if err != nil {
		t.Fatal(err)
	}
	out, err := runtime.Query(ctx, ports.AuthenticationEvidence{Material: []byte(token)}, "git.status", readcontracts.GitStatusRequest{ProjectID: "p", CorrelationID: "worktree-real"})
	if err != nil {
		t.Fatalf("secure real READ: %v", err)
	}
	response, ok := out.(readcontracts.GitStatusResponse)
	if !ok || response.ChangedEntries != len(direct.Entries) {
		t.Fatalf("projection mismatch: %T", out)
	}
	t.Logf("secure real READ PASS changedEntries=%d", response.ChangedEntries)
}

func TestMCPInboundWorktreeRead(t *testing.T) {
	root := worktreeFixture(t)
	c, token := inboundConfig(t)
	c.Projects[0].Workspace = root
	service, err := buildService(context.Background(), c, nil, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer service.Shutdown(context.Background())
	if err := service.StartMCP(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "worktree-read-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: service.MCPAddress() + "/mcp", HTTPClient: &http.Client{Transport: mcpBearer{token: token}}}, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(correlation string, wantError bool) {
		t.Helper()
		out, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "git.status", Arguments: map[string]any{"projectId": "p", "correlationId": correlation}})
		if err != nil || out == nil || out.IsError != wantError {
			t.Fatalf("MCP git.status error=%v want=%v: %v", out, wantError, err)
		}
	}
	call("worktree-http-pass", false)
	worktreeGit(t, root, "config", "--file", filepath.Join(root, ".git", "config.worktree"), "filter.attack.clean", "touch filter-ran; cat")
	call("worktree-http-denied", true)
	if _, err := os.Lstat(filepath.Join(root, "filter-ran")); !os.IsNotExist(err) {
		t.Fatal("MCP READ executed alternate configuration")
	}
	audit, err := os.ReadFile(c.MCP.Security.AuditFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, correlation := range []string{"worktree-http-pass", "worktree-http-denied"} {
		outcomes := []string{}
		for _, line := range bytes.Split(audit, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var event ports.ReadAuditEvent
			if err := json.Unmarshal(line, &event); err != nil {
				t.Fatal(err)
			}
			if event.CorrelationID == correlation {
				outcomes = append(outcomes, string(event.Outcome))
			}
		}
		want := "RECEIVED,AUTHORIZED,SUCCESS"
		if correlation == "worktree-http-denied" {
			want = "RECEIVED,AUTHORIZED,READ_FAILURE"
		}
		if strings.Join(outcomes, ",") != want {
			t.Fatalf("audit %s: %v", correlation, outcomes)
		}
	}
}
