package composition

import (
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("service validation requires Linux")
	}
	workspace := t.TempDir()
	state := t.TempDir()
	if err := os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "evidence.txt"), []byte("public evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{StateDir: state, RequestTimeout: Duration(75 * time.Second), ShutdownTimeout: Duration(60 * time.Second), MaxIntentBytes: 4096, MaxEvidenceBytes: 32768, Projects: []ProjectConfig{{ID: "p", Name: "Pilot", Workspace: workspace, Evidence: map[domain.ActionType]domain.ActionParams{domain.ActionTypeReadFile: {ReadFile: &domain.ReadFileParams{Path: "evidence.txt"}}}}}, Routes: []application.ProjectRoute{{Source: application.ConversationSource{GuildID: "1", ChannelID: "2"}, ProjectID: "p"}}}
}
func TestConfigFailClosed(t *testing.T) {
	c := testConfig(t)
	if c.Validate() != nil {
		t.Fatal("valid config rejected")
	}
	for _, change := range []func(*Config){
		func(c *Config) { c.Routes = append(c.Routes, c.Routes[0]) },
		func(c *Config) { c.Routes[0].ProjectID = "missing" },
		func(c *Config) { c.StateDir = c.Projects[0].Workspace },
		func(c *Config) { c.RequestTimeout = 0 },
		func(c *Config) { c.MaxEvidenceBytes = 65536 },
		func(c *Config) {
			c.Projects[0].Evidence[domain.ActionTypeRunTests] = domain.ActionParams{RunTests: &domain.RunTestsParams{Target: "unit"}}
		},
		func(c *Config) {
			c.Projects[0].Evidence[domain.ActionTypeReadFile] = domain.ActionParams{ReadFile: &domain.ReadFileParams{Path: "../secret"}}
		},
	} {
		bad := testConfig(t)
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
}
func TestConfigDecodeStrict(t *testing.T) {
	for _, data := range []string{`{"unknown":true}`, `null`, `{} {}`, `{"StateDir":"x","StateDir":"y"}`, `{"StateDir":"x","statedir":"y"}`} {
		if _, err := DecodeConfig([]byte(data)); err == nil {
			t.Fatal("accepted", data)
		}
	}
}
