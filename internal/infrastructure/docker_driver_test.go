package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-orchestrator/internal/ports"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const codexRuntimeSmokeImage = "dev-orchestrator-codex-runtime:0.159.2"

func TestDockerStopAppServerPreservesContainer(t *testing.T) {
	for _, running := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/exec/owned-exec/json") {
				t.Error("stop/remove container forbidden for lease transport")
				w.WriteHeader(500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"Running":%t,"ExitCode":0}`, running)
		}))
		sdk, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.56"))
		if err != nil {
			t.Fatal(err)
		}
		d := &DockerDriver{client: sdk}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		err = d.stopCodexAppServer(ctx, "container", "owned-exec")
		cancel()
		if (err != nil) != running {
			t.Error("process exit not confirmed")
		}
		sdk.Close()
		server.Close()
	}
}

func TestDockerCodexSessionFixedOptions(t *testing.T) {
	for _, workspace := range []string{"/trusted/a", "/trusted/b"} {
		opts := codexSessionCreateOptions(DockerEnvironmentConfig{workspace: workspace})
		if opts.Config.Image != codexRuntimeSmokeImage || opts.Config.Image == dockerProbeImage || opts.Config.WorkingDir != "/workspace" || !reflect.DeepEqual([]string(opts.Config.Cmd), []string{"/bin/sleep", "300"}) || len(opts.Config.Env) != 0 || opts.HostConfig.Privileged || opts.HostConfig.NetworkMode != "none" || opts.Platform.OS != "linux" {
			t.Fatal("unsafe session options")
		}
		mounts := opts.HostConfig.Mounts
		if len(mounts) != 1 || mounts[0].Type != mount.TypeBind || mounts[0].Source != workspace || mounts[0].Target != "/workspace" {
			t.Fatal("workspace not bound")
		}
		opts.Config.Image = "untrusted"
		mounts[0].Target = "/"
		fresh := codexSessionCreateOptions(DockerEnvironmentConfig{workspace: workspace})
		if fresh.Config.Image != codexRuntimeSmokeImage || fresh.HostConfig.Mounts[0].Target != "/workspace" {
			t.Fatal("mutable security configuration")
		}
	}
}

// Real composition proof stops at initialize/initialized. The test runner alone
// has daemon access; the Codex container gets only its temporary workspace.
func TestDockerCodexSessionRealHandshake(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_CODEX_COMPOSITION") != "1" {
		t.Skip("opt-in unauthenticated Docker composition")
	}
	workspace := os.Getenv("DEV_ORCHESTRATOR_INTEGRATION_WORKSPACE")
	localRoot := os.Getenv("DEV_ORCHESTRATOR_INTEGRATION_LOCAL_WORKSPACE")
	if workspace == "" || localRoot == "" {
		t.Fatal("dedicated temporary workspace required")
	}
	if err := os.MkdirAll(filepath.Join(localRoot, "workspace"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "workspace", "composition-sentinel"), []byte("p4412b-workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "host-only-sentinel"), []byte("outside workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	d, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	request := ports.RuntimeExecutionRequest{Workspace: workspace}
	config, err := executionWorkspaceConfig(request.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewDockerCodexExecutorRuntime(d).start(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	transport := session.(*CodexProcessTransport)
	id := transport.containerID
	inspected, err := d.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := inspected.Container
	if c.Config.Image != codexRuntimeImage || c.Config.WorkingDir != "/workspace" || c.HostConfig.Privileged || c.HostConfig.NetworkMode != "none" || len(c.Mounts) != 1 || c.Mounts[0].Source != workspace || c.Mounts[0].Destination != "/workspace" {
		t.Fatal("real container security/binding violated")
	}
	for _, env := range c.Config.Env {
		if strings.HasPrefix(env, "OPENAI_API_KEY=") || strings.HasPrefix(env, "CODEX_API_KEY=") {
			t.Fatal("unexpected credential environment")
		}
	}
	for _, cmd := range [][]string{
		{"/usr/bin/test", "-d", "/workspace"},
		{"/bin/grep", "-qx", "p4412b-workspace", "/workspace/composition-sentinel"},
		{"/usr/bin/test", "!", "-e", "/var/run/docker.sock"},
		{"/usr/bin/test", "!", "-e", "/host-only-sentinel"},
		{"/usr/bin/test", "!", "-e", "/run/codex-process/auth.json"},
		{"/usr/bin/test", "!", "-e", "/root/.codex/auth.json"},
	} {
		if err := d.authProbe(ctx, id, cmd); err != nil {
			t.Fatal("real isolation probe failed", cmd[1:])
		}
	}
	probeOutput := func(cmd []string) string {
		created, err := d.client.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: cmd, WorkingDir: "/workspace", AttachStdout: true})
		if err != nil {
			t.Fatal(err)
		}
		stream, err := d.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer stream.Close()
		if err := stream.Conn.SetReadDeadline(time.Now().Add(dockerOperationTimeout)); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		// Demultiplex through a bounded writer; EOF after a complete frame is normal.
		err = copyCodexProcessStdout(&output, io.LimitReader(stream.Reader, 4096))
		if err != io.EOF || output.Len() > 4096 {
			t.Fatal("invalid probe output", err)
		}
		if err := d.waitAuthExec(ctx, created.ID); err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(output.String())
	}
	if got := probeOutput([]string{"/bin/pwd"}); got != "/workspace" {
		t.Fatal("real cwd", got)
	}
	if got := probeOutput([]string{"codex", "--version"}); got != "codex-cli 0.159.2" {
		t.Fatal("real version", got)
	}
	response, err := codexHandshake(session)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.UserAgent, "0.159.2") {
		t.Fatal("handshake version")
	}
	if err := session.Close(); err != nil {
		t.Fatal("real cleanup", err)
	}
	if _, err := d.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("container retained")
	}
	t.Log("workspace binding, cwd, Codex 0.159.2, isolated network/mounts, stdio handshake and cleanup verified; no thread/turn/auth/account RPC")
}

// Only this infrastructure test harness selects the fixed image. No request or
// environment variable can supply an image, command, authentication or mounts.
func codexRuntimeSmokeOptions() client.ContainerCreateOptions {
	return client.ContainerCreateOptions{
		Config:     &container.Config{Image: codexRuntimeSmokeImage, WorkingDir: "/workspace", Cmd: []string{"/bin/sleep", "300"}},
		HostConfig: &container.HostConfig{NetworkMode: "none"},
		Platform:   &ocispec.Platform{OS: "linux"},
	}
}

func TestCodexRuntimeImageContract(t *testing.T) {
	data, err := os.ReadFile("testdata/codex-runtime/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	want := "FROM node:22.14.0-bookworm-slim@sha256:1c18d9ab3af4585870b92e4dbc5cac5a0dc77dd13df1a5905cea89fc720eb05b\n\nRUN apt-get update \\\n    && apt-get install -y --no-install-recommends ca-certificates \\\n    && rm -rf /var/lib/apt/lists/*\n\nRUN npm install --global --ignore-scripts @openai/codex@0.159.2\n\nWORKDIR /workspace\nENV CODEX_HOME=/run/codex-process\nRUN mkdir -p /run/codex-process\nCMD [\"/bin/sleep\", \"300\"]\n"
	if strings.ReplaceAll(string(data), "\r\n", "\n") != want {
		t.Fatal("runtime build must remain pinned and credential-free")
	}
	opts := codexRuntimeSmokeOptions()
	if opts.Config.Image != codexRuntimeSmokeImage || len(opts.Config.Env) != 0 || opts.HostConfig.Privileged || len(opts.HostConfig.Mounts) != 0 || opts.HostConfig.NetworkMode != "none" || opts.Config.WorkingDir != "/workspace" {
		t.Fatal("unsafe runtime smoke specification")
	}
	opts.Config.Image = "caller-image"
	if codexRuntimeSmokeOptions().Config.Image != codexRuntimeSmokeImage {
		t.Fatal("mutable image specification")
	}
}

func TestCodexRuntimeRealStartupSmoke(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_CODEX_SMOKE") != "1" {
		t.Skip("opt-in real pinned Codex startup")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	d, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	created, err := d.client.ContainerCreate(ctx, codexRuntimeSmokeOptions())
	if err != nil {
		t.Fatal(err)
	}
	id := created.ID
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
		defer cancel()
		if _, err := d.client.ContainerInspect(cleanup, id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			if err := d.Remove(cleanup, id); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := d.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	inspected, err := d.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := inspected.Container
	if c.HostConfig.Privileged || len(c.Mounts) != 0 || c.HostConfig.NetworkMode != "none" || c.Config.Image != codexRuntimeSmokeImage {
		t.Fatal("isolation violated")
	}
	for _, env := range c.Config.Env {
		if strings.HasPrefix(env, "OPENAI_API_KEY=") || strings.HasPrefix(env, "CODEX_API_KEY=") {
			t.Fatal("credential environment present")
		}
	}
	// Fixed local checks only; no auth source and no app-server RPC.
	if err := d.authProbe(ctx, id, []string{"/usr/bin/test", "-x", "/usr/local/bin/codex"}); err != nil {
		t.Fatal("installed binary unavailable")
	}
	for _, path := range []string{"/root/.codex/auth.json", "/run/codex-process/auth.json", "/workspace/auth.json", "/var/run/docker.sock"} {
		if err := d.authProbe(ctx, id, []string{"/usr/bin/test", "!", "-e", path}); err != nil {
			t.Fatal("unexpected auth/socket file")
		}
	}
	v, err := d.client.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: []string{"codex", "--version"}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := d.client.ExecAttach(ctx, v.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Bound hijacked stream lifetime as well as retained version output.
	stream.Conn.SetReadDeadline(time.Now().Add(dockerOperationTimeout))
	data, err := io.ReadAll(io.LimitReader(stream.Reader, 4096))
	stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	var version bytes.Buffer
	if err := copyCodexProcessStdout(&version, bytes.NewReader(data)); err != io.EOF {
		t.Fatal("malformed version stream")
	}
	if err := d.waitAuthExec(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(version.String()) != "codex-cli 0.159.2" {
		t.Fatalf("unexpected version: %q", version.String())
	}
	t.Log("reported_version=codex-cli 0.159.2")
	tr, response, err := startCodexHandshake(ctx, d, id)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if tr.attachment.input == nil || tr.attachment.output == nil {
		t.Fatal("stdio not attached")
	}
	if response == nil || response.PlatformOS != "linux" {
		t.Fatal("initialize response missing")
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-tr.closed:
		t.Fatal("unauthenticated process exited before observation")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-timer.C:
	}
	exec, err := d.client.ExecInspect(ctx, tr.attachment.execID, client.ExecInspectOptions{})
	if err != nil || !exec.Running {
		t.Fatal("real app-server is not alive")
	}
	if err := d.authProbe(ctx, id, []string{"/usr/bin/test", "!", "-e", "/run/codex-process/auth.json"}); err != nil {
		t.Fatal("authentication materialized")
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("container retained")
	}
	select {
	case <-tr.pumpDone:
	case <-ctx.Done():
		t.Fatal("attach pump retained")
	}
	t.Log("real_app_server_start=PASS initialize_response=received request_id_correlated=yes initialized_sent=yes process_alive_after_handshake=yes unauthenticated_start=yes cleanup=PASS network=none")
}

func TestDockerDriverLifecycle(t *testing.T) {
	for _, stage := range []string{"success", "create", "start", "wait", "exit", "remove"} {
		t.Run(stage, func(t *testing.T) {
			var calls []string
			var callsMu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				operation := ""
				switch {
				case strings.HasSuffix(r.URL.Path, "/create"):
					operation = "create"
				case strings.HasSuffix(r.URL.Path, "/start"):
					operation = "start"
				case strings.HasSuffix(r.URL.Path, "/wait"):
					operation = "wait"
				case r.Method == http.MethodDelete:
					operation = "remove"
				default:
					t.Errorf("unexpected Docker request %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				callsMu.Lock()
				calls = append(calls, operation)
				callsMu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if stage == operation {
					w.WriteHeader(500)
					fmt.Fprint(w, `{"message":"docker failure"}`)
					return
				}
				switch operation {
				case "create":
					var body struct {
						container.Config
						HostConfig container.HostConfig
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					mounts := body.HostConfig.Mounts
					if body.WorkingDir != "/workspace" || body.HostConfig.Privileged || len(mounts) != 1 || mounts[0].Type != mount.TypeBind || mounts[0].Source != "/trusted/project" || mounts[0].Target != "/workspace" || body.Image != dockerProbeImage || !reflect.DeepEqual([]string(body.Cmd), dockerProbeCommand()) {
						t.Errorf("unsafe create config: %+v", body)
					}
					if r.URL.Query().Get("platform") != "linux" {
						t.Error("Linux platform missing")
					}
					w.WriteHeader(201)
					fmt.Fprint(w, `{"Id":"probe-id"}`)
				case "wait":
					code := 0
					if stage == "exit" {
						code = 9
					}
					fmt.Fprintf(w, `{"StatusCode":%d}`, code)
				case "remove":
					if r.URL.Query().Get("force") != "1" && r.URL.Query().Get("force") != "true" {
						t.Error("cleanup must force removal")
					}
					w.WriteHeader(204)
				default:
					w.WriteHeader(204)
				}
			}))
			defer server.Close()
			sdk, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.56"))
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			driver := &DockerDriver{client: sdk}
			err = NewDockerExecutionEnvironment(driver).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project", Objective: "ignored shell", Scope: []string{"/"}})
			if (err == nil) != (stage == "success") {
				t.Fatalf("stage=%s error=%v", stage, err)
			}
			if stage != "success" && stage != "exit" && !strings.Contains(err.Error(), "docker failure") {
				t.Fatalf("Docker error lost: %v", err)
			}
			want := []string{"create", "start", "wait", "remove"}
			if stage == "create" {
				want = []string{"create"}
			}
			if stage == "start" {
				want = []string{"create", "start", "remove"}
			}
			callsMu.Lock()
			defer callsMu.Unlock()
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v want=%v", calls, want)
			}
		})
	}
}

func TestDockerDriverRealIntegration(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_DOCKER_INTEGRATION") != "1" {
		t.Skip("opt-in real Docker integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	driver, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	info, err := driver.client.Info(ctx, client.InfoOptions{})
	if err != nil || info.Info.OSType != "linux" {
		t.Fatal("Linux daemon required")
	}
	if _, err := driver.client.ImageInspect(ctx, dockerProbeImage); errdefs.IsNotFound(err) {
		pull, err := driver.client.ImagePull(ctx, dockerProbeImage, client.ImagePullOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := pull.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	workspace := os.Getenv("DEV_ORCHESTRATOR_INTEGRATION_WORKSPACE")
	localWorkspace := os.Getenv("DEV_ORCHESTRATOR_INTEGRATION_LOCAL_WORKSPACE")
	if workspace == "" {
		workspace = t.TempDir()
		localWorkspace = workspace
	}
	if localWorkspace == "" {
		t.Fatal("integration workspace mapping required")
	}
	entries, err := os.ReadDir(localWorkspace)
	if err != nil || len(entries) != 0 {
		t.Fatal("dedicated empty integration workspace required")
	}
	sentinel := filepath.Join(localWorkspace, "workspace-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("authorized sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sentinel)
	id, err := driver.Create(ctx, DockerEnvironmentConfig{workspace: workspace, authTmpfs: true})
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	defer func() {
		if removed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
		defer cancel()
		if err := driver.Remove(cleanupCtx, id); err != nil {
			t.Error("cleanup failed")
		}
	}()
	if err := driver.Start(ctx, id); err != nil {
		t.Fatal(err)
	}
	inspected, err := driver.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || !inspected.Container.State.Running {
		t.Fatal("container not running before exec")
	}
	c := inspected.Container
	for _, value := range c.Config.Env {
		if strings.Contains(value, "TEST_SECRET_DO_NOT_LEAK") {
			t.Fatal("secret environment")
		}
	}
	if c.HostConfig.Privileged || len(c.HostConfig.Mounts) != 2 || c.HostConfig.Mounts[1].Type != mount.TypeTmpfs || c.HostConfig.Mounts[1].Target != "/run/codex-auth" || c.HostConfig.Mounts[1].TmpfsOptions.Mode != 0700 || !reflect.DeepEqual([]string(c.Config.Cmd), []string{"/bin/sleep", "300"}) {
		t.Fatal("unsafe authenticated configuration")
	}
	if err := driver.authProbe(ctx, id, []string{"/bin/grep", "-q", " /run/codex-auth tmpfs ", "/proc/mounts"}); err != nil {
		t.Fatal("tmpfs not active")
	}
	if err := driver.authProbe(ctx, id, []string{"/bin/grep", "-q", "authorized sentinel", "/workspace/workspace-sentinel.txt"}); err != nil {
		t.Fatal("workspace sentinel unavailable")
	}
	for _, executable := range []string{"/usr/bin/tee", "/bin/chmod"} {
		if err := driver.authProbe(ctx, id, []string{"/usr/bin/test", "-x", executable}); err != nil {
			t.Fatal("BLOCKED_FIXED_WRITER: controlled tool unavailable")
		}
	}
	material := []byte("TEST_SECRET_DO_NOT_LEAK")
	defer clear(material)
	if err := driver.prepareAuth(ctx, id, "/run/codex-auth/auth.json", material); err != nil {
		t.Fatal("auth exec failed")
	}
	if err := driver.authProbe(ctx, id, []string{"/usr/bin/test", "-s", "/run/codex-auth/auth.json"}); err != nil {
		t.Fatalf("live tmpfs auth unavailable after exec: %v", err)
	}
	modeExec, err := driver.client.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd: []string{"/bin/stat", "-c", "%a", "/run/codex-auth/auth.json"}, AttachStdout: true, TTY: true,
	})
	if err != nil {
		t.Fatal("mode exec create failed")
	}
	modeStream, err := driver.client.ExecAttach(ctx, modeExec.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		t.Fatal("mode exec attach failed")
	}
	mode, err := io.ReadAll(io.LimitReader(modeStream.Reader, 32))
	modeStream.Close()
	if err != nil || strings.TrimSpace(string(mode)) != "600" {
		t.Fatal("auth mode is not 0600")
	}
	if err := driver.waitAuthExec(ctx, modeExec.ID); err != nil {
		t.Fatal("mode exec failed")
	}
	if err := driver.authProbe(ctx, id, []string{"/usr/bin/test", "!", "-e", "/workspace/auth.json"}); err != nil {
		t.Fatal("workspace contamination")
	}
	if err := driver.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	logs, err := driver.client.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatal("logs unavailable")
	}
	output, err := io.ReadAll(io.LimitReader(logs, 32768))
	logs.Close()
	leaked := bytes.Contains(output, material)
	clear(output)
	if err != nil || leaked {
		t.Fatal("sensitive log output")
	}
	if err := driver.Remove(ctx, id); err != nil {
		t.Fatal("cleanup failed")
	}
	removed = true
	if _, err := driver.client.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("container retained")
	}
	secondMaterial := []byte("TEST_SECRET_DO_NOT_LEAK")
	if err := newAuthenticatedDockerEnvironment(mappedAuthDocker{DockerDriver: driver, workspace: workspace}, fakeAuthSource{material: secondMaterial}).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: localWorkspace}); err != nil {
		t.Fatal("authenticated lifecycle failed")
	}
	for _, b := range secondMaterial {
		if b != 0 {
			t.Fatal("source retained material")
		}
	}
	entries, err = os.ReadDir(localWorkspace)
	if err != nil || len(entries) != 1 || entries[0].Name() != "workspace-sentinel.txt" {
		t.Fatal("workspace changed")
	}
}

func TestDockerDriverIgnoresCallerEnvironment(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://untrusted.invalid:2375")
	t.Setenv("DOCKER_TLS_VERIFY", "1")
	t.Setenv("DOCKER_CERT_PATH", "/untrusted")
	driver, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	if driver.client.DaemonHost() != client.DefaultDockerHost {
		t.Fatal("environment selected daemon")
	}
}

func TestDockerDriverAuthTmpfs(t *testing.T) {
	config := dockerCreateOptions(DockerEnvironmentConfig{workspace: "/trusted/project", authTmpfs: true})
	mounts := config.HostConfig.Mounts
	if len(mounts) != 2 || mounts[1].Type != mount.TypeTmpfs || mounts[1].Target != "/run/codex-auth" || mounts[1].Source != "" || mounts[1].TmpfsOptions.Mode != 0700 {
		t.Fatalf("unsafe auth tmpfs: %+v", mounts)
	}
}

// This opt-in validation harness uses the SDK because Docker CLI invocation is
// forbidden. It is separate from the dedicated-workspace integration probe.
// The repository is read-only; validation runs on a disposable container copy.
func TestDockerDriverLinuxValidation(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_DOCKER_VALIDATE") != "1" {
		t.Skip("opt-in Linux validation via SDK")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	driver, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	const image = "golang:1.25"
	if _, err := driver.client.ImageInspect(ctx, image); errdefs.IsNotFound(err) {
		pull, err := driver.client.ImagePull(ctx, image, client.ImagePullOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := pull.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	repository, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	created, err := driver.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, Tty: true, WorkingDir: "/", Cmd: []string{"/bin/sh", "-ec", `mkdir /validation; cp -a /source/. /validation/; cd /validation; chmod +x scripts/validate.sh; go test ./internal/infrastructure/...; ./scripts/validate.sh; go test -race ./internal/...`}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeBind, Source: repository, Target: "/source", ReadOnly: true}}},
		Platform:   &ocispec.Platform{OS: "linux"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := driver.Remove(context.Background(), created.ID); err != nil {
			t.Error(err)
		}
	}()
	if err := driver.Start(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	wait := driver.client.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	var waitErr error
	select {
	case <-ctx.Done():
		waitErr = ctx.Err()
	case err := <-wait.Error:
		waitErr = err
	case result := <-wait.Result:
		if result.StatusCode != 0 || result.Error != nil {
			waitErr = fmt.Errorf("Linux validation failed: %+v", result)
		}
	}
	logs, err := driver.client.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Error(err)
	} else {
		defer logs.Close()
		output, err := io.ReadAll(io.LimitReader(logs, 32768))
		if err != nil {
			t.Error(err)
		}
		t.Log(string(output))
	}
	if waitErr != nil {
		t.Fatal(waitErr)
	}
}

func TestDockerAuthTypedExec(t *testing.T) {
	for _, failure := range []string{"", "writer", "chmod"} {
		t.Run("failure="+failure, func(t *testing.T) {
			var calls []string
			var removed, workload bool
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"Id":"auth-id"}`)
				case strings.HasSuffix(r.URL.Path, "/containers/auth-id/start"), strings.HasSuffix(r.URL.Path, "/containers/auth-id/stop"):
					w.WriteHeader(204)
				case r.Method == http.MethodDelete:
					mu.Lock()
					removed = true
					mu.Unlock()
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/exec"):
					var options client.ExecCreateOptions
					if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
						t.Error(err)
					}
					mu.Lock()
					n := len(calls)
					calls = append(calls, "create")
					mu.Unlock()
					expected := []string{"/usr/bin/tee", "/run/codex-auth/auth.json"}
					id := "writer"
					if n > 0 {
						expected = []string{"/bin/chmod", "0600", "/run/codex-auth/auth.json"}
						id = "chmod"
					}
					if n > 1 {
						expected = []string{"/usr/bin/test", "-s", "/run/codex-auth/auth.json"}
						id = "workload"
						mu.Lock()
						workload = true
						mu.Unlock()
					}
					if !reflect.DeepEqual(options.Cmd, expected) || options.AttachStdout || options.AttachStderr || options.TTY || options.Privileged || len(options.Env) > 0 || options.AttachStdin != (id == "writer") {
						t.Error("unsafe exec options")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"Id":%q}`, id)
				case strings.HasSuffix(r.URL.Path, "/writer/start"):
					io.Copy(io.Discard, r.Body)
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					fmt.Fprint(rw, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					rw.Flush()
					content, err := io.ReadAll(io.LimitReader(rw, 1024))
					if err != nil || string(content) != "TEST_SECRET_DO_NOT_LEAK" {
						t.Error("secret not delivered by stdin")
					}
					clear(content)
					// Even unsolicited daemon output is drained without retaining it.
					fmt.Fprint(rw, "TEST_SECRET_DO_NOT_LEAK")
					rw.Flush()
				case strings.HasSuffix(r.URL.Path, "/chmod/start"), strings.HasSuffix(r.URL.Path, "/workload/start"):
					w.WriteHeader(200)
				case strings.HasSuffix(r.URL.Path, "/json"):
					code := 0
					if strings.Contains(r.URL.Path, "/"+failure+"/") && failure != "" {
						code = 9
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"Running":false,"ExitCode":%d}`, code)
				default:
					t.Error("unexpected request (archive forbidden)")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			sdk, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.56"))
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			driver := &DockerDriver{client: sdk}
			err = newAuthenticatedDockerEnvironment(driver, fakeAuthSource{material: []byte("TEST_SECRET_DO_NOT_LEAK")}).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project", Objective: "caller cannot select command"})
			if (err != nil) != (failure != "") || (err != nil && strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK")) {
				t.Error("exec failure not closed and redacted")
			}
			mu.Lock()
			count := len(calls)
			if !removed || workload != (failure == "") {
				t.Error("failure reached workload or skipped cleanup")
			}
			mu.Unlock()
			want := 3
			if failure == "chmod" {
				want = 2
			}
			if failure == "writer" {
				want = 1
			}
			if count != want {
				t.Errorf("exec count=%d want=%d", count, want)
			}
			if driver.prepareAuth(context.Background(), "auth-id", "/workspace/auth.json", []byte("fake")) == nil {
				t.Error("caller destination accepted")
			}
		})
	}
}

