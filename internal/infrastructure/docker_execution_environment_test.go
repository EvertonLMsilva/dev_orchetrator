package infrastructure

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"dev-orchestrator/internal/ports"
)

type authenticatedTLSResult struct{ proxy, handshake, chain, hostname string }

func (r authenticatedTLSResult) lines() string {
	if r.proxy != "PASS" {
		r.proxy = "FAIL"
	}
	if r.handshake != "PASS" {
		r.handshake = "FAIL"
	}
	if r.chain != "PASS" && r.chain != "FAIL" {
		r.chain = "UNKNOWN"
	}
	if r.hostname != "PASS" && r.hostname != "FAIL" {
		r.hostname = "UNKNOWN"
	}
	return "PROXY_CONNECT=" + r.proxy + "\nTLS_HANDSHAKE=" + r.handshake + "\nCERTIFICATE_CHAIN_VALID=" + r.chain + "\nCERTIFICATE_HOSTNAME_VALID=" + r.hostname + "\n"
}

// Test-only: no HTTP application bytes are sent after CONNECT. A nil root pool
// uses the workload's system trust store; fixture roots are confined to tests.
func authenticatedTLSProbe(ctx context.Context, host string, dial func(context.Context) (net.Conn, error), roots *x509.CertPool) authenticatedTLSResult {
	r := authenticatedTLSResult{"FAIL", "FAIL", "UNKNOWN", "UNKNOWN"}
	if host != "auth.openai.com" {
		return r
	}
	c, err := dial(ctx)
	if err != nil {
		return r
	}
	defer c.Close()
	if deadline, ok := ctx.Deadline(); ok {
		c.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	if _, err = io.WriteString(c, "CONNECT auth.openai.com:443 HTTP/1.1\r\nHost: auth.openai.com:443\r\n\r\n"); err != nil {
		return r
	}
	reader := bufio.NewReader(io.LimitReader(c, 8192))
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || response.StatusCode != 200 || reader.Buffered() != 0 {
		return r
	}
	r.proxy = "PASS"
	tunnel := tls.Client(c, &tls.Config{ServerName: "auth.openai.com", RootCAs: roots, MinVersion: tls.VersionTLS12})
	if err = tunnel.HandshakeContext(ctx); err != nil {
		var verification *tls.CertificateVerificationError
		if errors.As(err, &verification) {
			var hostname x509.HostnameError
			if errors.As(verification.Err, &hostname) {
				r.hostname = "FAIL"
			} else {
				r.chain = "FAIL"
			}
		}
		return r
	}
	r.handshake = "PASS"
	r.chain = "PASS"
	r.hostname = "PASS"
	return r
}

func TestAuthenticatedTLSProbeOffline(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal("fixture failed")
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"auth.openai.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal("fixture failed")
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	wrongTemplate := *template
	wrongTemplate.DNSNames = []string{"wrong.invalid"}
	wrongDER, err := x509.CreateCertificate(rand.Reader, &wrongTemplate, &wrongTemplate, pub, key)
	if err != nil {
		t.Fatal("fixture failed")
	}
	for _, tc := range []struct {
		name, reply, host string
		roots             bool
		want              authenticatedTLSResult
	}{
		{"connect-denied", "HTTP/1.1 403 Forbidden\r\n\r\n", "auth.openai.com", false, authenticatedTLSResult{"FAIL", "FAIL", "UNKNOWN", "UNKNOWN"}},
		{"tls-failure", "HTTP/1.1 200 Connection established\r\n\r\n", "auth.openai.com", false, authenticatedTLSResult{"PASS", "FAIL", "FAIL", "UNKNOWN"}},
		{"hostname-failure", "HTTP/1.1 200 Connection established\r\n\r\n", "auth.openai.com", true, authenticatedTLSResult{"PASS", "FAIL", "UNKNOWN", "FAIL"}},
		{"success", "HTTP/1.1 200 Connection established\r\n\r\n", "auth.openai.com", true, authenticatedTLSResult{"PASS", "PASS", "PASS", "PASS"}},
		{"tls-disconnected", "HTTP/1.1 200 Connection established\r\n\r\n", "auth.openai.com", true, authenticatedTLSResult{"PASS", "FAIL", "UNKNOWN", "UNKNOWN"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certificate := certificate
			if tc.name == "hostname-failure" {
				certificate.Certificate = [][]byte{wrongDER}
			}
			client, server := net.Pipe()
			done := make(chan bool, 1)
			go func() {
				defer server.Close()
				server.SetDeadline(time.Now().Add(time.Second))
				br := bufio.NewReader(server)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != "CONNECT" || req.Host != "auth.openai.com:443" {
					done <- false
					return
				}
				io.WriteString(server, tc.reply)
				if tc.name != "connect-denied" && tc.name != "tls-disconnected" {
					tunnel := tls.Server(server, &tls.Config{Certificates: []tls.Certificate{certificate}})
					if err := tunnel.Handshake(); err == nil {
						// The successful client must close without HTTP/application data.
						var application [1]byte
						if n, err := tunnel.Read(application[:]); n != 0 || err != io.EOF {
							done <- false
							return
						}
					}
				}
				done <- true
			}()
			var roots *x509.CertPool
			if tc.roots {
				roots = x509.NewCertPool()
				leaf, _ := x509.ParseCertificate(certificate.Certificate[0])
				roots.AddCert(leaf)
			} else {
				roots = x509.NewCertPool()
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			r := authenticatedTLSProbe(ctx, tc.host, func(context.Context) (net.Conn, error) { return client, nil }, roots)
			if r != tc.want || !<-done {
				t.Fatal("incorrect sanitized TLS classification")
			}
		})
	}
	called := false
	r := authenticatedTLSProbe(context.Background(), "third.invalid", func(context.Context) (net.Conn, error) { called = true; return nil, errors.New("SECRET") }, nil)
	if called || strings.Contains(r.lines(), "SECRET") {
		t.Fatal("policy or redaction failure")
	}
	if strings.Contains((authenticatedTLSResult{"SECRET", "SECRET", "SECRET", "SECRET"}).lines(), "SECRET") {
		t.Fatal("unallowlisted diagnostic")
	}
}

// Runs only inside the runtime image with network disabled. No handshake or
// authentication occurs; parsing real roots proves more than file existence.
func TestAuthenticatedSystemRootsImageOffline(t *testing.T) {
	if os.Getenv("P6_SYSTEM_ROOTS_IMAGE_OFFLINE") != "1" {
		t.Skip("runtime image offline check required")
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil || len(pool.Subjects()) == 0 {
		t.Fatal("system trust roots unavailable")
	}
	bundle, err := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	parsed := x509.NewCertPool()
	if err != nil || !parsed.AppendCertsFromPEM(bundle) || len(parsed.Subjects()) == 0 {
		t.Fatal("Debian system CA bundle unusable")
	}
}

func TestAuthenticatedTLSWorkloadHelper(t *testing.T) {
	if os.Getenv("P6_TLS_WORKLOAD_HELPER") != "1" {
		t.Skip("internal workload helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "codex-egress:8888")
	}
	if os.Getenv("HTTPS_PROXY") != codexProxyURL {
		fmt.Print((authenticatedTLSResult{"FAIL", "FAIL", "UNKNOWN", "UNKNOWN"}).lines())
		os.Exit(0)
	}
	fmt.Print(authenticatedTLSProbe(ctx, "auth.openai.com", dial, nil).lines())
	os.Exit(0)
}

func TestAuthenticatedTLSLiveOptIn(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_CODEX_TLS_LIVE") != "1" {
		t.Skip("explicit TLS diagnostic opt-in required")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("Linux required")
	}
	c, err := NewRuntimeHomeDockerContainer(os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOSTS"))
	if err != nil {
		t.Fatal("approved hosts required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	defer c.Destroy(context.Background())
	var empty bytes.Buffer
	tw := tar.NewWriter(&empty)
	tw.Close()
	if c.Prepare(ctx, &empty) != nil {
		t.Fatal("workload preparation failed")
	}
	// Copy this test executable into the owned workload, never desktop material.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("probe executable unavailable")
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal("probe executable unavailable")
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	writer.WriteHeader(&tar.Header{Name: "p6-tls-probe", Mode: 0700, Size: int64(len(data))})
	writer.Write(data)
	writer.Close()
	if _, err = c.fixedExec(ctx, []string{"/bin/tar", "-xf", "-", "-C", "/tmp"}, archive.Bytes(), false); err != nil {
		t.Fatal("probe preparation failed")
	}
	output, err := c.fixedExec(ctx, []string{"/bin/sh", "-c", "HTTPS_PROXY=http://codex-egress:8888 P6_TLS_WORKLOAD_HELPER=1 /tmp/p6-tls-probe -test.run '^TestAuthenticatedTLSWorkloadHelper$'"}, nil, true)
	if err != nil {
		t.Fatal("probe execution failed")
	}
	// Reject all unexpected output without printing it.
	valid := false
	for _, p := range []string{"PASS", "FAIL"} {
		for _, h := range []string{"PASS", "FAIL"} {
			for _, ch := range []string{"PASS", "FAIL", "UNKNOWN"} {
				for _, hn := range []string{"PASS", "FAIL", "UNKNOWN"} {
					if string(output) == (authenticatedTLSResult{p, h, ch, hn}).lines() {
						valid = true
					}
				}
			}
		}
	}
	if !valid {
		t.Fatal("probe output rejected")
	}
	fmt.Print(string(output))
	if !strings.Contains(string(output), "TLS_HANDSHAKE=PASS\n") {
		t.Fail()
	}
}

func TestRuntimeHomeDockerFailClosedPolicy(t *testing.T) {
	for _, hosts := range []string{"", "chatgpt.com", "auth.openai.com", "auth.openai.com,chatgpt.com,third.invalid", "*.openai.com,chatgpt.com"} {
		if c, err := NewRuntimeHomeDockerContainer(hosts); err == nil || c != nil {
			t.Fatal("unapproved hosts accepted")
		}
	}
	opts := runtimeHomeCreateOptions("owned-private")
	if opts.Config.Image != "dev-orchestrator-codex-runtime:0.159.2" || opts.HostConfig.NetworkMode != "owned-private" || opts.HostConfig.Privileged || opts.HostConfig.LogConfig.Type != "none" {
		t.Fatal("unsafe runtime isolation")
	}
	if len(opts.HostConfig.Mounts) != 1 || opts.HostConfig.Mounts[0].Type != mount.TypeTmpfs || opts.HostConfig.Mounts[0].Target != "/run/codex-auth" || opts.HostConfig.Mounts[0].TmpfsOptions.Mode != 0700 {
		t.Fatal("runtime home mounted outside private tmpfs")
	}
	if !reflect.DeepEqual(opts.HostConfig.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(opts.HostConfig.SecurityOpt, []string{"no-new-privileges:true"}) || len(opts.Config.Env) != 0 {
		t.Fatal("unsafe capabilities or credentials environment")
	}
}

func TestRuntimeHomeSafeNetworkDiagnostics(t *testing.T) {
	for _, tc := range []struct{ logs, want string }{
		{"CONNECT_HOST auth.openai.com 443\n", "hostname=auth.openai.com port=443 policy_decision=ALLOW dns_resolution=UNKNOWN upstream_connect=UNKNOWN"},
		{"CONNECT_STATE auth.openai.com 443 SUCCESS FAIL NO\n", "dns_resolution=SUCCESS upstream_connect=FAIL"},
		{"CONNECT_STATE auth.openai.com 443 FAIL UNKNOWN NO\n", "dns_resolution=FAIL upstream_connect=UNKNOWN"},
		{"CONNECT_HOST third.invalid 443\n", "hostname=third.invalid port=443 policy_decision=DENY"},
		{"CONNECT_HOST auth.openai.com@SECRET 443\nCONNECT_HOST 127.0.0.1 443\nRAW_SECRET_BODY\n", "requested_hostname=UNKNOWN"},
		{"CONNECT_STATE auth.openai.com 443 SECRET SECRET SECRET\n", "requested_hostname=UNKNOWN"},
		{"CONNECT_HOST auth.openai.com 99999\n", "requested_hostname=UNKNOWN"},
	} {
		lines := strings.Join(runtimeHomeNetworkLines(safeProxyDiagnostics(tc.logs, []string{"auth.openai.com", "chatgpt.com"})), "\n")
		if !strings.Contains(lines, tc.want) || strings.Contains(lines, "SECRET") || strings.Contains(lines, "RAW") {
			t.Fatal("unsafe network diagnostics")
		}
	}
	lines := strings.Join(runtimeHomeNetworkLines([]codexProxyDiagnostic{{Host: "SECRET@invalid", Port: 443, Decision: "ALLOW", DNSResolution: "SECRET"}}), "\n")
	if strings.Contains(lines, "SECRET") || !strings.Contains(lines, "result=UNKNOWN") {
		t.Fatal("unknown network data not closed")
	}
}

func TestRuntimeHomeSafeStageDiagnostics(t *testing.T) {
	c := &RuntimeHomeDockerContainer{containerStartResult: "PASS", appServerStartResult: "SECRET"}
	lines := strings.Join(c.StageDiagnosticLines(), "\n")
	if !strings.Contains(lines, "stage=CONTAINER_START result=PASS") || !strings.Contains(lines, "stage=APP_SERVER_START result=UNKNOWN") || strings.Contains(lines, "SECRET") {
		t.Fatal("unsafe infrastructure stage diagnostic")
	}
	lines = strings.Join(c.NetworkDiagnosticLines(context.Background()), "\n")
	if !strings.Contains(lines, "requested_hostname=UNKNOWN") || strings.Contains(lines, "result=PASS") {
		t.Fatal("unobserved proxy success")
	}
}

type liveAccountSession struct{ request []byte }

type diagnosticAccountSession struct{ wire string }

func (*diagnosticAccountSession) Write([]byte) error { return nil }
func (s *diagnosticAccountSession) Read() ([]byte, error) {
	if s.wire == "" {
		return nil, errors.New("SECRET_TRANSPORT")
	}
	return []byte(s.wire), nil
}
func (*diagnosticAccountSession) Close() error { return nil }

func TestRuntimeHomeAccountStructuralDiagnostics(t *testing.T) {
	unknown := RuntimeHomeAccountDiagnostics{classification: "SECRET", accountType: "SECRET", rpc: "SECRET"}
	if lines := strings.Join(unknown.Lines(), "\n"); strings.Contains(lines, "SECRET") || !strings.Contains(lines, "classification=ACCOUNT_READ_LOCAL_FAILURE") || !strings.Contains(lines, "ACCOUNT_READ_RPC=FAIL") {
		t.Fatal("unknown diagnostic did not fail closed")
	}
	for _, tc := range []struct{ wire, classification, signal string }{
		{"", "ACCOUNT_READ_TRANSPORT_FAILURE", "ACCOUNT_READ_RPC=FAIL"},
		{`{"id":99,"error":{"code":-32603,"message":"SECRET","data":{"token":"SECRET"}}}`, "ACCOUNT_READ_RPC_ERROR", "RPC_CODE=-32603"},
		{`{"id":99,"error":{"code":-32603,"message":"workspace routing discovery failed"}}`, "WORKSPACE_ROUTING_FAILURE", "WORKSPACE_ROUTING=FAIL"},
		{`{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`, "ACCOUNT_READ_RESULT_ACCOUNT_NONE", "ACCOUNT_PRESENT=no"},
		{`{"id":98,"result":{"account":null,"requiresOpenaiAuth":true}}`, "ACCOUNT_READ_ID_MISMATCH", "RESPONSE_CORRELATED=no"},
		{`{"id":99,"result":{"account":{"type":"SECRET","email":"SECRET","planType":"SECRET"},"requiresOpenaiAuth":true}}`, "ACCOUNT_READ_RESULT_UNEXPECTED", "ACCOUNT_PRESENT=yes"},
	} {
		d := ReadRuntimeHomeAccountDiagnostics(context.Background(), &diagnosticAccountSession{tc.wire})
		lines := strings.Join(d.Lines(), "\n")
		if !strings.Contains(lines, "classification="+tc.classification) || !strings.Contains(lines, tc.signal) || strings.Contains(lines, "SECRET") {
			t.Fatal("unsafe or incorrect account diagnostic")
		}
		if strings.Contains(fmt.Sprintf("%#v", d), "SECRET") {
			t.Fatal("format leak")
		}
	}
	d := ReadRuntimeHomeAccountDiagnostics(context.Background(), &liveAccountSession{})
	if !strings.Contains(strings.Join(d.Lines(), "\n"), "ACCOUNT_TYPE=chatgpt") {
		t.Fatal("valid account projection failed")
	}
}

func (s *liveAccountSession) Write(b []byte) error { s.request = append([]byte(nil), b...); return nil }
func (s *liveAccountSession) Read() ([]byte, error) {
	var req struct {
		ID any `json:"id"`
	}
	json.Unmarshal(s.request, &req)
	return json.Marshal(map[string]any{"id": req.ID, "result": map[string]any{"account": map[string]any{"type": "chatgpt", "email": "fake@example.invalid", "planType": "plus"}, "requiresOpenaiAuth": true}})
}
func (*liveAccountSession) Close() error { return nil }
func TestRuntimeHomeAccountReadDoesNotRefreshOrInfer(t *testing.T) {
	s := &liveAccountSession{}
	kind, _ := ReadRuntimeHomeAccount(context.Background(), s)
	var req struct {
		Method string
		Params map[string]any
	}
	if json.Unmarshal(s.request, &req) != nil || req.Method != "account/read" || req.Params["refreshToken"] != false || len(req.Params) != 1 {
		t.Fatal("unexpected account RPC")
	}
	if kind != "pass" {
		t.Fatal("valid runtime account rejected")
	}
}

type deterministicAccountSession struct {
	wires             []string
	readErr, writeErr error
	written           bool
	closed            chan struct{}
}

func TestAccountReadOfficialContract(t *testing.T) {
	const account = `"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true`
	for _, tc := range []struct{ name, result, decode string }{
		{"official_shape", `{` + account + `}`, "PASS"},
		{"workspace_absent", `{` + account + `}`, "PASS"},
		{"workspace_null", `{` + account + `,"workspaceRouting":null}`, "PASS"},
		{"workspace_valid", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","backendOrigin":"https://example.invalid","accountRoutingOverride":"NO_CONSTRAINT"}}`, "PASS"},
		{"enum_us", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","backendOrigin":"https://example.invalid","accountRoutingOverride":"us"}}`, "PASS"},
		{"enum_us_cr", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","backendOrigin":"https://example.invalid","accountRoutingOverride":"us_cr"}}`, "PASS"},
		{"invalid_enum", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","backendOrigin":"synthetic","accountRoutingOverride":"SECRET_INVALID"}}`, "FAIL"},
		{"wrong_workspace_type", `{` + account + `,"workspaceRouting":false}`, "FAIL"},
		{"missing_nested_id", `{` + account + `,"workspaceRouting":{"backendOrigin":"synthetic","accountRoutingOverride":"us"}}`, "FAIL"},
		{"missing_nested_origin", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","accountRoutingOverride":"us"}}`, "FAIL"},
		{"missing_nested_enum", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","backendOrigin":"synthetic"}}`, "FAIL"},
		{"unknown_nested", `{` + account + `,"workspaceRouting":{"chatgptAccountId":"synthetic","backendOrigin":"synthetic","accountRoutingOverride":"us","SECRET_FIELD":true}}`, "FAIL"},
		{"wrong_nested_type", `{` + account + `,"workspaceRouting":{"chatgptAccountId":7,"backendOrigin":"synthetic","accountRoutingOverride":"us"}}`, "FAIL"},
		{"unknown_result_field", `{` + account + `,"SECRET_FIELD":true}`, "FAIL"},
		{"missing_required_email", `{"account":{"type":"chatgpt","planType":"plus"},"requiresOpenaiAuth":true}`, "FAIL"},
		{"missing_required_discriminator", `{"account":{"email":null,"planType":"plus"},"requiresOpenaiAuth":true}`, "FAIL"},
		{"unknown_account_field", `{"account":{"type":"chatgpt","email":null,"planType":"plus","SECRET_FIELD":true},"requiresOpenaiAuth":true}`, "FAIL"},
		{"wrong_plan_type", `{"account":{"type":"chatgpt","email":null,"planType":7},"requiresOpenaiAuth":true}`, "FAIL"},
		{"wrong_auth_type", `{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":"SECRET"}`, "FAIL"},
		{"missing_required_plan", `{"account":{"type":"chatgpt","email":null},"requiresOpenaiAuth":true}`, "FAIL"},
		{"missing_required_auth", `{"account":{"type":"chatgpt","email":null,"planType":"plus"}}`, "FAIL"},
		{"wrong_discriminator", `{"account":{"type":"SECRET_INVALID","email":null,"planType":"plus"},"requiresOpenaiAuth":true}`, "FAIL"},
		{"wrong_field_type", `{"account":{"type":"chatgpt","email":7,"planType":"plus"},"requiresOpenaiAuth":true}`, "FAIL"},
		{"invalid_plan_enum", `{"account":{"type":"chatgpt","email":null,"planType":"SECRET_INVALID"},"requiresOpenaiAuth":true}`, "FAIL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := ReadRuntimeHomeAccountDiagnostics(context.Background(), &diagnosticAccountSession{`{"id":99,"result":` + tc.result + `}`})
			lines := strings.Join(d.Lines(), "\n")
			if !strings.Contains(lines, "ACCOUNT_READ_RPC=PASS") || !strings.Contains(lines, "RESULT_DECODE="+tc.decode) || strings.Contains(lines, "SECRET") || strings.Contains(lines, "synthetic") {
				t.Fatal("contract or sanitized semantics incorrect")
			}
			kind, _ := d.LegacyResult()
			if (kind == "pass") != (tc.decode == "PASS") {
				t.Fatal("contract accepted invalid result or rejected official result")
			}
		})
	}
	t.Run("rpc_error_semantics", func(t *testing.T) {
		d := ReadRuntimeHomeAccountDiagnostics(context.Background(), &diagnosticAccountSession{`{"id":99,"error":{"code":-32603,"message":"SECRET_ERROR"}}`})
		lines := strings.Join(d.Lines(), "\n")
		if !strings.Contains(lines, "ACCOUNT_READ_RPC=FAIL") || !strings.Contains(lines, "RESULT_DECODE=UNKNOWN") || !strings.Contains(lines, "RPC_CODE=-32603") || strings.Contains(lines, "SECRET") {
			t.Fatal("RPC failure semantics incorrect")
		}
	})
}

func (s *deterministicAccountSession) Write([]byte) error {
	s.written = s.writeErr == nil
	return s.writeErr
}
func (s *deterministicAccountSession) Read() ([]byte, error) {
	if len(s.wires) > 0 {
		wire := s.wires[0]
		s.wires = s.wires[1:]
		return []byte(wire), nil
	}
	if s.closed != nil {
		<-s.closed
		return nil, io.EOF
	}
	return nil, s.readErr
}
func (s *deterministicAccountSession) Close() error {
	if s.closed != nil {
		close(s.closed)
	}
	return nil
}

func TestAccountReadDeterministicDiagnostics(t *testing.T) {
	success := `{"id":99,"result":{"account":{"type":"chatgpt","email":"SECRET_EMAIL","planType":"plus"},"requiresOpenaiAuth":true}}`
	notification := `{"method":"account/updated","params":{"SECRET":"SECRET_TOKEN"}}`
	for _, tc := range []struct {
		name                            string
		wires                           []string
		readErr                         error
		rpc, classification, correlated string
	}{
		{"correlated_success", []string{success}, nil, "PASS", "PASS", "yes"},
		{"rpc_error", []string{`{"id":99,"error":{"code":-32603,"message":"SECRET_ERROR","data":"SECRET_DATA"}}`}, nil, "FAIL", "ACCOUNT_READ_RPC_ERROR", "yes"},
		{"eof", nil, io.EOF, "FAIL", "ACCOUNT_READ_EOF", "no"},
		{"timeout", nil, context.DeadlineExceeded, "FAIL", "ACCOUNT_READ_TIMEOUT", "no"},
		{"malformed", []string{`{"SECRET_BODY"`}, nil, "FAIL", "ACCOUNT_READ_ENVELOPE_INVALID", "no"},
		{"notification", []string{notification, success}, nil, "PASS", "PASS", "yes"},
		{"multiple_notifications", []string{notification, notification, success}, nil, "PASS", "PASS", "yes"},
		{"notification_then_eof", []string{notification}, io.EOF, "FAIL", "ACCOUNT_READ_EOF", "no"},
		{"mismatched_id", []string{`{"id":98,"result":{"account":null,"requiresOpenaiAuth":true}}`}, nil, "FAIL", "ACCOUNT_READ_ID_MISMATCH", "no"},
		{"server_request", []string{`{"id":7,"method":"SECRET_METHOD","params":{"token":"SECRET"}}`}, nil, "FAIL", "ACCOUNT_READ_SERVER_REQUEST", "no"},
		{"account_absent", []string{`{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`}, nil, "PASS", "ACCOUNT_READ_RESULT_ACCOUNT_NONE", "yes"},
		{"unexpected_account_type", []string{`{"id":99,"result":{"account":{"type":"SECRET","email":"SECRET","planType":"SECRET"},"requiresOpenaiAuth":true}}`}, nil, "PASS", "ACCOUNT_READ_RESULT_UNEXPECTED", "yes"},
		{"workspace_routing", []string{`{"id":99,"error":{"code":-32603,"message":"workspace routing discovery failed"}}`}, nil, "FAIL", "WORKSPACE_ROUTING_FAILURE", "yes"},
		{"result_decode", []string{`{"id":99,"result":{"SECRET_FIELD":"SECRET_VALUE"}}`}, nil, "PASS", "ACCOUNT_READ_RESULT_UNEXPECTED", "yes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &deterministicAccountSession{wires: tc.wires, readErr: tc.readErr}
			d := ReadRuntimeHomeAccountDiagnostics(context.Background(), s)
			lines := strings.Join(d.Lines(), "\n")
			for _, expected := range []string{"ACCOUNT_READ_REQUEST=PASS", "ACCOUNT_READ_RPC=" + tc.rpc, "classification=" + tc.classification, "RESPONSE_CORRELATED=" + tc.correlated} {
				if !strings.Contains(lines, expected) {
					t.Fatal("incorrect deterministic diagnostic")
				}
			}
			if !s.written || strings.Contains(lines, "=unknown") || strings.Contains(lines, "ACCOUNT_READ_RPC=UNKNOWN") || strings.Contains(lines, "SECRET") || strings.Contains(fmt.Sprintf("%#v", d), "SECRET") {
				t.Fatal("non-deterministic or unsafe diagnostic")
			}
		})
	}
	t.Run("blocked_read_timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		s := &deterministicAccountSession{closed: make(chan struct{})}
		d := ReadRuntimeHomeAccountDiagnostics(ctx, s)
		if lines := strings.Join(d.Lines(), "\n"); !strings.Contains(lines, "classification=ACCOUNT_READ_TIMEOUT") || !strings.Contains(lines, "ACCOUNT_READ_REQUEST=PASS") {
			t.Fatal("blocked read did not terminate safely")
		}
	})
	t.Run("write_failure", func(t *testing.T) {
		d := ReadRuntimeHomeAccountDiagnostics(context.Background(), &deterministicAccountSession{writeErr: errors.New("SECRET_WRITE")})
		lines := strings.Join(d.Lines(), "\n")
		if !strings.Contains(lines, "ACCOUNT_READ_REQUEST=FAIL") || !strings.Contains(lines, "ACCOUNT_READ_RPC=FAIL") || strings.Contains(lines, "SECRET") {
			t.Fatal("write failure not closed")
		}
	})
}

func TestRuntimeHomeDockerCapturePreservesUntilDestroy(t *testing.T) {
	var captured, removed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/container/exec"):
			if removed.Load() {
				t.Error("container destroyed before capture")
			}
			var opts client.ExecCreateOptions
			if json.NewDecoder(r.Body).Decode(&opts) != nil || len(opts.Env) != 0 || opts.AttachStdin || !opts.AttachStdout || !opts.AttachStderr {
				t.Error("unsafe capture options")
			}
			if len(opts.Cmd) != 3 || !strings.Contains(opts.Cmd[2], "test ! -L auth.json") {
				t.Error("symlink guard missing")
			}
			fmt.Fprint(w, `{"Id":"capture"}`)
		case strings.HasSuffix(r.URL.Path, "/exec/capture/start"):
			io.Copy(io.Discard, r.Body)
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			fmt.Fprint(rw, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			rw.Write(codexDockerFrame(1, "controlled capture archive"))
			rw.Flush()
		case strings.HasSuffix(r.URL.Path, "/exec/capture/json"):
			captured.Store(true)
			fmt.Fprint(w, `{"Running":false,"ExitCode":0}`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/container"):
			if !captured.Load() {
				t.Error("removed before capture finished")
			}
			removed.Store(true)
			w.WriteHeader(204)
		default:
			t.Error("unexpected container lifecycle operation")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	sdk, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	d := &DockerDriver{client: sdk}
	reader, writer := io.Pipe()
	defer writer.Close()
	processDriver := leaseProcessDocker{&fakeCodexProcessDocker{output: reader}}
	process, err := startLeaseCodexProcessRuntime(context.Background(), processDriver, "container")
	if err != nil {
		t.Fatal(err)
	}
	c := &RuntimeHomeDockerContainer{driver: d, egress: &ownedCodexEgress{DockerDriver: d}, id: "container", process: process}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if process.Close() != nil || removed.Load() {
		t.Fatal("transport removed container")
	}
	stream, err := c.Capture(ctx)
	if err != nil {
		t.Fatal("capture failed after transport close")
	}
	data, err := io.ReadAll(stream)
	stream.Close()
	if err != nil || !bytes.Equal(data, []byte("controlled capture archive")) || removed.Load() {
		t.Fatal("capture sequence invalid")
	}
	if c.Destroy(ctx) != nil || c.Destroy(ctx) != nil || !removed.Load() {
		t.Fatal("final destruction failed")
	}
}

type recordingDocker struct {
	config                                  DockerEnvironmentConfig
	calls                                   []string
	createErr, startErr, stopErr, removeErr error
	cleanupCanceled                         bool
	partialCreate, emptyID                  bool
}

func (d *recordingDocker) Create(_ context.Context, config DockerEnvironmentConfig) (string, error) {
	d.calls = append(d.calls, "create")
	d.config = config
	if d.partialCreate {
		return "container-id", d.createErr
	}
	if d.emptyID {
		return "", nil
	}
	if d.createErr != nil {
		return "", d.createErr
	}
	return "container-id", nil
}
func (d *recordingDocker) Start(_ context.Context, id string) error {
	if id != "container-id" {
		panic("unexpected container")
	}
	d.calls = append(d.calls, "start")
	return d.startErr
}
func (d *recordingDocker) Stop(ctx context.Context, id string) error {
	if id != "container-id" {
		panic("unexpected container")
	}
	d.cleanupCanceled = d.cleanupCanceled || ctx.Err() != nil
	d.calls = append(d.calls, "stop")
	return d.stopErr
}
func (d *recordingDocker) Remove(ctx context.Context, id string) error {
	if id != "container-id" {
		panic("unexpected container")
	}
	d.cleanupCanceled = d.cleanupCanceled || ctx.Err() != nil
	d.calls = append(d.calls, "remove")
	return d.removeErr
}

func TestDockerEnvironmentIsolationAndSuccessCleanup(t *testing.T) {
	d := &recordingDocker{}
	err := NewDockerExecutionEnvironment(d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{
		Workspace: "/trusted/project", Objective: "ignored", Scope: []string{"/var/run/docker.sock"},
		Constraints: []string{"privileged=true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.config.WorkspaceSource() != "/trusted/project" || d.config.WorkspaceTarget() != "/workspace" || d.config.WorkingDirectory() != "/workspace" || d.config.Privileged() {
		t.Fatalf("unsafe configuration: %+v", d.config)
	}
	// Configuration has a private workspace source and tmpfs flag, with no fields for additional mounts,
	// commands or privileges. Its exported API exposes only read access.
	typ := reflect.TypeOf(d.config)
	if typ.NumField() != 2 || typ.Field(0).IsExported() || typ.Field(1).IsExported() || typ.Field(1).Type.Kind() != reflect.Bool || d.config.AuthTmpfsTarget() != "" {
		t.Fatal("configuration exposes caller controls")
	}
	if !reflect.DeepEqual(d.calls, []string{"create", "start", "stop", "remove"}) {
		t.Fatal(d.calls)
	}
}

func TestDockerEnvironmentFailures(t *testing.T) {
	createErr := errors.New("create failed")
	startErr := errors.New("start failed")
	stopErr := errors.New("stop failed")
	removeErr := errors.New("remove failed")
	for _, tt := range []struct {
		name  string
		d     recordingDocker
		calls []string
		want  []error
	}{
		{"create", recordingDocker{createErr: createErr}, []string{"create"}, []error{createErr}},
		{"partial create", recordingDocker{partialCreate: true, createErr: createErr, removeErr: removeErr}, []string{"create", "remove"}, []error{createErr, removeErr}},
		{"start", recordingDocker{startErr: startErr}, []string{"create", "start", "remove"}, []error{startErr}},
		{"start and remove", recordingDocker{startErr: startErr, removeErr: removeErr}, []string{"create", "start", "remove"}, []error{startErr, removeErr}},
		{"stop", recordingDocker{stopErr: stopErr}, []string{"create", "start", "stop", "remove"}, []error{stopErr}},
		{"remove", recordingDocker{removeErr: removeErr}, []string{"create", "start", "stop", "remove"}, []error{removeErr}},
		{"stop and remove", recordingDocker{stopErr: stopErr, removeErr: removeErr}, []string{"create", "start", "stop", "remove"}, []error{stopErr, removeErr}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := NewDockerExecutionEnvironment(&tt.d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
			for _, want := range tt.want {
				if !errors.Is(err, want) {
					t.Fatalf("error %v does not preserve %v", err, want)
				}
			}
			if !reflect.DeepEqual(tt.d.calls, tt.calls) {
				t.Fatal(tt.d.calls)
			}
		})
	}
}

func TestDockerEnvironmentRejectsInvalidWorkspace(t *testing.T) {
	for _, workspace := range []string{"", " ", "relative", "/", "/trusted/..", "/var/run/docker.sock", "/trusted/\x00project"} {
		t.Run(workspace, func(t *testing.T) {
			d := &recordingDocker{}
			err := NewDockerExecutionEnvironment(d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: workspace})
			if err == nil || len(d.calls) != 0 {
				t.Fatalf("workspace accepted: %q, calls=%v", workspace, d.calls)
			}
		})
	}
}

type cancelOnStartDocker struct {
	*recordingDocker
	cancel context.CancelFunc
}

func (d cancelOnStartDocker) Start(ctx context.Context, id string) error {
	d.cancel()
	return d.recordingDocker.Start(ctx, id)
}

func TestDockerEnvironmentCancellationStillCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &recordingDocker{startErr: context.Canceled}
	err := NewDockerExecutionEnvironment(cancelOnStartDocker{d, cancel}).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if !errors.Is(err, context.Canceled) || d.cleanupCanceled || !reflect.DeepEqual(d.calls, []string{"create", "start", "remove"}) {
		t.Fatalf("error=%v calls=%v canceled=%v", err, d.calls, d.cleanupCanceled)
	}
}

func TestDockerEnvironmentMissingDriver(t *testing.T) {
	if NewDockerExecutionEnvironment(nil).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"}) == nil {
		t.Fatal("missing driver accepted")
	}
}

func TestDockerEnvironmentEmptyContainerID(t *testing.T) {
	d := &recordingDocker{emptyID: true}
	err := NewDockerExecutionEnvironment(d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if err == nil || !reflect.DeepEqual(d.calls, []string{"create"}) {
		t.Fatalf("error=%v calls=%v", err, d.calls)
	}
}

func TestDockerEnvironmentCanceledBeforeCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &recordingDocker{}
	err := NewDockerExecutionEnvironment(d).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if !errors.Is(err, context.Canceled) || len(d.calls) != 0 {
		t.Fatalf("error=%v calls=%v", err, d.calls)
	}
}

func TestDockerEnvironmentCanceledAfterStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &recordingDocker{}
	err := NewDockerExecutionEnvironment(cancelOnStartDocker{d, cancel}).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if !errors.Is(err, context.Canceled) || d.cleanupCanceled || !reflect.DeepEqual(d.calls, []string{"create", "start", "stop", "remove"}) {
		t.Fatalf("error=%v calls=%v canceled=%v", err, d.calls, d.cleanupCanceled)
	}
}
