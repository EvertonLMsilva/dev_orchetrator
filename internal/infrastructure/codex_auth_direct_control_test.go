package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"github.com/moby/moby/api/types/mount"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestDirectControlOptInAndProtocol(t *testing.T) {
	for _, flag := range []string{"", "0", "true", "1"} {
		calls := 0
		s := &directControlFakeSession{threadFake: threadFake{messages: []string{handshakeResponse, `{"method":"tool/run","params":{"secret":"SECRET"}}`, `{"id":99,"result":{"account":{"type":"chatgpt","email":"SECRET","planType":"plus"},"requiresOpenaiAuth":true}}`}}}
		result, err := runDirectControl(flag, func() (directControlSession, error) { calls++; return s, nil })
		if flag != "1" {
			if calls != 0 || !result.Skipped || err != nil {
				t.Fatal("control invoked without explicit opt-in")
			}
			continue
		}
		if err != nil || !result.Initialize || !result.Account || !s.closed || calls != 1 {
			t.Fatal("control failed or cleanup missing")
		}
		if strings.Contains(fmt.Sprint(result), "SECRET") {
			t.Fatal("account metadata exposed")
		}
		if len(s.writes) != 3 || !strings.Contains(string(s.writes[0]), `"method":"initialize"`) || !strings.Contains(string(s.writes[1]), `"method":"initialized"`) || !strings.Contains(string(s.writes[2]), `"method":"account/read"`) {
			t.Fatal("control escaped initialize/account scope")
		}
	}
}

type directControlFakeSession struct {
	threadFake
	closed   bool
	closeErr error
}

func (s *directControlFakeSession) Close() error { s.closed = true; return s.closeErr }

func TestDirectControlSanitizationAndCleanup(t *testing.T) {
	for _, response := range []string{`{"id":99,"error":{"code":-32603,"message":"workspace routing discovery failed","data":{"token":"SECRET","account_id":"SECRET"}}}`, `{"id":99,"error":{"code":-32603,"message":"Authorization: SECRET auth.json SECRET"}}`, `{"id":99,"result":{"account":{"type":"chatgpt","email":"SECRET","access_token":"SECRET"}}}`} {
		s := &directControlFakeSession{threadFake: threadFake{messages: []string{handshakeResponse, response}}}
		report, err := runDirectControl("1", func() (directControlSession, error) { return s, nil })
		if !s.closed || err != nil || report.Account || strings.Contains(fmt.Sprint(report), "SECRET") {
			t.Fatal("diagnostic leaked or failed cleanup")
		}
	}
	s := &directControlFakeSession{threadFake: threadFake{messages: []string{"SECRET"}}, closeErr: errors.New("SECRET")}
	report, err := runDirectControl("1", func() (directControlSession, error) { return s, nil })
	if !s.closed || report.Initialize || err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("initialize failure cleanup leaked")
	}
}

func TestDirectControlDockerSpecification(t *testing.T) {
	config := DockerEnvironmentConfig{workspace: "/diagnostic/workspace", authTmpfs: true}
	opts := directControlCreateOptions(config)
	production := codexSessionCreateOptions(config)
	if opts.Config.Image != codexRuntimeImage || opts.HostConfig.NetworkMode != "bridge" || opts.HostConfig.Privileged || len(opts.HostConfig.Mounts) != 2 || opts.HostConfig.Mounts[0].Source != "/diagnostic/workspace" || opts.HostConfig.Mounts[1].Type != mount.TypeTmpfs || opts.HostConfig.Mounts[1].Target != "/run/codex-auth" || opts.HostConfig.Mounts[1].TmpfsOptions.Mode != 0700 {
		t.Fatal("unsafe direct specification")
	}
	opts.HostConfig.NetworkMode = production.HostConfig.NetworkMode
	if !reflect.DeepEqual(opts, production) || codexSessionCreateOptions(config).HostConfig.NetworkMode != "none" {
		t.Fatal("production specification changed")
	}
	process := directControlProcessOptions()
	if !reflect.DeepEqual(process.Env, []string{"CODEX_HOME=/run/codex-auth", "HTTPS_PROXY=", "HTTP_PROXY=", "ALL_PROXY=", "https_proxy=", "http_proxy=", "all_proxy=", "NO_PROXY=", "no_proxy="}) || process.Privileged {
		t.Fatal("proxy or privilege in control")
	}
	process.Env = codexProcessCreateOptions().Env
	if !reflect.DeepEqual(process, codexProcessCreateOptions()) {
		t.Fatal("fixed app-server command changed")
	}
}

