package infrastructure

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"dev-orchestrator/internal/ports"
)

// RuntimeHomeDockerContainer owns one controlled-egress container. Its caller
// must place it under a RuntimeHomeLease before Prepare or Start. Credentials
// travel only through bounded stdin/stdout buffers and container tmpfs.
type RuntimeHomeDockerContainer struct {
	driver    *DockerDriver
	egress    *ownedCodexEgress
	id        string
	process   *CodexProcessTransport
	destroyed bool
}

func (*RuntimeHomeDockerContainer) Format(s fmt.State, _ rune) {
	io.WriteString(s, "RuntimeHomeDockerContainer[redacted]")
}

func NewRuntimeHomeDockerContainer(hosts string) (*RuntimeHomeDockerContainer, error) {
	if hosts != "auth.openai.com,chatgpt.com" && hosts != "chatgpt.com,auth.openai.com" {
		return nil, errors.New("approved explicit device login hosts required")
	}
	d, err := NewDockerDriver()
	if err != nil {
		return nil, errors.New("runtime Docker unavailable")
	}
	return &RuntimeHomeDockerContainer{driver: d}, nil
}

func (c *RuntimeHomeDockerContainer) Prepare(ctx context.Context, archive io.Reader) error {
	if c == nil || c.driver == nil || c.egress != nil || c.destroyed {
		return errors.New("runtime preparation rejected")
	}
	owned, err := c.driver.prepareCodexEgress(ctx, []string{"auth.openai.com", "chatgpt.com"})
	if err != nil {
		return errors.New("runtime egress unavailable")
	}
	c.egress = owned.(*ownedCodexEgress)
	opts := runtimeHomeCreateOptions(c.egress.private)
	bounded, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	created, err := c.driver.client.ContainerCreate(bounded, opts)
	c.id = created.ID
	if err != nil || c.id == "" || c.driver.Start(bounded, c.id) != nil {
		return errors.New("runtime creation failed")
	}
	data, err := io.ReadAll(io.LimitReader(archive, 1024*1024+8193))
	defer clear(data)
	if err != nil || len(data) > 1024*1024+8192 {
		return errors.New("runtime preparation limit")
	}
	_, err = c.fixedExec(bounded, []string{"/bin/tar", "--extract", "--file=-", "--directory=/run/codex-auth", "--no-same-owner"}, data, false)
	return err
}

func runtimeHomeCreateOptions(private string) client.ContainerCreateOptions {
	opts := authenticatedCodexCreateOptions(DockerEnvironmentConfig{workspace: "/workspace", authTmpfs: true}, private)
	// Device login/account read require no host workspace or credential mounts.
	opts.HostConfig.Mounts = opts.HostConfig.Mounts[1:]
	opts.HostConfig.LogConfig.Type = "none"
	opts.Config.Cmd = []string{"/bin/sleep", "1200"}
	return opts
}

// Start cannot read desktop auth, initialize RPC, start login or run inference.
func (c *RuntimeHomeDockerContainer) Start(ctx context.Context) (CodexExecutorSession, error) {
	if c == nil || c.destroyed || c.id == "" || c.process != nil {
		return nil, errors.New("runtime process rejected")
	}
	version, err := c.fixedExec(ctx, []string{"codex", "--version"}, nil, true)
	defer clear(version)
	if err != nil || strings.TrimSpace(string(version)) != "codex-cli 0.159.2" {
		return nil, errors.New("runtime version rejected")
	}
	c.process, err = startLeaseCodexProcessRuntime(ctx, c.egress, c.id)
	return c.process, err
}

func (c *RuntimeHomeDockerContainer) Capture(ctx context.Context) (io.ReadCloser, error) {
	if c == nil || c.destroyed || c.process == nil || c.process.Close() != nil {
		return nil, errors.New("runtime capture rejected")
	}
	data, err := c.fixedExec(ctx, []string{"/bin/sh", "-ec", `cd /run/codex-auth; test ! -L auth.json; test -f auth.json; test "$(stat -c %a auth.json)" = 600; exec tar --format=ustar -cf - auth.json`}, nil, true)
	if err != nil {
		clear(data)
		return nil, errors.New("runtime capture failed")
	}
	return &runtimeCaptureReader{Reader: bytes.NewReader(data), data: data}, nil
}

type runtimeCaptureReader struct {
	*bytes.Reader
	data []byte
}

func (r *runtimeCaptureReader) Close() error { clear(r.data); r.data = nil; return nil }

// Destroy is idempotent and called only by the lease, also on partial failures.
func (c *RuntimeHomeDockerContainer) Destroy(ctx context.Context) error {
	if c == nil || c.destroyed {
		return nil
	}
	var err error
	if c.process != nil {
		err = c.process.Close()
	}
	if c.egress != nil {
		err = errors.Join(err, c.egress.Remove(ctx, c.id))
	}
	if c.driver != nil {
		err = errors.Join(err, c.driver.Close())
	}
	if err != nil {
		return errors.New("runtime cleanup failed")
	}
	c.destroyed = true
	return nil
}

