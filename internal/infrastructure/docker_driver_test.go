package infrastructure

import (
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
		t.Skip("set DEV_ORCHESTRATOR_DOCKER_INTEGRATION=1 to use the real local daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	driver, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	info, err := driver.client.Info(ctx, client.InfoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if info.Info.OSType != "linux" {
		t.Fatal("Linux Docker daemon required")
	}
	var workspace string
	t.Cleanup(func() {
		if _, err := os.Stat(workspace); !os.IsNotExist(err) {
			t.Errorf("temporary workspace not removed: %v", err)
		}
	})
	workspace = t.TempDir()
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
	if err := os.WriteFile(filepath.Join(workspace, "workspace-sentinel.txt"), []byte("authorized sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := NewDockerExecutionEnvironment(driver).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	options := dockerCreateOptions(DockerEnvironmentConfig{workspace: workspace, authTmpfs: true})
	// The integration workload is fixed here; it is never a runtime request field.
	options.Config.Cmd = []string{"/bin/sh", "-ec", `test -d /workspace; test "$(pwd)" = /workspace; test "$(cat /workspace/workspace-sentinel.txt)" = "authorized sentinel"; test ! -e /var/run/docker.sock; test -d /run/codex-auth; grep -q ' /run/codex-auth tmpfs ' /proc/mounts; umask 077; printf fake-secret > /run/codex-auth/auth.json; test ! -e /workspace/auth.json`}
	created, err := driver.client.ContainerCreate(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	defer func() {
		if !removed {
			if err := driver.Remove(context.Background(), created.ID); err != nil {
				t.Error(err)
			}
		}
	}()
	inspected, err := driver.client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := inspected.Container
	if c.Config.WorkingDir != "/workspace" || c.HostConfig.Privileged || len(c.HostConfig.Mounts) != 2 || c.HostConfig.Mounts[0].Source != workspace || c.HostConfig.Mounts[0].Target != "/workspace" || c.HostConfig.Mounts[1].Type != mount.TypeTmpfs || c.HostConfig.Mounts[1].Target != "/run/codex-auth" {
		t.Fatal("unexpected real Docker configuration")
	}
	if err := driver.Start(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := driver.Wait(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := driver.Remove(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	removed = true
	if _, err := driver.client.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("container was not removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "auth.json")); !os.IsNotExist(err) {
		t.Fatal("auth persisted in workspace")
	}
	t.Log("real Linux probe passed; sentinel visible; tmpfs present; container removed; temporary workspace cleanup registered")
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