func TestDirectControlSafeRoutingError(t *testing.T) {
	s := &directControlFakeSession{threadFake: threadFake{messages: []string{handshakeResponse, `{"id":99,"error":{"code":-32603,"message":"workspace routing discovery failed"}}`}}}
	report, err := runDirectControl("1", func() (directControlSession, error) { return s, nil })
	if err != nil || !report.Initialize || report.Account || report.Classification != "workspace_routing" || report.RPCCode != "-32603" || report.SafeMessage != "workspace routing discovery failed" || !s.closed {
		t.Fatal("safe routing classification lost")
	}
}

func TestDirectControlAuthLifecycleSuccess(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	source := &directControlSource{material: []byte("SYNTHETIC_SECRET")}
	driver := &directControlLifecycleFake{fakeCodexProcessDocker: fakeCodexProcessDocker{output: reader}}
	session, err := startDirectControl(context.Background(), driver, source, DockerEnvironmentConfig{workspace: "/diagnostic/workspace"})
	if err != nil {
		t.Fatal("diagnostic startup failed")
	}
	if session.Close() != nil || !driver.prepared || !reflect.DeepEqual(driver.calls, []string{"start", "closeWrite", "stop", "remove"}) {
		t.Fatal("diagnostic cleanup failed")
	}
	for _, b := range source.material {
		if b != 0 {
			t.Fatal("auth buffer retained")
		}
	}
	<-session.pumpDone
}

type directControlSource struct{ material []byte }

func (s *directControlSource) obtain(context.Context) ([]byte, error) { return s.material, nil }

type directControlLifecycleFake struct {
	fakeCodexProcessDocker
	stage    string
	config   DockerEnvironmentConfig
	prepared bool
}

func (d *directControlLifecycleFake) createCodexContainer(_ context.Context, c DockerEnvironmentConfig) (string, error) {
	d.config = c
	if d.stage == "create" {
		return "container", errors.New("SECRET")
	}
	return "container", nil
}
func (d *directControlLifecycleFake) Start(context.Context, string) error {
	if d.stage == "start" {
		return errors.New("SECRET")
	}
	return nil
}
func (d *directControlLifecycleFake) prepareAuth(_ context.Context, _ string, target string, material []byte) error {
	d.prepared = target == "/run/codex-auth/auth.json" && string(material) == "SYNTHETIC_SECRET"
	if d.stage == "auth" {
		return errors.New("SECRET")
	}
	return nil
}

func TestDirectControlAuthLifecycleFailures(t *testing.T) {
	for _, stage := range []string{"create", "start", "auth", "process"} {
		source := &directControlSource{material: []byte("SYNTHETIC_SECRET")}
		driver := &directControlLifecycleFake{stage: stage, fakeCodexProcessDocker: fakeCodexProcessDocker{startErr: errors.New("SECRET")}}
		_, err := startDirectControl(context.Background(), driver, source, DockerEnvironmentConfig{workspace: "/diagnostic/workspace"})
		if err == nil || strings.Contains(err.Error(), "SECRET") || !driver.config.authTmpfs || !reflect.DeepEqual(driver.calls, func() []string {
			if stage == "process" {
				return []string{"start", "remove"}
			}
			return []string{"remove"}
		}()) {
			t.Fatal("unsafe failure or missing cleanup")
		}
		for _, b := range source.material {
			if b != 0 {
				t.Fatal("auth buffer retained")
			}
		}
		if (stage == "auth" || stage == "process") && !driver.prepared {
			t.Fatal("auth not materialized through existing seam")
		}
	}
}
