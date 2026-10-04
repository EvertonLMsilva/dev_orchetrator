package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const dockerProbeImage = "alpine:3.23"
const dockerOperationTimeout = 30 * time.Second

// DockerDriver runs only an internal isolation probe, never Codex or caller commands.
// Authenticated containers stay alive until the fixed probe completes.
type DockerDriver struct {
	client         *client.Client
	authContainers sync.Map
}

var _ chatGPTAuthDocker = (*DockerDriver)(nil)

var _ DockerLifecycle = (*DockerDriver)(nil)

// NewDockerDriver uses the SDK's local default endpoint. Environment variables,
// runtime requests and model output cannot select a daemon or image.
func NewDockerDriver() (*DockerDriver, error) {
	sdk, err := client.New(client.WithHost(client.DefaultDockerHost), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker client: %w", err)
	}
	return &DockerDriver{client: sdk}, nil
}

func (d *DockerDriver) Close() error { return d.client.Close() }

func dockerProbeCommand() []string {
	return []string{"/bin/sh", "-ec", `test -d /workspace; test "$(pwd)" = /workspace; test ! -e /var/run/docker.sock`}
}

func dockerCreateOptions(c DockerEnvironmentConfig) client.ContainerCreateOptions {
	mounts := []mount.Mount{{Type: mount.TypeBind, Source: c.WorkspaceSource(), Target: c.WorkspaceTarget()}}
	if target := c.AuthTmpfsTarget(); target != "" {
		mounts = append(mounts, mount.Mount{Type: mount.TypeTmpfs, Target: target, TmpfsOptions: &mount.TmpfsOptions{Mode: 0700}})
	}
	cmd := dockerProbeCommand()
	if c.authTmpfs {
		cmd = []string{"/bin/sleep", "300"}
	}
	return client.ContainerCreateOptions{
		Config:     &container.Config{Image: dockerProbeImage, WorkingDir: c.WorkingDirectory(), Cmd: cmd},
		HostConfig: &container.HostConfig{Privileged: c.Privileged(), Mounts: mounts},
		Platform:   &ocispec.Platform{OS: "linux"},
	}
}

func (d *DockerDriver) Create(ctx context.Context, c DockerEnvironmentConfig) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	result, err := d.client.ContainerCreate(ctx, dockerCreateOptions(c))
	if err != nil {
		return result.ID, fmt.Errorf("Docker create: %w", err)
	}
	if c.authTmpfs {
		d.authContainers.Store(result.ID, true)
	}
	return result.ID, nil
}

func (d *DockerDriver) Start(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	_, err := d.client.ContainerStart(ctx, id, client.ContainerStartOptions{})
	if err != nil {
		return fmt.Errorf("Docker start: %w", err)
	}
	return nil
}

// Stop completes the fixed workload before stopping authenticated containers.
func (d *DockerDriver) Stop(ctx context.Context, id string) error {
	if _, ok := d.authContainers.Load(id); !ok {
		return d.Wait(ctx, id)
	}
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	if err := d.authProbe(ctx, id, []string{"/usr/bin/test", "-s", "/run/codex-auth/auth.json"}); err != nil {
		return err
	}
	if _, err := d.client.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil {
		return errors.New("stop authenticated container failed")
	}
	return nil
}

// authProbe accepts only infrastructure-owned arguments, never runtime input.
func (d *DockerDriver) authProbe(ctx context.Context, id string, cmd []string) error {
	created, err := d.client.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: cmd})
	if err != nil {
		return errors.New("auth workload create failed")
	}
	if _, err := d.client.ExecStart(ctx, created.ID, client.ExecStartOptions{Detach: true}); err != nil {
		return errors.New("auth workload start failed")
	}
	return d.waitAuthExec(ctx, created.ID)
}

func (d *DockerDriver) waitAuthExec(ctx context.Context, execID string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, err := d.client.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return errors.New("auth workload inspect failed")
		}
		if !result.Running {
			if result.ExitCode != 0 {
				return fmt.Errorf("auth workload failed (exit %d)", result.ExitCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("auth workload timeout")
		case <-ticker.C:
		}
	}
}

func (d *DockerDriver) prepareAuth(ctx context.Context, id, target string, material []byte) error {
	if target != "/run/codex-auth/auth.json" || len(material) == 0 {
		return errors.New("invalid authentication materialization")
	}
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	created, err := d.client.ExecCreate(ctx, id, client.ExecCreateOptions{
		Cmd: []string{"/usr/bin/tee", "/run/codex-auth/auth.json"}, AttachStdin: true,
	})
	if err != nil {
		return errors.New("auth writer create failed")
	}
	attached, err := d.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return errors.New("auth writer attach failed")
	}
	defer attached.Close()
	// Cancellation closes the hijacked connection too: its I/O is not governed
	// by the HTTP request context after the upgrade. Output is never retained.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			attached.Close()
		case <-done:
		}
	}()
	if n, err := attached.Conn.Write(material); err != nil || n != len(material) {
		return errors.New("auth stdin failed")
	}
	if err := attached.CloseWrite(); err != nil {
		return errors.New("auth stdin close failed")
	}
	if _, err := io.Copy(io.Discard, attached.Reader); err != nil {
		return errors.New("auth writer stream failed")
	}
	if err := d.waitAuthExec(ctx, created.ID); err != nil {
		return err
	}
	return d.authProbe(ctx, id, []string{"/bin/chmod", "0600", "/run/codex-auth/auth.json"})
}

func (d *DockerDriver) Wait(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	wait := d.client.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-ctx.Done():
		return fmt.Errorf("Docker wait: %w", ctx.Err())
	case err := <-wait.Error:
		return fmt.Errorf("Docker wait: %w", err)
	case result := <-wait.Result:
		if result.Error != nil {
			return fmt.Errorf("Docker wait: %s", result.Error.Message)
		}
		if result.StatusCode != 0 {
			return fmt.Errorf("Docker probe exit status %d", result.StatusCode)
		}
		return nil
	}
}

func (d *DockerDriver) Remove(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	_, err := d.client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	if err != nil {
		return fmt.Errorf("Docker remove: %w", err)
	}
	d.authContainers.Delete(id)
	return nil
}