// Unknown destinations remain denied; only already-sanitized hostname leaves.
func (c *RuntimeHomeDockerContainer) DeniedHost(ctx context.Context) string {
	if c == nil || c.egress == nil || c.destroyed {
		return ""
	}
	return c.egress.blockedDestination(ctx)
}

type boundedRuntimeOutput struct{ bytes.Buffer }

func (b *boundedRuntimeOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024+8192 {
		return 0, errors.New("runtime output limit")
	}
	return b.Buffer.Write(p)
}

func (c *RuntimeHomeDockerContainer) fixedExec(ctx context.Context, cmd []string, input []byte, retain bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	created, err := c.driver.client.ExecCreate(ctx, c.id, client.ExecCreateOptions{Cmd: cmd, AttachStdin: input != nil, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return nil, errors.New("runtime exec failed")
	}
	attached, err := c.driver.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, errors.New("runtime attach failed")
	}
	defer attached.Close()
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()
	attached.Conn.SetDeadline(time.Now().Add(dockerOperationTimeout))
	if input != nil {
		if n, e := attached.Conn.Write(input); e != nil || n != len(input) {
			return nil, errors.New("runtime stdin failed")
		}
		if attached.CloseWrite() != nil {
			return nil, errors.New("runtime stdin close failed")
		}
	}
	var output boundedRuntimeOutput
	var destination io.Writer = io.Discard
	if retain {
		destination = &output
	}
	err = copyCodexProcessStdout(destination, attached.Reader)
	if !errors.Is(err, io.EOF) || c.driver.waitAuthExec(ctx, created.ID) != nil {
		clear(output.Bytes())
		return nil, errors.New("runtime exec unconfirmed")
	}
	return output.Bytes(), nil
}

// Exposes only the existing account classifier. Raw errors and account identity
// never cross this administrative boundary.
func ReadRuntimeHomeAccount(ctx context.Context, session CodexExecutorSession) (string, *int64) {
	err := requireCodexChatGPTAccount(codexContextTransport{ctx: ctx, session: session})
	if err == nil {
		return "pass", nil
	}
	var diagnostic *CodexAccountReadError
	if errors.As(err, &diagnostic) && diagnostic.Kind == "workspace_routing" {
		return "workspace_routing", diagnostic.RPCCode
	}
	return "account_unavailable", nil
}

// DockerEnvironmentConfig describes the only authorized bind mount. It has no
// caller-controlled command, additional mounts or privilege settings.
type DockerEnvironmentConfig struct {
	workspace string
	authTmpfs bool
}

// AuthTmpfsTarget is container-only tmpfs (mode 0700), never a bind mount or
// persistent volume. The driver must create it before preparing authentication.
func (c DockerEnvironmentConfig) AuthTmpfsTarget() string {
	if c.authTmpfs {
		return "/run/codex-auth"
	}
	return ""
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
	docker       DockerLifecycle
	authRequired bool
	authSource   chatGPTAuthSource
	allowedHosts []string
}

func NewDockerExecutionEnvironment(docker DockerLifecycle) *DockerExecutionEnvironment {
	return &DockerExecutionEnvironment{docker: docker}
}

// The application has physically validated this workspace. Infrastructure only
// checks the boundary value and preserves it exactly; it never resolves a fallback.
func executionWorkspaceConfig(workspace string) (DockerEnvironmentConfig, error) {
	if strings.TrimSpace(workspace) == "" || strings.ContainsRune(workspace, '\x00') ||
		(!filepath.IsAbs(workspace) && !path.IsAbs(workspace)) {
		return DockerEnvironmentConfig{}, errors.New("docker environment requires an absolute workspace")
	}
	clean := filepath.Clean(workspace)
	if filepath.Dir(clean) == clean || path.Clean(workspace) == "/" ||
		path.Clean(workspace) == "/var/run/docker.sock" || path.Clean(workspace) == "/run/docker.sock" {
		return DockerEnvironmentConfig{}, errors.New("docker environment rejects host root or Docker socket workspace")
	}
	return DockerEnvironmentConfig{workspace: workspace}, nil
}

type codexSessionDocker interface {
	codexProcessDocker
	createCodexContainer(context.Context, DockerEnvironmentConfig) (string, error)
	Start(context.Context, string) error
}

