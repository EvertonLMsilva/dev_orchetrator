// Local MCP-5 Docker integration harness. No production credentials/fixtures.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "prepare":
		err = prepare()
	case "probe":
		err = probe()
	default:
		err = errors.New("invalid mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "MCP5_LOCAL_SLICE FAILED")
		os.Exit(1)
	}
}
func write(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}
func prepare() error {
	for _, dir := range []string{"/fixture/admin", "/fixture/project", "/state"} {
		if os.MkdirAll(dir, 0700) != nil || os.Chmod(dir, 0700) != nil {
			return errors.New("fixture directory")
		}
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	token := hex.EncodeToString(random)
	digest := sha256.Sum256([]byte(token))
	if os.WriteFile("/fixture/client-token", []byte(token), 0600) != nil {
		return errors.New("token fixture")
	}
	if err := write("/fixture/admin/auth.json", map[string]any{"credentials": []any{map[string]string{"principalId": "local-reader", "sha256": hex.EncodeToString(digest[:])}}}); err != nil {
		return err
	}
	entries := []map[string]string{}
	for _, op := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		entries = append(entries, map[string]string{"principalId": "local-reader", "operation": op, "projectId": "p"})
	}
	if err := write("/fixture/admin/grants.json", map[string]any{"grants": entries}); err != nil {
		return err
	}
	if err := exec.Command("git", "-C", "/fixture/project", "init", "--quiet").Run(); err != nil {
		return err
	}
	config := map[string]any{"StateDir": "/state", "DisableDiscord": true, "RequestTimeout": "5s", "ShutdownTimeout": "10s", "MaxIntentBytes": 4096, "MaxEvidenceBytes": 32768, "Projects": []any{map[string]any{"ID": "p", "Name": "Controlled Docker project", "Workspace": "/fixture/project"}}, "MCP": map[string]any{"Listen": "0.0.0.0:8080", "Concurrency": 2, "Security": map[string]string{"AuthenticationFile": "/fixture/admin/auth.json", "GrantsFile": "/fixture/admin/grants.json", "AuditFile": "/state/mcp-audit.jsonl"}}}
	if err := write("/fixture/orchestrator.json", config); err != nil {
		return err
	}
	fmt.Println("MCP5_FIXTURE PREPARED")
	return nil
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}
func probe() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, err := os.ReadFile("/fixture/client-token")
	if err != nil {
		return err
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "mcp5-local-real-client", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: "http://127.0.0.1:8080/mcp", HTTPClient: &http.Client{Timeout: 10 * time.Second, Transport: bearer{token: string(token)}}}, &sdk.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		return err
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 4 {
		return errors.New("READ tool surface")
	}
	for _, op := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		args := map[string]any{"projectId": "p", "correlationId": "docker-" + op}
		if op == "project.tasks" {
			args["limit"] = 1
		}
		out, err := session.CallTool(ctx, &sdk.CallToolParams{Name: op, Arguments: args})
		if err != nil || out.IsError {
			return errors.New("READ slice")
		}
		data, _ := json.Marshal(out.StructuredContent)
		if op == "execution.status" && !strings.Contains(string(data), "UNAVAILABLE") {
			return errors.New("execution observation")
		}
		if op == "project.status" && !strings.Contains(string(data), "Controlled Docker project") {
			return errors.New("project projection")
		}
		fmt.Println("MCP5_LOCAL_SLICE", op, "PASS")
	}
	out, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "project.status", Arguments: map[string]any{"projectId": "unauthorized", "correlationId": "docker-negative"}})
	if err != nil || !out.IsError {
		return errors.New("negative accepted")
	}
	data, err := os.ReadFile("/state/mcp-audit.jsonl")
	if err != nil {
		return err
	}
	denied := false
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			CorrelationID string `json:"correlationId"`
			Outcome       string `json:"outcome"`
		}
		if json.Unmarshal([]byte(line), &event) != nil {
			return errors.New("audit record")
		}
		if event.CorrelationID == "docker-negative" {
			if event.Outcome == "AUTHORIZED" || event.Outcome == "SUCCESS" {
				return errors.New("negative reached READ")
			}
			if event.Outcome == "AUTHORIZATION_DENIED" {
				denied = true
			}
		}
	}
	if !denied || strings.Contains(string(data), string(token)) {
		return errors.New("audit evidence")
	}
	fmt.Println("MCP5_LOCAL_SLICE unauthorized DENY_AUDITED")
	clear(token)
	return nil
}
