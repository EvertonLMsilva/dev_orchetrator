package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"dev-orchestrator/internal/ports"
)

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
