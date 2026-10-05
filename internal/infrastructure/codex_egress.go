package infrastructure

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const codexProxyImage = "dev-orchestrator-egress-proxy:1.11.3"
const codexProxyURL = "http://codex-egress:8888"

type codexEgressSession interface {
	codexSessionDocker
}
type codexEgressFactory interface {
	prepareCodexEgress(context.Context, []string) (codexEgressSession, error)
}

// Trusted infrastructure configuration only. No application/model configuration
// or implicit home, hostname, proxy, image or credential discovery is accepted.
func NewAuthenticatedDockerCodexExecutorRuntime(driver *DockerDriver, home *AuthorizedCodexHome, hosts []string) (*CodexExecutorRuntime, error) {
	if driver == nil || driver.client == nil || home == nil {
		return nil, errors.New("authenticated runtime dependencies required")
	}
	if _, _, err := codexProxyPolicy(hosts); err != nil {
		return nil, err
	}
	env := &DockerExecutionEnvironment{docker: driver, authRequired: true, authSource: home, allowedHosts: append([]string(nil), hosts...)}
	runtime := NewCodexExecutorRuntime(env.startCodexSession)
	runtime.authenticated = true
	return runtime, nil
}

var codexHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)

func codexProxyPolicy(hosts []string) (string, string, error) {
	if len(hosts) == 0 || len(hosts) > 32 {
		return "", "", errors.New("explicit egress allowlist required")
	}
	var filter strings.Builder
	for _, host := range hosts {
		if len(host) > 253 || !codexHostname.MatchString(host) || regexp.MustCompile(`^[0-9.]+$`).MatchString(host) {
			return "", "", errors.New("exact egress hostname required")
		}
		// FilterURLs evaluates the raw CONNECT authority. Anchors reject HTTP
		// URLs, alternate ports, IPs, userinfo, path suffixes and subdomains.
		filter.WriteString("^" + strings.ReplaceAll(host, ".", `\.`) + ":443$\n")
	}
	config := "Port 8888\nTimeout 30\nMaxClients 16\nLogLevel Connect\nPidFile \"/run/proxy/tinyproxy.pid\"\nConnectPort 443\nFilter \"/run/proxy/allowlist\"\nFilterURLs On\nFilterType ere\nFilterCaseSensitive On\nFilterDefaultDeny Yes\n"
	return config, filter.String(), nil
}

func codexProxyCreateOptions(private, external, config, filter string) client.ContainerCreateOptions {
	return client.ContainerCreateOptions{
		Config:           &container.Config{Image: codexProxyImage, User: "65532:65532", Cmd: []string{"/bin/sh", "-ec", `printf '%s' "$PROXY_CONFIG" > /run/proxy/tinyproxy.conf; printf '%s' "$PROXY_FILTER" > /run/proxy/allowlist; /usr/local/bin/tinyproxy -d -c /run/proxy/tinyproxy.conf 2>/dev/null | /usr/local/bin/denied-hosts`}, Env: []string{"PROXY_CONFIG=" + config, "PROXY_FILTER=" + filter}},
		HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(private), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Tmpfs: map[string]string{"/run/proxy": "rw,noexec,nosuid,nodev,size=1m,mode=0700,uid=65532,gid=65532"}, LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}}},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{private: {Aliases: []string{"codex-egress"}}, external: {GwPriority: 1}}},
		Platform:         &ocispec.Platform{OS: "linux"},
	}
}

// Only sanitized DNS names leave infrastructure, solely for opt-in diagnostics.
type codexProxyDiagnostic struct {
	Host              string
	Port              int
	Decision          string
	DNSResolution     string
	UpstreamConnect   string
	TunnelEstablished string
}

