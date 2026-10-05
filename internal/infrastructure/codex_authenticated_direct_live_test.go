package infrastructure

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Test-only diagnostic control. No production constructor or fallback can
// select this driver. Never create threads, turns or respond to tool requests.
type directControlDocker struct{ *DockerDriver }

func directControlCreateOptions(c DockerEnvironmentConfig) client.ContainerCreateOptions {
	opts := codexSessionCreateOptions(c)
	opts.HostConfig.NetworkMode = container.NetworkMode("bridge")
	return opts
}

func directControlProcessOptions() client.ExecCreateOptions {
	opts := codexProcessCreateOptions()
	opts.Env = []string{"CODEX_HOME=/run/codex-auth", "HTTPS_PROXY=", "HTTP_PROXY=", "ALL_PROXY=", "https_proxy=", "http_proxy=", "all_proxy=", "NO_PROXY=", "no_proxy="}
	return opts
}

func (d *directControlDocker) createCodexContainer(ctx context.Context, c DockerEnvironmentConfig) (string, error) {
	if !c.authTmpfs {
		return "", errors.New("diagnostic auth tmpfs required")
	}
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	created, err := d.client.ContainerCreate(ctx, directControlCreateOptions(c))
	if err != nil {
		return created.ID, errors.New("diagnostic container creation failed")
	}
	return created.ID, nil
}

func (d *directControlDocker) startCodexProcess(ctx context.Context, id string) (codexProcessAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	created, err := d.client.ExecCreate(ctx, id, directControlProcessOptions())
	if err != nil {
		return codexProcessAttachment{}, errors.New("diagnostic process creation failed")
	}
	attached, err := d.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return codexProcessAttachment{}, errors.New("diagnostic process attachment failed")
	}
	return codexProcessAttachment{execID: created.ID, input: codexProcessStdin{attached.Conn}, output: attached.Reader, close: attached.Close, closeWrite: attached.CloseWrite}, nil
}

type directControlAuthDriver interface {
	codexSessionDocker
	prepareAuth(context.Context, string, string, []byte) error
}

func startDirectControl(ctx context.Context, driver directControlAuthDriver, source chatGPTAuthSource, config DockerEnvironmentConfig) (*CodexProcessTransport, error) {
	if err := ctx.Err(); err != nil {
		return nil, errors.New("diagnostic context canceled")
	}
	if _, err := executionWorkspaceConfig(config.workspace); err != nil {
		return nil, errors.New("diagnostic workspace rejected")
	}
	material, err := source.obtain(ctx)
	defer clear(material)
	if err != nil || len(material) == 0 {
		return nil, errors.New("diagnostic authentication unavailable")
	}
	config.authTmpfs = true
	id, err := driver.createCodexContainer(ctx, config)
	cleanup := func() error {
		if id != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
			defer cancel()
			if driver.Remove(cleanupCtx, id) != nil {
				return errors.New("diagnostic container cleanup failed")
			}
		}
		return nil
	}
	if err != nil || id == "" {
		return nil, errors.Join(errors.New("diagnostic container creation failed"), cleanup())
	}
	if driver.Start(ctx, id) != nil {
		return nil, errors.Join(errors.New("diagnostic container start failed"), cleanup())
	}
	if driver.prepareAuth(ctx, id, "/run/codex-auth/auth.json", material) != nil {
		return nil, errors.Join(errors.New("diagnostic authentication preparation failed"), cleanup())
	}
	clear(material)
	// Existing transport takes ownership, including cleanup on startup failure.
	return startCodexProcessRuntime(ctx, driver, id)
}

type directControlSession interface {
	codexHandshakeTransport
	Close() error
}
type directControlReport struct {
	Skipped, Initialize, Account         bool
	Classification, RPCCode, SafeMessage string
}

func runDirectControl(flag string, open func() (directControlSession, error)) (report directControlReport, err error) {
	if flag != "1" {
		report.Skipped = true
		return report, nil
	}
	report.Classification, report.RPCCode, report.SafeMessage = "not_reached", "none", "account read not reached"
	session, openErr := open()
	if openErr != nil {
		return report, errors.New("diagnostic session unavailable")
	}
	defer func() {
		if session.Close() != nil {
			err = errors.New("diagnostic cleanup failed")
		}
	}()
	if _, handshakeErr := codexHandshake(session); handshakeErr != nil {
		return report, nil
	}
	report.Initialize = true
	if accountErr := requireCodexChatGPTAccount(session); accountErr != nil {
		var diagnostic *CodexAccountReadError
		report.Classification, report.SafeMessage = "other", "account read failed"
		if errors.As(accountErr, &diagnostic) {
			report.Classification, report.SafeMessage = diagnostic.Kind, diagnostic.SafeMessage
			if diagnostic.RPCCode != nil {
				report.RPCCode = strconv.FormatInt(*diagnostic.RPCCode, 10)
			}
		}
		return report, nil
	}
	report.Account = true
	report.Classification, report.RPCCode, report.SafeMessage = "", "", ""
	return report, nil
}

func TestAuthenticatedCodexDirectLiveOptIn(t *testing.T) {
	flag := os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_DIRECT_LIVE")
	if flag != "1" {
		t.Skip("opt-in direct authenticated diagnostic control")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	report, err := runDirectControl(flag, func() (directControlSession, error) {
		homePath := os.Getenv("DEV_ORCHESTRATOR_CODEX_AUTH_HOME")
		if homePath == "" {
			return nil, errors.New("explicit authorized home required")
		}
		home, err := NewAuthorizedCodexHome(homePath, true)
		if err != nil {
			return nil, errors.New("authorized home rejected")
		}
		driver, err := NewDockerDriver()
		if err != nil {
			return nil, errors.New("Docker unavailable")
		}
		t.Cleanup(func() { _ = driver.Close() })
		config, err := executionWorkspaceConfig(t.TempDir())
		if err != nil {
			return nil, errors.New("temporary workspace rejected")
		}
		return startDirectControl(ctx, &directControlDocker{driver}, home, config)
	})
	status := func(pass bool) string {
		if pass {
			return "PASS"
		}
		return "FAIL"
	}
	t.Logf("initialize=%s", status(report.Initialize))
	t.Logf("account_read=%s", status(report.Account))
	if report.Account {
		t.Log("account_type=chatgpt")
	} else {
		t.Logf("classification=%s rpc_code=%s safe_message=%s", report.Classification, report.RPCCode, report.SafeMessage)
	}
	if err != nil {
		t.Fatal(err.Error())
	}
	if !report.Initialize || !report.Account {
		t.Fail()
	}
}
