package infrastructure

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Local Docker only; no provider authentication or Internet request. Explicitly
// opted in separately from the default unit/validate suites.
func TestCodexEgressDockerOptIn(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_P6_DOCKER_TEST") != "1" {
		t.Skip("opt-in local Docker egress validation")
	}
	d, err := NewDockerDriver()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	resource, err := d.prepareCodexEgress(ctx, []string{"allowed.test"})
	if err != nil {
		t.Fatal(err)
	}
	o := resource.(*ownedCodexEgress)
	workload := ""
	defer func() {
		if err := o.Remove(context.Background(), workload); err != nil {
			t.Error(err)
		}
	}()
	// The only allowed destination is a synthetic TCP endpoint, not Internet.
	fixture, err := d.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:           &container.Config{Image: dockerProbeImage, Cmd: []string{"/bin/sh", "-ec", "while true; do printf 'local-only\\n' | nc -l -p 443; done"}},
		HostConfig:       &container.HostConfig{NetworkMode: container.NetworkMode(o.external)},
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{o.external: {Aliases: []string{"allowed.test"}}}},
		Name:             "codex-fixture-" + o.external[:12],
	})
	if err != nil {
		t.Fatal(err)
	}
	fixtureRemoved := false
	defer func() {
		if !fixtureRemoved {
			if err := d.Remove(context.Background(), fixture.ID); err != nil {
				t.Error(err)
			}
		}
	}()
	if err := d.Start(ctx, fixture.ID); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	if configured := os.Getenv("DEV_ORCHESTRATOR_INTEGRATION_WORKSPACE"); configured != "" {
		workspace = configured
	}
	workload, err = o.createCodexContainer(ctx, DockerEnvironmentConfig{workspace: workspace, authTmpfs: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Start(ctx, workload); err != nil {
		t.Fatal(err)
	}
	if err := o.prepareAuth(ctx, workload, "/run/codex-auth/auth.json", []byte(testManagedAuth)); err != nil {
		t.Fatal("synthetic auth preparation failed")
	}
	if err := d.authProbe(ctx, workload, []string{"/bin/sh", "-ec", `test "$(stat -c %a /run/codex-auth)" = 700; test "$(stat -c %a /run/codex-auth/auth.json)" = 600; grep -q ' /run/codex-auth tmpfs ' /proc/mounts; test ! -e /workspace/auth.json`}); err != nil {
		t.Fatal("private auth storage failed")
	}
	for _, tc := range []struct {
		authority string
		status    int
	}{
		{"allowed.test:443", 200}, {"unknown.test:443", 403}, {"allowed.test:80", 403},
		{"allowed.test:444", 403},
	} {
		// This tests the actual Tinyproxy parser/filter, not a Go regex imitation.
		script := `const net=require('net'); const s=net.connect(8888,'codex-egress'); s.setTimeout(3000); let b=''; s.on('connect',()=>s.write('CONNECT ` + tc.authority + ` HTTP/1.1\r\nHost: ` + tc.authority + `\r\n\r\n')); s.on('data',d=>{b+=d; if(b.includes('\r\n')){s.destroy();process.exit(b.startsWith('HTTP/1.1 ` + strconv.Itoa(tc.status) + `')||b.startsWith('HTTP/1.0 ` + strconv.Itoa(tc.status) + `')?0:1)}}); s.on('timeout',()=>process.exit(2));s.on('error',()=>process.exit(3));`
		if err := d.authProbe(ctx, workload, []string{"node", "-e", script}); err != nil {
			t.Fatalf("proxy policy %s: %v", tc.authority, err)
		}
	}
	for _, url := range []string{"http://allowed.test/", "http://allowed.test:443/", "http://allowed.test:8888/"} {
		script := `const net=require('net');const s=net.connect(8888,'codex-egress');s.setTimeout(3000);s.on('connect',()=>s.write('GET ` + url + ` HTTP/1.1\r\nHost: allowed.test\r\n\r\n'));s.on('data',d=>{s.destroy();process.exit(/^HTTP\/1\.[01] 403/.test(d.toString())?0:1)});s.on('error',()=>process.exit(2));s.on('timeout',()=>process.exit(3));`
		if err := d.authProbe(ctx, workload, []string{"node", "-e", script}); err != nil {
			t.Fatal("plain HTTP was not denied")
		}
	}
	if host := o.blockedDestination(ctx); host != "unknown.test" {
		t.Fatal("denied hostname unavailable")
	}
	diagnostics, err := o.proxyDiagnostics(ctx, []string{"allowed.test"})
	if err != nil {
		t.Fatal("proxy diagnostics unavailable")
	}
	seenAllow, seenDeny, seenPort := false, false, false
	for _, diagnostic := range diagnostics {
		seenAllow = seenAllow || diagnostic.Host == "allowed.test" && diagnostic.Port == 443 && diagnostic.Decision == "ALLOW"
		seenDeny = seenDeny || diagnostic.Host == "unknown.test" && diagnostic.Port == 443 && diagnostic.Decision == "DENY"
		seenPort = seenPort || diagnostic.Host == "allowed.test" && diagnostic.Port == 80 && diagnostic.Decision == "DENY"
	}
	if !seenAllow || !seenDeny || !seenPort {
		t.Fatal("CONNECT diagnostics missing")
	}
	// Log filter drops URLs containing a token-shaped sentinel before Docker
	// logs, while still retaining the bare denied DNS hostname above.
	leakProbe := `const net=require('net');const s=net.connect(8888,'codex-egress');s.setTimeout(3000);s.on('connect',()=>s.write('GET http://allowed.test/?token=LOG_QUERY_SENTINEL HTTP/1.1\r\nHost: allowed.test\r\n\r\n'));s.on('data',()=>{s.destroy();process.exit(0)});s.on('error',()=>process.exit(1));s.on('timeout',()=>process.exit(2));`
	if d.authProbe(ctx, workload, []string{"node", "-e", leakProbe}) != nil {
		t.Fatal("log redaction probe failed")
	}
	logs, err := d.client.ContainerLogs(ctx, o.proxy, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "128"})
	if err != nil {
		t.Fatal(err)
	}
	logBytes, err := io.ReadAll(io.LimitReader(logs, 64*1024))
	logs.Close()
	if err != nil || bytes.Contains(logBytes, []byte("TEST_SECRET_DO_NOT_LEAK")) || bytes.Contains(logBytes, []byte("LOG_QUERY_SENTINEL")) || bytes.Contains(logBytes, []byte("http://allowed.test")) {
		t.Fatal("proxy logs retained secret or URL")
	}
	inspected, err := d.client.ContainerInspect(ctx, workload, client.ContainerInspectOptions{})
	if err != nil || len(inspected.Container.NetworkSettings.Networks) != 1 {
		t.Fatal("Codex has additional network")
	}
	proxy, err := d.client.ContainerInspect(ctx, o.proxy, client.ContainerInspectOptions{})
	if err != nil || len(proxy.Container.NetworkSettings.Networks) != 2 || !proxy.Container.HostConfig.ReadonlyRootfs || proxy.Container.Config.User != "65532:65532" {
		t.Fatal("proxy isolation")
	}
	private, err := d.client.NetworkInspect(ctx, o.private, client.NetworkInspectOptions{})
	if err != nil || !private.Network.Internal {
		t.Fatal("private network has external routing")
	}
	// The workload cannot reach even the local fixture on the egress network
	// directly; only the proxy can reach it.
	endpoint, err := d.client.ContainerInspect(ctx, fixture.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range endpoint.Container.NetworkSettings.Networks {
		script := `const net=require('net');const s=net.connect(443,'` + address.IPAddress.String() + `');s.setTimeout(1000);s.on('connect',()=>process.exit(1));s.on('error',()=>process.exit(0));s.on('timeout',()=>process.exit(0));`
		if err := d.authProbe(ctx, workload, []string{"node", "-e", script}); err != nil {
			t.Fatal("direct egress succeeded")
		}
	}
	// Verify the existing stdio transport/initialize seam with synthetic auth.
	session, err := startCodexProcessRuntime(ctx, o, workload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codexHandshake(session); err != nil {
		_ = session.Close()
		t.Fatal("initialize failed")
	}
	if _, err := d.client.ContainerStop(ctx, o.proxy, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := o.startCodexProcess(ctx, workload); err == nil {
		t.Fatal("unavailable proxy accepted")
	}
	if _, err := o.createCodexContainer(ctx, DockerEnvironmentConfig{workspace: workspace, authTmpfs: true}); err == nil {
		t.Fatal("unavailable proxy created workload")
	}
	if err := d.Remove(ctx, fixture.ID); err != nil {
		t.Fatal(err)
	}
	fixtureRemoved = true
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.client.ContainerInspect(ctx, workload, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("workload cleanup")
	}
	if _, err := d.client.ContainerInspect(ctx, o.proxy, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("proxy cleanup")
	}
	for _, id := range []string{o.private, o.external} {
		if _, err := d.client.NetworkInspect(ctx, id, client.NetworkInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Fatal("network cleanup")
		}
	}
}
