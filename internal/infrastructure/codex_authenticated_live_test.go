package infrastructure

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in plus exact trusted configuration. Never consult default homes
// or require external authentication in go test/validate.sh.
func TestAuthenticatedCodexLiveOptIn(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_LIVE") != "1" {
		t.Skip("opt-in authenticated provider test")
	}
	homePath := os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOME")
	configuredHosts := os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS")
	if homePath == "" || configuredHosts == "" {
		t.Fatal("explicit authorized home and allowlist required")
	}
	home, err := NewAuthorizedCodexHome(homePath, true)
	if err != nil {
		t.Fatal("authorized home configuration rejected")
	}
	driver, err := NewDockerDriver()
	if err != nil {
		t.Fatal("Docker unavailable")
	}
	defer driver.Close()
	runtime, err := NewAuthenticatedDockerCodexExecutorRuntime(driver, home, strings.Split(configuredHosts, ","))
	if err != nil {
		t.Fatal("runtime configuration rejected")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	config, err := executionWorkspaceConfig(t.TempDir())
	if err != nil {
		t.Fatal("temporary workspace rejected")
	}
	session, err := runtime.start(ctx, config)
	if err != nil {
		t.Fatal("container/app-server=FAIL; B_P4_001=BLOCKED_BEFORE_REPRODUCTION")
	}
	defer func() {
		if session.Close() != nil {
			t.Error("cleanup=FAIL")
		}
	}()
	owned := session.(*CodexProcessTransport).docker.(*ownedCodexEgress)
	checkDenied := func() {
		if host := owned.blockedDestination(context.Background()); host != "" {
			t.Fatalf("BLOCKED_LIVE_DESTINATION blocked_destination=%s; B_P4_001=BLOCKED_BEFORE_REPRODUCTION; PLANNER_REVIEW", host)
		}
	}
	transport := codexContextTransport{ctx: ctx, session: session}
	ready, err := completeCodexHandshake(transport)
	checkDenied()
	if err != nil {
		t.Fatal("initialize=FAIL; B_P4_001=BLOCKED_BEFORE_REPRODUCTION")
	}
	t.Log("initialize=PASS")
	accountErr := requireCodexChatGPTAccount(transport)
	checkDenied()
	if accountErr != nil {
		t.Fatal("account_read=FAIL; B_P4_001=BLOCKED_BEFORE_REPRODUCTION")
	}
	t.Log("account_read=PASS authenticated=yes")
	thread, err := ready.StartThread()
	checkDenied()
	if err != nil {
		t.Fatal("thread_start=FAIL; B_P4_001=BLOCKED_BEFORE_REPRODUCTION")
	}
	t.Log("thread_start=PASS")
	turn, err := thread.StartTurn("Reply with exactly P6_RUNTIME_OK. Do not use tools, inspect files, or execute commands.")
	checkDenied()
	if err != nil || turn.Status != CodexTurnCompletedStatus || strings.TrimSpace(turn.Text) != "P6_RUNTIME_OK" {
		t.Fatal("turn_start=FAIL; B_P4_001=BLOCKED_BEFORE_REPRODUCTION; stop for Planner review, never expand allowlist")
	}
	t.Log("turn_start=PASS B_P4_001=NOT_REPRODUCED in the minimal temporary-workspace flow")
}
