package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"dev-orchestrator/internal/ports"
)

// DockerEnvironmentConfig describes the only authorized bind mount. It has no
// caller-controlled command, additional mounts or privilege settings.
type DockerEnvironmentConfig struct {
	workspace string
}

func (c DockerEnvironmentConfig) WorkspaceSource() string { return c.workspace }
func (DockerEnvironmentConfig) WorkspaceTarget() string   { return "/workspace" }
func (DockerEnvironmentConfig) WorkingDirectory() string  { return "/workspace" }
func (DockerEnvironmentConfig) Privileged() bool          { return false }

// DockerLifecycle is an infrastructure seam, not an executor port. A driver must
// create a Linux container using exactly this configuration, with no other bind
// mounts. Create returns the container ID if a container needs cleanup, including
// when it also returns an error. Remove must remove a possibly running container.
// Driver implementations and the fixed future workload are outside this increment.
type DockerLifecycle interface {
	Create(context.Context, DockerEnvironmentConfig) (string, error)
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Remove(context.Context, string) error
}

type DockerExecutionEnvironment struct {
	docker DockerLifecycle
}

func NewDockerExecutionEnvironment(docker DockerLifecycle) *DockerExecutionEnvironment {
	return &DockerExecutionEnvironment{docker: docker}
}

// RunLifecycle prepares, starts and cleans up an isolated environment. It does
// not execute Codex or produce RuntimeExecutionResult, and is not ExecutorRuntime.
// The workspace is already physically validated by the application layer; these
// checks only reject unsafe boundary values without resolving a different path.
func (e *DockerExecutionEnvironment) RunLifecycle(ctx context.Context, request ports.RuntimeExecutionRequest) (resultErr error) {
	workspace := request.Workspace
	if strings.TrimSpace(workspace) == "" || strings.ContainsRune(workspace, '\x00') ||
		(!filepath.IsAbs(workspace) && !path.IsAbs(workspace)) {
		return errors.New("docker environment requires an absolute workspace")
	}
	clean := filepath.Clean(workspace)
	if filepath.Dir(clean) == clean || path.Clean(workspace) == "/" ||
		path.Clean(workspace) == "/var/run/docker.sock" || path.Clean(workspace) == "/run/docker.sock" {
		return errors.New("docker environment rejects host root or Docker socket workspace")
	}
	if e == nil || e.docker == nil {
		return errors.New("docker environment requires a lifecycle driver")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := e.docker.Create(ctx, DockerEnvironmentConfig{workspace: workspace})
	if id != "" {
		// Cleanup must remain possible after cancellation or any later failure.
		defer func() {
			if err := e.docker.Remove(context.WithoutCancel(ctx), id); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove docker environment: %w", err))
			}
		}()
	}
	if err != nil {
		return fmt.Errorf("create docker environment: %w", err)
	}
	if id == "" {
		return errors.New("docker driver returned an empty container ID")
	}
	if err := e.docker.Start(ctx, id); err != nil {
		return fmt.Errorf("start docker environment: %w", err)
	}
	if err := e.docker.Stop(context.WithoutCancel(ctx), id); err != nil {
		return fmt.Errorf("stop docker environment: %w", err)
	}
	return ctx.Err()
}
