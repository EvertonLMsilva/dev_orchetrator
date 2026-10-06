package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		liveBootstrapDiagnostics(t, container, nil)
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
		liveBootstrapDiagnostics(t, container, nil)
		t.Fatal("app_server=FAIL")
	}
	bootstrap := NewDeviceCodeBootstrap(liveContextTransport{session}, 15*time.Minute)
	attempt, err := bootstrap.Start(ctx)
	liveBootstrapDiagnostics(t, container, bootstrap)
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
	accountDiagnostic := infrastructure.ReadRuntimeHomeAccountDiagnostics(ctx, session)
	for _, line := range accountDiagnostic.Lines() {
		t.Log("account_read_phase=BEFORE_CAPTURE " + line)
	}
	kind, code := accountDiagnostic.LegacyResult()
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
	restartDiagnostic := infrastructure.ReadRuntimeHomeAccountDiagnostics(ctx, nextSession)
	for _, line := range restartDiagnostic.Lines() {
		t.Log("account_read_phase=AFTER_RESTART " + line)
	}
	restartKind, _ := restartDiagnostic.LegacyResult()
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

func liveBootstrapDiagnostics(t *testing.T, c *infrastructure.RuntimeHomeDockerContainer, b *DeviceCodeBootstrap) {
	t.Helper()
	for _, line := range c.StageDiagnosticLines() {
		t.Log(line)
	}
	diagnostic := BootstrapDiagnostics{}
	if b != nil {
		diagnostic = b.Diagnostics()
	}
	for _, line := range diagnostic.Lines() {
		t.Log(line)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, line := range c.NetworkDiagnosticLines(ctx) {
		t.Log(line)
	}
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

type persistedReadFixture struct {
	memoryRuntimeHome
	responses [][]byte
	writes    []string
	stopped   bool
}

func (f *persistedReadFixture) Start(context.Context) (infrastructure.CodexExecutorSession, error) {
	return f, nil
}
func (f *persistedReadFixture) Write(b []byte) error {
	f.writes = append(f.writes, string(b))
	return nil
}
func (f *persistedReadFixture) Read() ([]byte, error) {
	if len(f.responses) == 0 {
		return nil, errors.New("SECRET_RAW_ERROR")
	}
	b := f.responses[0]
	f.responses = f.responses[1:]
	return b, nil
}
func (f *persistedReadFixture) Close() error { f.stopped = true; return nil }
func (f *persistedReadFixture) Capture(ctx context.Context) (io.ReadCloser, error) {
	if !f.stopped {
		return nil, errors.New("SECRET_NOT_STOPPED")
	}
	f.auth = []byte(strings.ReplaceAll(runtimeAuth, "SECRET_REFRESH", "SECRET_REFRESH_UPDATED"))
	return f.memoryRuntimeHome.Capture(ctx)
}

func TestPersistedAccountReadLifecycle(t *testing.T) {
	for _, tc := range []struct{ name, response, verdict string }{
		{"account", `{"id":99,"result":{"account":{"type":"chatgpt","email":"SECRET_EMAIL","planType":"plus"},"requiresOpenaiAuth":true}}`, "NOT_REPRODUCED_WITH_FRESH_RUNTIME_SESSION"},
		{"rpc", `{"id":99,"error":{"code":-32603,"message":"SECRET_RAW_ERROR"}}`, "INCONCLUSIVE"},
		{"routing", `{"id":99,"error":{"code":-32603,"message":"workspace routing discovery failed"}}`, "REPRODUCED_WITH_FRESH_RUNTIME_SESSION"},
		{"none", `{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`, "INCONCLUSIVE"},
		{"malformed", `{"id":99,"result":{"SECRET_PAYLOAD":true}}`, "INCONCLUSIVE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testRuntimeStore(t)
			if s.StoreSession(context.Background(), []byte(runtimeAuth)) != nil {
				t.Fatal("fixture failed")
			}
			f := &persistedReadFixture{responses: [][]byte{[]byte(`{"id":1,"result":{"userAgent":"fixture","codexHome":"/run/codex-auth","platformFamily":"unix","platformOs":"linux"}}`), []byte(tc.response)}}
			var lines []string
			if !runPersistedAccountRead(context.Background(), s, f, func(line string) { lines = append(lines, line) }) {
				t.Fatal("lifecycle failed")
			}
			output := strings.Join(lines, "\n")
			for _, expected := range []string{"INITIALIZE=PASS", "INITIALIZED=PASS", "AUTH_CAPTURE=PASS", "AUTH_PERSIST=PASS", "B_P4_001=" + tc.verdict} {
				if !strings.Contains(output, expected) {
					t.Fatal("missing safe diagnostic")
				}
			}
			if strings.Contains(output, "SECRET") || !f.removed || !f.stopped || len(f.writes) != 3 || f.writes[2] != `{"id":99,"method":"account/read","params":{"refreshToken":false}}` {
				t.Fatal("unsafe sequence or output")
			}
			material, err := s.read()
			defer clear(material)
			if err != nil || !strings.Contains(string(material), "SECRET_REFRESH_UPDATED") {
				t.Fatal("refresh was lost")
			}
		})
	}
}

type persistedReadContainer interface {
	RuntimeHomeContainer
	Start(context.Context) (infrastructure.CodexExecutorSession, error)
}

// Test-only composition: no bootstrap, login fallback, or desktop auth source.
func runPersistedAccountRead(ctx context.Context, store *CodexRuntimeHome, c persistedReadContainer, report func(string)) (ok bool) {
	capture, persist := "NOT_REACHED", "NOT_REACHED"
	defer func() { report("AUTH_CAPTURE=" + capture); report("AUTH_PERSIST=" + persist) }()
	lease, err := store.Materialize(ctx, c)
	if err != nil {
		report("RUNTIME_HOME=FAIL")
		return false
	}
	report("RUNTIME_HOME=PASS")
	defer func() {
		if lease.Abort(context.Background()) != nil {
			ok = false
		}
	}()
	s, err := c.Start(ctx)
	if err != nil || s == nil {
		report("APP_SERVER_START=FAIL")
		return false
	}
	report("APP_SERVER_START=PASS")
	// Capture is attempted even on initialization/RPC failure, once stop is confirmed.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if s.Close() != nil {
			ok = false
			return
		}
		if network, hasPolicy := c.(interface{ DeniedHost(context.Context) string }); hasPolicy && network.DeniedHost(cleanupCtx) != "" {
			ok = false
		}
		err := lease.Finish(cleanupCtx)
		if errors.Is(err, ErrRuntimeHomeCapture) {
			capture = "FAIL"
			ok = false
			return
		}
		capture = "PASS"
		if errors.Is(err, ErrRuntimeAuthPersistence) {
			persist = "FAIL"
			ok = false
			return
		}
		persist = "PASS"
		if err != nil {
			ok = false
		}
	}()
	request, _ := infrastructure.EncodeCodexInitialize(infrastructure.CodexIntegerID(1), infrastructure.CodexClientInfo{Name: "dev-orchestrator", Version: "0.1.0"})
	tr := liveContextTransport{s}
	if tr.Write(ctx, request) != nil {
		report("INITIALIZE=FAIL")
		return false
	}
	raw, err := tr.Read(ctx)
	defer clear(raw)
	if err == nil {
		_, err = infrastructure.DecodeCodexMessage(raw, &infrastructure.CodexResponseExpectation{ID: infrastructure.CodexIntegerID(1), Method: infrastructure.CodexInitialize})
	}
	if err != nil {
		report("INITIALIZE=FAIL")
		return false
	}
	report("INITIALIZE=PASS")
	if tr.Write(ctx, []byte(`{"method":"initialized"}`)) != nil {
		report("INITIALIZED=FAIL")
		return false
	}
	report("INITIALIZED=PASS")
	d := infrastructure.ReadRuntimeHomeAccountDiagnostics(ctx, s)
	facts := make(map[string]string)
	for _, line := range d.Lines() {
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "ACCOUNT_READ_RPC", "RESPONSE_CORRELATED", "RESULT_PRESENT", "ACCOUNT_PRESENT", "ACCOUNT_TYPE", "RPC_ERROR_PRESENT", "RPC_CODE", "WORKSPACE_ROUTING":
			facts[key] = value
			// No evidence of an RPC envelope: do not assert absence of an error.
			if key != "RPC_ERROR_PRESENT" || value != "unknown" {
				report(line)
			}
		}
	}
	verdict := "INCONCLUSIVE"
	if facts["ACCOUNT_READ_RPC"] == "PASS" && facts["ACCOUNT_PRESENT"] == "yes" {
		verdict = "NOT_REPRODUCED_WITH_FRESH_RUNTIME_SESSION"
	}
	if kind, _ := d.LegacyResult(); kind == "workspace_routing" {
		verdict = "REPRODUCED_WITH_FRESH_RUNTIME_SESSION"
	}
	report("B_P4_001=" + verdict)
	return true
}