// Decision describes the exact CONNECT policy, not successful TLS or routing.
func safeProxyDiagnostics(logs string, hosts []string) []codexProxyDiagnostic {
	var result []codexProxyDiagnostic
	for _, line := range strings.Split(logs, "\n") {
		fields := strings.Fields(line)
		if len(result) >= 128 {
			break
		}
		if (len(fields) != 3 && len(fields) != 6) || (fields[0] != "CONNECT_HOST" && fields[0] != "CONNECT_STATE") || len(fields[1]) > 253 || !codexHostname.MatchString(fields[1]) || regexp.MustCompile(`^[0-9.]+$`).MatchString(fields[1]) {
			continue
		}
		port, err := strconv.Atoi(fields[2])
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		decision := "DENY"
		for _, host := range hosts {
			if port == 443 && host == fields[1] {
				decision = "ALLOW"
			}
		}
		if fields[0] == "CONNECT_STATE" {
			if len(fields) != 6 || port != 443 {
				continue
			}
			state := strings.Join(fields[3:], " ")
			if state != "SUCCESS SUCCESS UNKNOWN" && state != "SUCCESS FAIL NO" && state != "FAIL UNKNOWN NO" {
				continue
			}
			// Do not associate host-only outcomes with overlapping/retried requests:
			// emit separate observations, preserving unknown where correlation is absent.
			result = append(result, codexProxyDiagnostic{fields[1], port, decision, fields[3], fields[4], fields[5]})
			continue
		}
		if len(fields) != 3 {
			continue
		}
		tunnel := "UNKNOWN"
		if decision == "DENY" {
			tunnel = "NO"
		}
		result = append(result, codexProxyDiagnostic{fields[1], port, decision, "UNKNOWN", "UNKNOWN", tunnel})
	}
	return result
}

func (o *ownedCodexEgress) proxyDiagnostics(ctx context.Context, hosts []string) ([]codexProxyDiagnostic, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	logs, err := o.client.ContainerLogs(ctx, o.proxy, client.ContainerLogsOptions{ShowStdout: true, Tail: "128"})
	if err != nil {
		return nil, errors.New("proxy diagnostics unavailable")
	}
	defer logs.Close()
	var output bytes.Buffer
	if err := copyCodexProcessStdout(&output, io.LimitReader(logs, 64*1024)); err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("proxy diagnostics unavailable")
	}
	return safeProxyDiagnostics(output.String(), hosts), nil
}

func (o *ownedCodexEgress) blockedDestination(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	logs, err := o.client.ContainerLogs(ctx, o.proxy, client.ContainerLogsOptions{ShowStdout: true, Tail: "128"})
	if err != nil {
		return ""
	}
	defer logs.Close()
	var output bytes.Buffer
	_ = copyCodexProcessStdout(&output, io.LimitReader(logs, 64*1024))
	for _, line := range strings.Split(output.String(), "\n") {
		if strings.HasPrefix(line, "DENIED_HOST ") {
			host := strings.TrimPrefix(line, "DENIED_HOST ")
			if len(host) <= 253 && codexHostname.MatchString(host) {
				return host
			}
		}
	}
	return ""
}

type ownedCodexEgress struct {
	*DockerDriver
	private, external, proxy string
	once                     sync.Once
	cleanupErr               error
}

func (d *DockerDriver) prepareCodexEgress(ctx context.Context, hosts []string) (codexEgressSession, error) {
	config, filter, err := codexProxyPolicy(hosts)
	if err != nil || d == nil || d.client == nil {
		return nil, errors.New("controlled egress configuration required")
	}
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	o := &ownedCodexEgress{DockerDriver: d}
	failed := func() (codexEgressSession, error) {
		failure := errors.New("controlled egress provisioning failed")
		return nil, errors.Join(failure, o.Remove(context.Background(), ""))
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return failed()
	}
	name := "codex-egress-" + hex.EncodeToString(nonce[:])
	for _, internal := range []bool{true, false} {
		suffix := "-external"
		if internal {
			suffix = "-private"
		}
		result, err := d.client.NetworkCreate(ctx, name+suffix, client.NetworkCreateOptions{Driver: "bridge", Internal: internal})
		if internal {
			o.private = result.ID
		} else {
			o.external = result.ID
		}
		if err != nil || result.ID == "" {
			return failed()
		}
	}
	created, err := d.client.ContainerCreate(ctx, codexProxyCreateOptions(o.private, o.external, config, filter))
	o.proxy = created.ID
	if err != nil || o.proxy == "" {
		return failed()
	}
	if err := d.Start(ctx, o.proxy); err != nil {
		return failed()
	}
	// Probe the listening socket without any external request or log retention.
	if d.authProbe(ctx, o.proxy, []string{"/usr/bin/timeout", "3", "/bin/bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/8888"}) != nil {
		return failed()
	}
	return o, nil
}

