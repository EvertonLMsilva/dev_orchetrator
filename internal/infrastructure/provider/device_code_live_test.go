package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"dev-orchestrator/internal/infrastructure"
)

func validLiveHosts(hosts string) bool {
	return hosts == "auth.openai.com,chatgpt.com" || hosts == "chatgpt.com,auth.openai.com"
}

func validLivePresentation(url, code string) bool {
	return url == "https://auth.openai.com/codex/device" && regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`).MatchString(code)
}

func liveEnabled(value string) bool { return value == "1" }

// Credentials/codes are never written through testing.T, stdout or stderr.
// A real controlling terminal is mandatory and is opened before any process.
func TestDeviceCodeLiveOptIn(t *testing.T) {
	if !liveEnabled(os.Getenv("DEV_ORCHESTRATOR_CODEX_DEVICE_LOGIN_LIVE")) {
		t.Skip("explicit device login opt-in required")
	}
	if runtime.GOOS != "linux" || !validLiveHosts(os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS")) {
		t.Fatal("Linux and exact approved hosts required")
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal("local administrative terminal required")
	}
	defer terminal.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 17*time.Minute)
	defer cancel()
	ctx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	store, err := NewCodexRuntimeHome()
	if err != nil {
		t.Fatal("runtime home unavailable")
	}
	defer store.Close()
	container, err := infrastructure.NewRuntimeHomeDockerContainer(os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS"))
	if err != nil {
		t.Fatal("runtime Docker unavailable")
	}
	lease, err := store.Bootstrap(ctx, container)
	if err != nil {
		container.Destroy(ctx)
		t.Fatal("fresh runtime home preparation failed")
	}
	defer func() {
		if lease.Abort(context.Background()) != nil {
			t.Error("cleanup=FAIL")
		}
	}()
	session, err := container.Start(ctx)
	if err != nil {
		t.Fatal("app_server=FAIL")
	}
	bootstrap := NewDeviceCodeBootstrap(liveContextTransport{session}, 15*time.Minute)
	attempt, err := bootstrap.Start(ctx)
	if err != nil {
		liveDenied(t, container)
		t.Fatal("LOGIN=" + liveLoginFailure(ctx, bootstrap.deadline))
	}
	if !validLivePresentation(attempt.VerificationURL(), attempt.UserCode()) {
		t.Fatal("verification presentation rejected")
	}
	if _, err = fmt.Fprintf(terminal, "verificationUrl=%s\nuserCode=%s\n", attempt.VerificationURL(), attempt.UserCode()); err != nil {
		t.Fatal("administrative presentation failed")
	}
	attempt = LoginAttempt{}
	if _, err = bootstrap.Wait(ctx); err != nil {
		liveDenied(t, container)
		t.Fatal("LOGIN=" + liveLoginFailure(ctx, bootstrap.deadline))
	}
	t.Log("LOGIN=PASS")
	kind, code := infrastructure.ReadRuntimeHomeAccount(ctx, session)
	liveDenied(t, container)
	liveAccountResult(t, kind, code)
	// Routing failure still preserves a valid freshly created session as evidence.
	if session.Close() != nil {
		t.Fatal("app_server_stop=FAIL")
	}
	if lease.Finish(ctx) != nil {
		t.Fatal("session_persistence=FAIL")
	}
	if store.Close() != nil {
		t.Fatal("runtime_home_close=FAIL")
	}
	restarted, err := NewCodexRuntimeHome()
	if err != nil {
		t.Fatal("RESTART_SESSION=FAIL")
	}
	defer restarted.Close()
	next, err := infrastructure.NewRuntimeHomeDockerContainer(os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS"))
	if err != nil {
		t.Fatal("RESTART_SESSION=FAIL")
	}
	nextLease, err := restarted.Materialize(ctx, next)
	if err != nil {
		next.Destroy(ctx)
		t.Fatal("RESTART_SESSION=FAIL")
	}
	defer func() {
		if nextLease.Abort(context.Background()) != nil {
			t.Error("cleanup=FAIL")
		}
	}()
	nextSession, err := next.Start(ctx)
	if err != nil || initializeLiveSession(ctx, nextSession) != nil {
		t.Fatal("RESTART_SESSION=FAIL")
	}
	restartKind, _ := infrastructure.ReadRuntimeHomeAccount(ctx, nextSession)
	liveDenied(t, next)
	if nextSession.Close() != nil || nextLease.Finish(ctx) != nil {
		t.Fatal("RESTART_SESSION=FAIL")
	}
	if restartKind != "pass" {
		t.Error("RESTART_SESSION=FAIL classification=" + restartKind)
	} else {
		t.Log("RESTART_SESSION=PASS")
	}
	if kind != "pass" {
		t.Error("ACCOUNT_READ=FAIL")
	}
}

func liveLoginFailure(ctx context.Context, deadline time.Time) string {
	if ctx.Err() == context.DeadlineExceeded || !deadline.IsZero() && time.Now().After(deadline) {
		return "TIMEOUT"
	}
	if ctx.Err() == context.Canceled {
		return "CANCELLED"
	}
	return "FAIL"
}

func liveDenied(t *testing.T, c *infrastructure.RuntimeHomeDockerContainer) {
	t.Helper()
	if host := c.DeniedHost(context.Background()); host != "" {
		t.Fatalf("DENY hostname=%s; PLANNER_REVIEW", host)
	}
}

func liveAccountResult(t *testing.T, kind string, code *int64) {
	t.Helper()
	if kind == "pass" {
		t.Log("ACCOUNT_READ=PASS B_P4_001=NOT_REPRODUCED_WITH_FRESH_RUNTIME_SESSION")
		return
	}
	if kind == "workspace_routing" {
		if code != nil {
			t.Logf("ACCOUNT_READ=FAIL classification=workspace_routing rpc_code=%d", *code)
		} else {
			t.Log("ACCOUNT_READ=FAIL classification=workspace_routing")
		}
		t.Log("B_P4_001=REPRODUCED_WITH_FRESH_RUNTIME_SESSION")
		return
	}
	t.Log("ACCOUNT_READ=FAIL classification=account_unavailable")
}

type liveContextTransport struct {
	session infrastructure.CodexExecutorSession
}

func (l liveContextTransport) Write(ctx context.Context, b []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	stop := context.AfterFunc(ctx, func() { l.session.Close() })
	defer stop()
	return l.session.Write(b)
}
func (l liveContextTransport) Read(ctx context.Context) ([]byte, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	stop := context.AfterFunc(ctx, func() { l.session.Close() })
	defer stop()
	return l.session.Read()
}

func initializeLiveSession(ctx context.Context, s infrastructure.CodexExecutorSession) error {
	if s == nil {
		return fmt.Errorf("process unavailable")
	}
	request, _ := infrastructure.EncodeCodexInitialize(infrastructure.CodexIntegerID(1), infrastructure.CodexClientInfo{Name: "dev-orchestrator", Version: "0.1.0"})
	tr := liveContextTransport{s}
	if tr.Write(ctx, request) != nil {
		return fmt.Errorf("initialize failed")
	}
	raw, err := tr.Read(ctx)
	defer clear(raw)
	if err != nil {
		return fmt.Errorf("initialize failed")
	}
	_, err = infrastructure.DecodeCodexMessage(raw, &infrastructure.CodexResponseExpectation{ID: infrastructure.CodexIntegerID(1), Method: infrastructure.CodexInitialize})
	if err != nil {
		return fmt.Errorf("initialize failed")
	}
	return tr.Write(ctx, []byte(`{"method":"initialized"}`))
}

func TestDeviceCodeLivePolicy(t *testing.T) {
	for _, value := range []string{"", "0", "true", "yes", " 1"} {
		if liveEnabled(value) {
			t.Fatal("implicit opt-in accepted")
		}
	}
	if !liveEnabled("1") {
		t.Fatal("explicit opt-in rejected")
	}
	for _, hosts := range []string{"", "chatgpt.com", "auth.openai.com", "auth.openai.com,chatgpt.com,third.invalid", "*.openai.com,chatgpt.com", "auth.openai.com,auth.openai.com", "auth.openai.com:443,chatgpt.com"} {
		if validLiveHosts(hosts) {
			t.Fatal("unapproved egress accepted")
		}
	}
	if !validLiveHosts("auth.openai.com,chatgpt.com") || !validLiveHosts("chatgpt.com,auth.openai.com") {
		t.Fatal("approved egress rejected")
	}
	for _, url := range []string{"http://auth.openai.com/codex/device", "https://auth.openai.com.evil/codex/device", "https://user@auth.openai.com/codex/device", "https://auth.openai.com:443/codex/device", "https://auth.openai.com/codex/device?secret=x", "https://chatgpt.com/codex/device", "https://auth.openai.com/codex/device#x"} {
		if validLivePresentation(url, "ABCD-1234") {
			t.Fatal("unexpected URL accepted")
		}
	}
	if !validLivePresentation("https://auth.openai.com/codex/device", "ABCD-1234") || validLivePresentation("https://auth.openai.com/codex/device", "SECRET\nRAW") {
		t.Fatal("presentation boundary invalid")
	}
}

func TestDeviceCodeLiveRedaction(t *testing.T) {
	b := NewDeviceCodeBootstrap(session(attemptResponse, completed), time.Minute)
	a, err := b.Start(context.Background())
	if err != nil {
		t.Fatal("fake login start failed")
	}
	if _, err = b.Wait(context.Background()); err != nil {
		t.Fatal("fake completion failed")
	}
	for _, v := range []any{a, b} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), "SECRET") || strings.Contains(fmt.Sprintf("%#v", v), "SECRET") {
			t.Fatal("redaction failed")
		}
	}
	if b.attempt != (LoginAttempt{}) {
		t.Fatal("completed code retained")
	}
}

func TestDeviceCodeLiveLoginClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if liveLoginFailure(ctx, time.Time{}) != "CANCELLED" || liveLoginFailure(context.Background(), time.Now().Add(-time.Second)) != "TIMEOUT" || liveLoginFailure(context.Background(), time.Time{}) != "FAIL" {
		t.Fatal("unexpected login classification")
	}
}

var _ DeviceCodeTransport = liveContextTransport{}