// The Linux test runner and Desktop daemon see the same dedicated host workspace
// through different paths. Only the test adapter translates that bind source.
type mappedAuthDocker struct {
	*DockerDriver
	workspace string
}

func TestDockerCodexProcessAttachAndFailureCleanup(t *testing.T) {
	for _, stage := range []string{"", "inspect", "create", "attach", "stop", "exit", "remove", "authenticated"} {
		t.Run(stage, func(t *testing.T) {
			var mu sync.Mutex
			var calls []string
			stdin := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls = append(calls, r.Method+" "+r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/containers/container/json"):
					if stage == "inspect" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					fmt.Fprint(w, `{"State":{"Running":true}}`)
				case strings.HasSuffix(r.URL.Path, "/containers/container/exec"):
					var options client.ExecCreateOptions
					if json.NewDecoder(r.Body).Decode(&options) != nil || !reflect.DeepEqual(options, codexProcessCreateOptions()) {
						t.Error("unsafe process spec")
					}
					if stage == "create" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					fmt.Fprint(w, `{"Id":"codex-exec"}`)
				case strings.HasSuffix(r.URL.Path, "/exec/codex-exec/start"):
					var options client.ExecStartOptions
					if json.NewDecoder(r.Body).Decode(&options) != nil || options.Detach || options.TTY {
						t.Error("unsafe attach/start")
					}
					if stage == "attach" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(5 * time.Second))
					fmt.Fprint(rw, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					rw.Flush()
					line, err := rw.ReadString('\n')
					if err != nil {
						t.Error(err)
						return
					}
					stdin <- line
					rw.Write(codexDockerFrame(2, "TEST_SECRET_DO_NOT_LEAK\n"))
					rw.Write(codexDockerFrame(1, "{\"method\":\"fake\"}\n"))
					rw.Flush()
					io.Copy(io.Discard, rw)
				case strings.HasSuffix(r.URL.Path, "/containers/container/stop"):
					if stage == "stop" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/exec/codex-exec/json"):
					if stage == "exit" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					fmt.Fprint(w, `{"Running":false,"ExitCode":0}`)
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/container"):
					if stage == "remove" {
						w.WriteHeader(500)
						fmt.Fprint(w, `{"message":"TEST_SECRET_DO_NOT_LEAK"}`)
						return
					}
					w.WriteHeader(204)
				default:
					t.Error("unexpected SDK operation")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			sdk, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(server.URL, "http://")), client.WithAPIVersion("1.56"))
			if err != nil {
				t.Fatal(err)
			}
			defer sdk.Close()
			d := &DockerDriver{client: sdk}
			if stage == "authenticated" {
				d.authContainers.Store("container", true)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			tr, err := startCodexProcessRuntime(ctx, d, "container")
			startFailure := stage == "inspect" || stage == "create" || stage == "attach" || stage == "authenticated"
			if (err != nil) != startFailure || err != nil && strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK") {
				t.Fatalf("start: %v", err)
			}
			if tr != nil {
				if err := tr.Write([]byte(`{"method":"fake"}`)); err != nil {
					t.Fatal(err)
				}
				message, err := tr.Read()
				if err != nil || string(message) != `{"method":"fake"}` {
					t.Fatalf("SDK stdout: %s %v", message, err)
				}
				if got := <-stdin; got != "{\"method\":\"fake\"}\n" {
					t.Fatalf("SDK stdin: %q", got)
				}
				err = tr.Close()
				cleanupFailure := stage == "stop" || stage == "exit" || stage == "remove"
				if (err != nil) != cleanupFailure || err != nil && strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK") {
					t.Fatalf("close: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(calls) == 0 || !strings.Contains(calls[len(calls)-1], "DELETE ") {
				t.Fatalf("no cleanup: %v", calls)
			}
			starts := 0
			for _, call := range calls {
				if strings.HasSuffix(call, "/exec/codex-exec/start") {
					starts++
				}
			}
			if starts > 1 {
				t.Fatal("process started twice")
			}
		})
	}
}

func (d mappedAuthDocker) Create(ctx context.Context, c DockerEnvironmentConfig) (string, error) {
	c.workspace = d.workspace
	return d.DockerDriver.Create(ctx, c)
}
