package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestAuthenticatedCodexRequestsPrivateStorage(t *testing.T) {
	opts := codexSessionCreateOptions(DockerEnvironmentConfig{workspace: "/trusted/project", authTmpfs: true})
	for _, m := range opts.HostConfig.Mounts {
		if m.Target == "/run/codex-auth" && m.TmpfsOptions != nil && m.TmpfsOptions.Mode == 0700 {
			return
		}
	}
	t.Fatal("authenticated Codex session did not request private auth tmpfs")
}

func TestCodexEgressPolicy(t *testing.T) {
	for _, hosts := range [][]string{nil, {}, {"*.openai.com"}, {"127.0.0.1"}, {"chatgpt.com\nConnectPort 80"}, {"https://chatgpt.com"}, {"chatgpt.com:443"}, {"CHATGPT.COM"}} {
		if _, _, err := codexProxyPolicy(hosts); err == nil {
			t.Fatal("invalid allowlist accepted")
		}
	}
	config, filter, err := codexProxyPolicy([]string{"chatgpt.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, directive := range []string{"ConnectPort 443\n", "FilterDefaultDeny Yes\n", "FilterURLs On\n", "FilterType ere\n"} {
		if !strings.Contains(config, directive) {
			t.Fatal("missing deny policy")
		}
	}
	rule := regexp.MustCompile(strings.TrimSpace(filter))
	for _, tc := range []struct {
		destination string
		allowed     bool
	}{
		{"chatgpt.com:443", true}, // HTTPS and WSS use the same CONNECT tunnel.
		{"unknown.example:443", false}, {"chatgpt.com:80", false}, {"chatgpt.com:444", false},
		{"http://chatgpt.com/", false}, {"http://chatgpt.com:443/", false},
		{"sub.chatgpt.com:443", false}, {"chatgpt.com.evil:443", false},
		{"chatgpt.com:443/path", false}, {"user@chatgpt.com:443", false},
	} {
		if rule.MatchString(tc.destination) != tc.allowed {
			t.Fatal("destination authorization mismatch")
		}
	}
	// Redirect destination is a fresh CONNECT; it cannot inherit approval.
	if !rule.MatchString("chatgpt.com:443") || rule.MatchString("redirect.example:443") {
		t.Fatal("redirect bypass")
	}
}

func TestAuthenticatedDockerSpecifications(t *testing.T) {
	config, filter, _ := codexProxyPolicy([]string{"chatgpt.com"})
	p := codexProxyCreateOptions("private-id", "external-id", config, filter)
	if p.Config.User != "65532:65532" || !p.HostConfig.ReadonlyRootfs || p.HostConfig.Privileged || p.HostConfig.NetworkMode != "private-id" || len(p.HostConfig.Mounts) != 0 || !reflect.DeepEqual(p.HostConfig.CapDrop, []string{"ALL"}) {
		t.Fatal("unsafe proxy spec")
	}
	if len(p.NetworkingConfig.EndpointsConfig) != 2 || p.NetworkingConfig.EndpointsConfig["private-id"] == nil || p.NetworkingConfig.EndpointsConfig["external-id"] == nil {
		t.Fatal("proxy topology")
	}
	c := authenticatedCodexCreateOptions(DockerEnvironmentConfig{workspace: "/trusted/project", authTmpfs: true}, "private-id")
	if c.HostConfig.NetworkMode != "private-id" || c.NetworkingConfig != nil || c.HostConfig.Privileged || len(c.HostConfig.Mounts) != 2 {
		t.Fatal("workload can bypass proxy")
	}
	if !reflect.DeepEqual(c.HostConfig.DNS, []netip.Addr{netip.MustParseAddr("127.0.0.1")}) {
		t.Fatal("workload has external DNS upstream")
	}
	if c.HostConfig.Mounts[0].Source != "/trusted/project" || c.HostConfig.Mounts[1].Source != "" {
		t.Fatal("extra host mount")
	}
	process := authenticatedCodexProcessOptions()
	if process.Env[0] != "CODEX_HOME=/run/codex-auth" {
		t.Fatal("wrong auth home")
	}
	for _, spec := range []any{p, c, process} {
		encoded, err := json.Marshal(spec)
		if err != nil || strings.Contains(string(encoded), "TEST_SECRET_DO_NOT_LEAK") {
			t.Fatal("secret in specification")
		}
	}
	if !reflect.DeepEqual([]string(process.Cmd), []string{"codex", "app-server", "--listen", "stdio://"}) {
		t.Fatal("caller controlled args")
	}
	for _, env := range process.Env {
		if strings.Contains(env, "TOKEN") || strings.Contains(env, "KEY") || strings.Contains(env, "TEST_SECRET") {
			t.Fatal("secret env")
		}
	}
	if normal := codexSessionCreateOptions(DockerEnvironmentConfig{workspace: "/trusted/project"}); normal.HostConfig.NetworkMode != "none" || len(normal.HostConfig.Mounts) != 1 || len(normal.Config.Env) != 0 {
		t.Fatal("normal runtime changed")
	}
}

type authCompositionDocker struct {
	*compositionDockerFake
	proxyErr, authErr error
	material          []byte
}

func (d *authCompositionDocker) prepareCodexEgress(_ context.Context, hosts []string) (codexEgressSession, error) {
	d.record("egress")
	if _, _, err := codexProxyPolicy(hosts); err != nil {
		return nil, err
	}
	if d.proxyErr != nil {
		return nil, d.proxyErr
	}
	return d, nil
}
func (d *authCompositionDocker) Remove(context.Context, string) error {
	d.record("remove")
	return d.removeErr
}
func (d *authCompositionDocker) prepareAuth(_ context.Context, _ string, target string, material []byte) error {
	d.record("auth")
	if target != "/run/codex-auth/auth.json" || string(material) != testManagedAuth {
		return errors.New("bad materialization")
	}
	d.material = material
	return d.authErr
}

func TestAuthenticatedSessionLifecycle(t *testing.T) {
	for _, stage := range []string{"success", "missing-auth", "missing-allowlist", "proxy", "create", "partial-create", "start", "auth", "process", "cleanup", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			home := t.TempDir()
			if stage != "missing-auth" {
				if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(testManagedAuth), 0600); err != nil {
					t.Fatal(err)
				}
			}
			source, err := NewAuthorizedCodexHome(home, true)
			if err != nil {
				t.Fatal(err)
			}
			reader, writer := io.Pipe()
			defer writer.Close()
			d := &authCompositionDocker{compositionDockerFake: &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{output: reader}}}
			secretErr := errors.New("TEST_SECRET_DO_NOT_LEAK")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hosts := []string{"chatgpt.com"}
			switch stage {
			case "missing-allowlist":
				hosts = nil
			case "proxy":
				d.proxyErr = secretErr
			case "create":
				d.createErr = secretErr
			case "partial-create":
				d.createErr = secretErr
				d.partial = true
			case "start":
				d.containerStartErr = secretErr
			case "auth":
				d.authErr = secretErr
			case "process":
				d.startErr = secretErr
			case "cleanup":
				d.authErr = secretErr
				d.removeErr = secretErr
			case "cancel":
				d.cancelStart = cancel
			}
			env := &DockerExecutionEnvironment{docker: d, authRequired: true, authSource: source, allowedHosts: hosts}
			session, err := env.startCodexSession(ctx, DockerEnvironmentConfig{workspace: "/trusted/project"})
			if stage == "success" {
				if err != nil || session == nil {
					t.Fatal("authenticated session rejected")
				}
				if !d.config.authTmpfs {
					t.Fatal("private storage/network missing")
				}
				if session.Close() != nil {
					t.Fatal("cleanup failed")
				}
			} else if err == nil {
				t.Fatal("failure accepted")
			}
			if err != nil && strings.Contains(err.Error(), "TEST_SECRET") {
				t.Fatal("secret error")
			}
			for _, b := range d.material {
				if b != 0 {
					t.Fatal("sensitive buffer retained")
				}
			}
			if stage == "missing-auth" && len(d.calls) != 0 {
				t.Fatal("missing auth started resource")
			}
			if stage != "missing-auth" && stage != "missing-allowlist" && stage != "proxy" && d.calls[len(d.calls)-1] != "remove" {
				t.Fatal("cleanup skipped")
			}
		})
	}
}
