package infrastructure

import (
	"context"
	"fmt"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const dockerProbeImage = "alpine:3.23"
const dockerOperationTimeout = 30 * time.Second

// DockerDriver runs only an internal isolation probe, never Codex or caller commands.
// It implements the existing infrastructure lifecycle; Stop waits for the probe.
type DockerDriver struct{ client *client.Client }

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
	return client.ContainerCreateOptions{
		Config:     &container.Config{Image: dockerProbeImage, WorkingDir: c.WorkingDirectory(), Cmd: dockerProbeCommand()},
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

// Stop fulfills the lifecycle's completion step by awaiting the fixed workload.
func (d *DockerDriver) Stop(ctx context.Context, id string) error { return d.Wait(ctx, id) }

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
	return nil
}
