package composition

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"fmt"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type mcpBearer struct{ token string }

func (b mcpBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}
func inboundConfig(t *testing.T) (Config, string) {
	c := testConfig(t)
	c.DisableDiscord = true
	c.Routes = nil
	c.Projects[0].Evidence = nil
	c.RequestTimeout = Duration(2 * time.Second)
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("permissions")
	}
	token := make([]byte, 32)
	rand.Read(token)
	material := hex.EncodeToString(token)
	sum := sha256.Sum256([]byte(material))
	auth := filepath.Join(dir, "auth.json")
	grants := filepath.Join(dir, "grants.json")
	writeSecurity(t, auth, fmt.Sprintf(`{"credentials":[{"principalId":"reader","sha256":"%x"}]}`, sum))
	entries := []map[string]string{}
	for _, op := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		entries = append(entries, map[string]string{"principalId": "reader", "operation": op, "projectId": "p"})
	}
	data, _ := json.Marshal(map[string]any{"grants": entries})
	writeSecurity(t, grants, string(data))
	c.MCP = &MCPInboundConfig{Listen: "127.0.0.1:0", Concurrency: 2, Security: MCPReadConfig{AuthenticationFile: auth, GrantsFile: grants, AuditFile: filepath.Join(c.StateDir, "mcp-audit.jsonl")}}
	return c, material
}
func TestMCPInboundLifecycleAndSecurity(t *testing.T) {
	c, token := inboundConfig(t)
	service, err := buildService(context.Background(), c, nil, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer service.Shutdown(context.Background())
	if service.MCPReady() {
		t.Fatal("ready before listener")
	}
	if err := service.StartMCP(); err != nil {
		t.Fatal(err)
	}
	endpoint := service.MCPAddress()
	response, err := http.Get(endpoint + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || !service.MCPReady() {
		t.Fatal("not ready")
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "local-real-client", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: endpoint + "/mcp", HTTPClient: &http.Client{Transport: mcpBearer{token: token}}}, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, op := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		args := map[string]any{"projectId": "p", "correlationId": "local-" + op}
		if op == "project.tasks" {
			args["limit"] = 1
		}
		out, err := session.CallTool(ctx, &sdk.CallToolParams{Name: op, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if op == "git.status" {
			if !out.IsError {
				t.Fatal("non-Git workspace should not invent Git result")
			}
		} else if out.IsError {
			t.Fatal("READ failed", op)
		}
		if op == "execution.status" {
			data, _ := json.Marshal(out)
			if !strings.Contains(string(data), "UNAVAILABLE") {
				t.Fatal("invented execution")
			}
		}
	}
	out, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "project.status", Arguments: map[string]any{"projectId": "unauthorized", "correlationId": "negative"}})
	if err != nil || !out.IsError {
		t.Fatal("unauthorized result")
	}
	data, err := os.ReadFile(c.MCP.Security.AuditFile)
	if err != nil || !strings.Contains(string(data), "AUTHORIZATION_DENIED") || strings.Contains(string(data), token) {
		t.Fatal("missing/unsafe negative audit")
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event ports.ReadAuditEvent
		json.Unmarshal([]byte(line), &event)
		if event.CorrelationID == "negative" && (event.Outcome == "AUTHORIZED" || event.Outcome == "SUCCESS") {
			t.Fatal("negative reached READ")
		}
	}
	writeSecurity(t, c.MCP.Security.GrantsFile, `broken`)
	out, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "project.status", Arguments: map[string]any{"projectId": "p", "correlationId": "source-failure"}})
	if err != nil || !out.IsError {
		t.Fatal("grants failure bypass")
	}
	shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if service.Shutdown(shutdown) != nil || service.MCPReady() {
		t.Fatal("shutdown failed")
	}
}
func TestMCPInboundStartupFailClosed(t *testing.T) {
	for _, scenario := range []string{"auth invalid", "audit unusable", "inside workspace", "invalid listen", "duplicate writer"} {
		t.Run(scenario, func(t *testing.T) {
			c, _ := inboundConfig(t)
			switch scenario {
			case "auth invalid":
				writeSecurity(t, c.MCP.Security.AuthenticationFile, `{}`)
			case "audit unusable":
				c.MCP.Security.AuditFile = c.StateDir
			case "inside workspace":
				c.MCP.Security.GrantsFile = filepath.Join(c.Projects[0].Workspace, "grants.json")
				writeSecurity(t, c.MCP.Security.GrantsFile, `{"grants":[]}`)
			case "invalid listen":
				c.MCP.Listen = "public.invalid:8080"
			}
			if scenario == "duplicate writer" {
				first, err := buildService(context.Background(), c, nil, func(string) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				defer first.Shutdown(context.Background())
			}
			service, err := buildService(context.Background(), c, nil, func(string) error { return nil })
			if err == nil {
				service.Shutdown(context.Background())
				t.Fatal("invalid startup accepted")
			}
		})
	}
}