// startCodexSession owns partial creation until the process transport takes
// ownership. Both paths remove resources using fresh, bounded cleanup contexts.
func (e *DockerExecutionEnvironment) startCodexSession(ctx context.Context, config DockerEnvironmentConfig) (CodexExecutorSession, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e == nil || (!e.authRequired && config.authTmpfs) {
		return nil, errors.New("unauthenticated codex environment required")
	}
	if _, err := executionWorkspaceConfig(config.workspace); err != nil {
		return nil, err
	}
	driver, ok := e.docker.(codexSessionDocker)
	if !ok || driver == nil {
		return nil, errors.New("codex session driver required")
	}
	var material []byte
	if e.authRequired {
		if e.authSource == nil {
			return nil, errors.New("authorized codex home required")
		}
		var err error
		material, err = e.authSource.obtain(ctx)
		defer clear(material)
		if err != nil || len(material) == 0 {
			return nil, errors.New("chatgpt authentication unavailable")
		}
		factory, ok := e.docker.(codexEgressFactory)
		if !ok {
			return nil, errors.New("controlled egress driver required")
		}
		owned, err := factory.prepareCodexEgress(ctx, e.allowedHosts)
		if err != nil {
			return nil, errors.New("controlled egress unavailable")
		}
		driver = owned
		config.authTmpfs = true
	}
	id, err := driver.createCodexContainer(ctx, config)
	cleanup := func(failure error) error {
		if id == "" && !e.authRequired {
			return failure
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
		defer cancel()
		if driver.Remove(cleanupCtx, id) != nil {
			return errors.Join(failure, errors.New("codex session container cleanup failed"))
		}
		return failure
	}
	if err != nil {
		return nil, cleanup(errors.New("codex session container creation failed"))
	}
	if id == "" {
		return nil, cleanup(errors.New("codex session empty container ID"))
	}
	if err := ctx.Err(); err != nil {
		return nil, cleanup(err)
	}
	if driver.Start(ctx, id) != nil {
		return nil, cleanup(errors.New("codex session container start failed"))
	}
	if err := ctx.Err(); err != nil {
		return nil, cleanup(err)
	}
	if e.authRequired {
		authDriver, ok := driver.(interface {
			prepareAuth(context.Context, string, string, []byte) error
		})
		if !ok || authDriver.prepareAuth(ctx, id, "/run/codex-auth/auth.json", material) != nil {
			return nil, cleanup(errors.New("chatgpt authentication preparation failed"))
		}
		clear(material)
	}
	// Ownership now transfers, including when process startup fails.
	transport, err := startCodexProcessRuntime(ctx, driver, id)
	if err != nil {
		return nil, err
	}
	return transport, nil
}

// RunLifecycle prepares, starts and cleans up an isolated environment. It does
// not execute Codex or produce RuntimeExecutionResult, and is not ExecutorRuntime.
// The workspace is already physically validated by the application layer; these
// checks only reject unsafe boundary values without resolving a different path.
func (e *DockerExecutionEnvironment) RunLifecycle(ctx context.Context, request ports.RuntimeExecutionRequest) (resultErr error) {
	workspace := request.Workspace
	if _, err := executionWorkspaceConfig(workspace); err != nil {
		return err
	}
	if e == nil || e.docker == nil {
		return errors.New("docker environment requires a lifecycle driver")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Auth errors, including driver errors after injection, are opaque: wrapping
	// an underlying error could expose material supplied by a source or driver.
	boundaryError := func(operation string, err error) error {
		if e.authRequired {
			return errors.New(operation + " failed")
		}
		return fmt.Errorf("%s: %w", operation, err)
	}
	var material []byte
	if e.authRequired {
		if e.authSource == nil {
			return errors.New("chatgpt authentication source required")
		}
		var err error
		material, err = e.authSource.obtain(ctx)
		defer func() { clear(material) }()
		if err != nil || len(material) == 0 {
			return errors.New("chatgpt authentication unavailable")
		}
		if _, ok := e.docker.(chatGPTAuthDocker); !ok {
			return errors.New("chatgpt authentication preparation required")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	id, err := e.docker.Create(ctx, DockerEnvironmentConfig{workspace: workspace, authTmpfs: e.authRequired})
	if id != "" {
		// Cleanup must remain possible after cancellation or any later failure.
		defer func() {
			if err := e.docker.Remove(context.WithoutCancel(ctx), id); err != nil {
				resultErr = errors.Join(resultErr, boundaryError("remove docker environment", err))
			}
		}()
	}
	if err != nil {
		return boundaryError("create docker environment", err)
	}
	if id == "" {
		return errors.New("docker driver returned an empty container ID")
	}
	if err := e.docker.Start(ctx, id); err != nil {
		return boundaryError("start docker environment", err)
	}
	if e.authRequired {
		if err := e.docker.(chatGPTAuthDocker).prepareAuth(ctx, id, "/run/codex-auth/auth.json", material); err != nil {
			return boundaryError("prepare chatgpt authentication", err)
		}
		clear(material)
	}
	if err := e.docker.Stop(context.WithoutCancel(ctx), id); err != nil {
		return boundaryError("stop docker environment", err)
	}
	return ctx.Err()
}
