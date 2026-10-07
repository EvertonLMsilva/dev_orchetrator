package composition

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type secureProjects struct {
	ports.ProjectRepository
	calls int
}

func (p *secureProjects) FindByID(_ context.Context, id domain.ProjectID) (domain.Project, bool, error) {
	p.calls++
	return domain.Project{ID: id, Name: "Project"}, true, nil
}

type secureTasks struct {
	ports.TaskRepository
	calls int
	err   error
}

func (t *secureTasks) FindByProject(context.Context, domain.ProjectID) ([]domain.Task, error) {
	t.calls++
	return []domain.Task{}, t.err
}
func writeSecurity(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMCPReadComposition(t *testing.T) {
	dir := t.TempDir()
	config := MCPReadConfig{AuthenticationFile: filepath.Join(dir, "auth.json"), GrantsFile: filepath.Join(dir, "grants.json"), AuditFile: filepath.Join(dir, "audit.jsonl")}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	token := []byte(hex.EncodeToString(random))
	digest := sha256.Sum256(token)
	auth := fmt.Sprintf(`{"credentials":[{"principalId":"authenticated-user","sha256":"%s"}]}`, hex.EncodeToString(digest[:]))
	grant := `{"grants":[{"principalId":"authenticated-user","operation":"project.tasks","projectId":"p"}]}`
	writeSecurity(t, config.AuthenticationFile, auth)
	writeSecurity(t, config.GrantsFile, grant)
	p := &secureProjects{}
	tasks := &secureTasks{}
	runtime, err := NewMCPReadRuntime(config, p, tasks, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := readcontracts.ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 1}
	query := func(e []byte, op string, r any, wantSuccess bool) {
		t.Helper()
		out, err := runtime.Query(context.Background(), ports.AuthenticationEvidence{Material: e}, op, r)
		if wantSuccess {
			if err != nil || out == nil {
				t.Fatal("expected READ success", err)
			}
		} else if err == nil || out != nil {
			t.Fatal("denied query exposed data")
		}
	}
	query(token, "project.tasks", req, true)
	if tasks.calls != 1 {
		t.Fatal("READ not executed")
	}
	baseline := p.calls
	query(nil, "project.tasks", req, false)
	query([]byte("authenticated-user"), "project.tasks", req, false)
	other := req
	other.ProjectID = "other"
	query(token, "project.tasks", other, false)
	query(token, "project.status", readcontracts.ProjectStatusRequest{ProjectID: "p", CorrelationID: "c"}, false)
	query(token, "WRITE", req, false)
	writeSecurity(t, config.GrantsFile, `{"grants":[]}`)
	query(token, "project.tasks", req, false)
	writeSecurity(t, config.GrantsFile, `invalid`)
	query(token, "project.tasks", req, false)
	if p.calls != baseline || tasks.calls != 1 {
		t.Fatal("denied requests reached project/READ")
	}
	writeSecurity(t, config.GrantsFile, grant)
	tasks.err = errors.New("raw-sensitive-backend-error")
	query(token, "project.tasks", req, false)
	writeSecurity(t, config.AuthenticationFile, `{"credentials":[]}`)
	baseline = p.calls
	query(token, "project.tasks", req, false)
	if p.calls != baseline {
		t.Fatal("invalid authentication config bypass")
	}
	data, err := os.ReadFile(config.AuditFile)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, token) || bytes.Contains(data, []byte("raw-sensitive-backend-error")) || bytes.Contains(data, []byte("sha256")) {
		t.Fatal("secret/raw error in audit")
	}
	outcomes := map[string]int{}
	attempts := map[string][]string{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event ports.ReadAuditEvent
		if json.Unmarshal(line, &event) != nil {
			t.Fatal("invalid audit record")
		}
		outcomes[event.Outcome]++
		attempts[event.AttemptID] = append(attempts[event.AttemptID], event.Outcome)
		if event.Outcome == "SUCCESS" && event.PrincipalID != "authenticated-user" {
			t.Fatal("caller selected principal")
		}
	}
	for _, outcome := range []string{"RECEIVED", "AUTHORIZED", "SUCCESS", "AUTHENTICATION_DENIED", "AUTHORIZATION_DENIED", "READ_FAILURE"} {
		if outcomes[outcome] == 0 {
			t.Fatal("missing audit outcome", outcome)
		}
	}
	for _, sequence := range attempts {
		if sequence[0] != "RECEIVED" {
			t.Fatal("attempt not audited first")
		}
		if sequence[len(sequence)-1] == "SUCCESS" && (len(sequence) != 3 || sequence[1] != "AUTHORIZED") {
			t.Fatal("success before authorization audit")
		}
	}
	// An unusable audit source blocks authentication/READ even with valid files.
	writeSecurity(t, config.AuthenticationFile, auth)
	config.AuditFile = dir
	broken, err := NewMCPReadRuntime(config, p, tasks, nil)
	if err == nil {
		out, queryErr := broken.Query(context.Background(), ports.AuthenticationEvidence{Material: token}, "project.tasks", req)
		if queryErr == nil || out != nil {
			t.Fatal("audit failure allowed result")
		}
	}
	if p.calls != baseline {
		t.Fatal("audit failure reached READ")
	}
	config.AuthenticationFile = config.GrantsFile
	if _, err := NewMCPReadRuntime(config, p, tasks, nil); err == nil {
		t.Fatal("invalid composition accepted")
	}
}