func (o *ownedCodexEgress) proxyHealthy(ctx context.Context) bool {
	result, err := o.client.ContainerInspect(ctx, o.proxy, client.ContainerInspectOptions{})
	return err == nil && result.Container.State != nil && result.Container.State.Running
}
func (o *ownedCodexEgress) createCodexContainer(ctx context.Context, c DockerEnvironmentConfig) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	if !c.authTmpfs || o.private == "" || !o.proxyHealthy(ctx) {
		return "", errors.New("controlled egress unavailable")
	}
	created, err := o.client.ContainerCreate(ctx, authenticatedCodexCreateOptions(c, o.private))
	if err != nil {
		return created.ID, errors.New("authenticated codex container creation failed")
	}
	return created.ID, nil
}
func authenticatedCodexCreateOptions(c DockerEnvironmentConfig, private string) client.ContainerCreateOptions {
	opts := codexSessionCreateOptions(c)
	opts.HostConfig.NetworkMode = container.NetworkMode(private)
	// Docker's embedded DNS retains the private proxy alias. External queries
	// have only the workload's loopback as upstream, never host/external DNS.
	opts.HostConfig.DNS = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	return opts
}
func authenticatedCodexProcessOptions() client.ExecCreateOptions {
	opts := codexProcessCreateOptions()
	opts.Env = []string{"CODEX_HOME=/run/codex-auth", "HTTPS_PROXY=" + codexProxyURL, "HTTP_PROXY=" + codexProxyURL, "https_proxy=" + codexProxyURL, "http_proxy=" + codexProxyURL, "NO_PROXY=", "no_proxy="}
	return opts
}
func (o *ownedCodexEgress) startCodexProcess(ctx context.Context, id string) (codexProcessAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	if !o.proxyHealthy(ctx) {
		return codexProcessAttachment{}, errors.New("controlled egress unavailable")
	}
	created, err := o.client.ExecCreate(ctx, id, authenticatedCodexProcessOptions())
	if err != nil {
		return codexProcessAttachment{}, errors.New("authenticated codex exec failed")
	}
	attached, err := o.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return codexProcessAttachment{}, errors.New("authenticated codex attach failed")
	}
	return codexProcessAttachment{execID: created.ID, input: codexProcessStdin{attached.Conn}, output: attached.Reader, close: attached.Close, closeWrite: attached.CloseWrite}, nil
}
func (o *ownedCodexEgress) Remove(_ context.Context, id string) error {
	o.once.Do(func() {
		// Each operation gets its own bounded context. Never remove preexisting
		// resources or skip remaining cleanup after an earlier failure.
		for _, containerID := range []string{id, o.proxy} {
			if containerID == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
			if err := o.DockerDriver.Remove(ctx, containerID); err != nil {
				o.cleanupErr = errors.New("authenticated session cleanup failed")
			}
			cancel()
		}
		for _, networkID := range []string{o.private, o.external} {
			if networkID == "" {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
			if _, err := o.client.NetworkRemove(ctx, networkID, client.NetworkRemoveOptions{}); err != nil {
				o.cleanupErr = errors.New("authenticated session cleanup failed")
			}
			cancel()
		}
	})
	return o.cleanupErr
}