func TestPersistedAccountReadLiveOptIn(t *testing.T) {
	if !liveEnabled(os.Getenv("DEV_ORCHESTRATOR_CODEX_PERSISTED_ACCOUNT_READ_LIVE")) {
		t.Skip("explicit persisted account-read opt-in required")
	}
	if runtime.GOOS != "linux" || !validLiveHosts(os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS")) {
		t.Fatal("Linux and exact approved hosts required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := NewCodexRuntimeHome()
	if err != nil {
		t.Fatal("RUNTIME_HOME=FAIL")
	}
	defer store.Close()
	c, err := infrastructure.NewRuntimeHomeDockerContainer(os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS"))
	if err != nil {
		t.Fatal("APP_SERVER_START=FAIL")
	}
	if !runPersistedAccountRead(ctx, store, c, func(line string) { t.Log(line) }) {
		t.Fail()
	}
}

func TestPersistedAccountReadFailurePreservesSession(t *testing.T) {
	for _, failure := range []string{"initialize", "capture", "persist", "missing"} {
		t.Run(failure, func(t *testing.T) {
			s := testRuntimeStore(t)
			if failure != "missing" && s.StoreSession(context.Background(), []byte(runtimeAuth)) != nil {
				t.Fatal("fixture failed")
			}
			f := &persistedReadFixture{responses: [][]byte{[]byte(`{"id":1,"result":{"userAgent":"fixture","codexHome":"/run/codex-auth","platformFamily":"unix","platformOs":"linux"}}`), []byte(`{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`)}}
			if failure == "initialize" {
				f.responses = nil
			}
			if failure == "capture" {
				f.fail = "capture"
			}
			if failure == "persist" {
				s.ops.write = func(*os.File, []byte) error { return errors.New("SECRET_WRITE_ERROR") }
			}
			var lines []string
			if runPersistedAccountRead(context.Background(), s, f, func(line string) { lines = append(lines, line) }) {
				t.Fatal("failure hidden")
			}
			output := strings.Join(lines, "\n")
			if strings.Contains(output, "SECRET") || !f.removed {
				t.Fatal("failure leaked or cleanup omitted")
			}
			if failure == "missing" {
				if len(f.writes) != 0 || !strings.Contains(output, "AUTH_CAPTURE=NOT_REACHED") {
					t.Fatal("missing auth triggered process or fallback")
				}
				return
			}
			material, err := s.read()
			defer clear(material)
			if err != nil {
				t.Fatal("session lost")
			}
			if failure == "initialize" {
				if !strings.Contains(string(material), "SECRET_REFRESH_UPDATED") || !strings.Contains(output, "AUTH_PERSIST=PASS") {
					t.Fatal("failed initialization lost refresh")
				}
			} else if string(material) != runtimeAuth {
				t.Fatal("failed capture/persist replaced session")
			}
			if failure == "capture" && (!strings.Contains(output, "AUTH_CAPTURE=FAIL") || !strings.Contains(output, "AUTH_PERSIST=NOT_REACHED")) {
				t.Fatal("capture diagnostics incorrect")
			}
			if failure == "persist" && (!strings.Contains(output, "AUTH_CAPTURE=PASS") || !strings.Contains(output, "AUTH_PERSIST=FAIL")) {
				t.Fatal("persistence diagnostics incorrect")
			}
		})
	}
}
