package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"dev-orchestrator/internal/adapters/localsecurity"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"fmt"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type protectedProjects struct {
	ports.ProjectRepository
	calls atomic.Int32
}

func (p *protectedProjects) FindByID(_ context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	p.calls.Add(1)
	return domain.Project{ID: id, Name: "Controlled project", Workspace: "trusted"}, true, nil
}

type protectedTasks struct{ ports.TaskRepository }

func (protectedTasks) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	return []domain.Task{}, nil
}

type controlledGit struct{ application.LocalCapabilities }

func (controlledGit) GitStatus(context.Context, string) (ports.GitStatusResult, error) {
	return ports.GitStatusResult{Entries: []ports.GitStatusEntry{}}, nil
}

func TestMCPRealClientSecureVerticalSlice(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	directory := t.TempDir()
	authPath := filepath.Join(directory, "auth.json")
	grantsPath := filepath.Join(directory, "grants.json")
	auditPath := filepath.Join(directory, "audit.jsonl")
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(random)
	digest := sha256.Sum256([]byte(token))
	put := func(path, data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(authPath, fmt.Sprintf(`{"credentials":[{"principalId":"validated-reader","sha256":"%x"}]}`, digest))
	grants := []map[string]string{}
	for _, operation := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		grants = append(grants, map[string]string{"principalId": "validated-reader", "operation": operation, "projectId": "p"})
	}
	data, _ := json.Marshal(map[string]any{"grants": grants})
	validGrants := string(data)
	put(grantsPath, validGrants)
	projects := &protectedProjects{}
	runtime := application.NewSecureReadRuntime(localsecurity.NewAuthentication(authPath), localsecurity.NewGrants(grantsPath), localsecurity.NewAudit(auditPath), projects, protectedTasks{}, application.NewLocalAgent(projects, nil, application.NewReadOnlyActionAllowlist(), controlledGit{}))
	adapter, err := New(ctx, runtime, time.Second, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Stop()
	server := httptest.NewServer(adapter)
	defer server.Close()
	session := connect(t, server.URL, token)
	for _, operation := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		args := map[string]any{"projectId": "p", "correlationId": "positive"}
		if operation == "project.tasks" {
			args["limit"] = 1
		}
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: operation, Arguments: args})
		if err != nil || result.IsError {
			t.Fatal("secure READ failed", operation, err)
		}
	}
	for _, scenario := range []string{"missing evidence", "invalid evidence", "wrong project", "wrong operation", "grant source failure", "audit failure"} {
		t.Run(scenario, func(t *testing.T) {
			before := projects.calls.Load()
			active := session
			operation := "project.status"
			project := "p"
			switch scenario {
			case "missing evidence":
				active = connect(t, server.URL, "")
			case "invalid evidence":
				active = connect(t, server.URL, "validated-reader")
			case "wrong project":
				project = "outside"
			case "wrong operation":
				put(grantsPath, `{"grants":[{"principalId":"validated-reader","operation":"project.tasks","projectId":"p"}]}`)
			case "grant source failure":
				put(grantsPath, `broken`)
			case "audit failure":
				put(auditPath, `partial`)
			}
			result, err := active.CallTool(ctx, &sdk.CallToolParams{Name: operation, Arguments: map[string]any{"projectId": project, "correlationId": "denied"}})
			if err != nil || !result.IsError || projects.calls.Load() != before {
				t.Fatal("denied request reached MCP-2", err, projects.calls.Load(), before)
			}
			if scenario != "audit failure" {
				audit, err := os.ReadFile(auditPath)
				if err != nil || !strings.Contains(string(audit), "DENIED") || strings.Contains(string(audit), token) {
					t.Fatal("negative audit absent/unsafe")
				}
			}
			put(grantsPath, validGrants)
		})
	}
}
