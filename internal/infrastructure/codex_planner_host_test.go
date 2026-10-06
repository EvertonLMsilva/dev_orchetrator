package infrastructure

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type plannerProcessFixture struct{ output []byte }

func (d plannerProcessFixture) startCodexProcess(context.Context, string) (codexProcessAttachment, error) {
	return codexProcessAttachment{execID: "fixture", input: io.Discard, output: bytes.NewReader(d.output), close: func() {}, closeWrite: func() error { return nil }}, nil
}
func (plannerProcessFixture) stopCodexProcess(context.Context, string, string) error   { return nil }
func (plannerProcessFixture) stopCodexAppServer(context.Context, string, string) error { return nil }
func (plannerProcessFixture) Remove(context.Context, string) error                     { return nil }

func TestPlannerHostProcessCleanAndMalformedEOF(t *testing.T) {
	message := []byte("{\"version\":1,\"structuredOutput\":{}}\n")
	frame := make([]byte, 8+len(message))
	frame[0] = 1
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(message)))
	copy(frame[8:], message)
	for _, malformed := range []bool{false, true} {
		data := append([]byte(nil), frame...)
		if malformed {
			data = append(data, 1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		p, err := startPlannerHostProcessRuntime(ctx, plannerProcessFixture{output: data}, "fixture")
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		if _, err = p.Read(); err != nil {
			cancel()
			t.Fatal(err)
		}
		endErr := plannerHostEOF(p.reader)
		if (endErr == nil) == malformed {
			t.Fatalf("malformed=%v end=%v", malformed, endErr)
		}
		if p.Close() != nil {
			t.Fatal("process cleanup failed")
		}
		cancel()
	}
}

func TestPlannerHostExactEOF(t *testing.T) {
	for _, suffix := range []string{"", "\n", "{}\n", "{"} {
		err := plannerHostEOF(bufio.NewReader(strings.NewReader(suffix)))
		if (err == nil) != (suffix == "") {
			t.Fatalf("suffix accepted: %q", suffix)
		}
	}
}

func hostTestRequest() PlannerRuntimeRequest {
	return PlannerRuntimeRequest{Instructions: "Decide", Input: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Limits: PlannerRuntimeLimits{MaxOutputBytes: 1024, Timeout: time.Second}}
}

func TestPlannerHostStrictResponse(t *testing.T) {
	for _, bad := range []string{`{"Version":1,"structuredOutput":{}}`, `{"version":1,"structuredOutput":{"ok":true,"ok":false}}`} {
		if _, err := decodePlannerHostResponse([]byte(bad), 1024); err == nil {
			t.Fatalf("ambiguous response accepted: %s", bad)
		}
	}
	valid := []byte(`{"version":1,"structuredOutput":{"ok":true}}`)
	result, err := decodePlannerHostResponse(valid, 1024)
	if err != nil || string(result.StructuredOutput) != `{"ok":true}` {
		t.Fatalf("structured output: %v", err)
	}
	for _, bad := range []string{`{`, `{"version":1,"version":1,"structuredOutput":{}}`, `{"version":1,"structuredOutput":{},"tools":[]}`, `{"version":2,"structuredOutput":{}}`, `{"version":1,"error":"runtime_failure"}`, `{"version":1,"structuredOutput":{}} {}`, `{"version":1,"structuredOutput":null}`, `{"version":1,"structuredOutput":{},"error":"runtime_failure"}`} {
		if _, err := decodePlannerHostResponse([]byte(bad), 1024); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if _, err := decodePlannerHostResponse(valid, 4); err == nil {
		t.Fatal("oversized output accepted")
	}
}

func TestPlannerHostClosedRequest(t *testing.T) {
	data, err := encodePlannerHostRequest(hostTestRequest())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 5 {
		t.Fatal("request authority expanded")
	}
	for _, key := range []string{"version", "instructions", "input", "outputSchema", "limits"} {
		if fields[key] == nil {
			t.Fatal(key)
		}
	}
}

func TestPlannerHostNoProjectWorkspace(t *testing.T) {
	opts := plannerHostCreateOptions("private-network")
	if opts.Config.Image != "dev-orchestrator-codex-planner-runtime:0.159.2" {
		t.Fatal("wrong image")
	}
	for _, m := range opts.HostConfig.Mounts {
		if m.Target != "/run/codex-auth" || string(m.Type) != "tmpfs" {
			t.Fatal("project or credential bind mount")
		}
	}
	if opts.HostConfig.NetworkMode != "private-network" || len(opts.HostConfig.DNS) != 1 || opts.HostConfig.DNS[0].String() != "127.0.0.1" {
		t.Fatal("egress bypass")
	}
	original := runtimeHomeCreateOptions("private-network")
	if original.Config.Image != codexRuntimeImage {
		t.Fatal("P6.2 image changed")
	}
}

func TestPlannerHostFixedProcess(t *testing.T) {
	opts := plannerHostProcessOptions()
	if len(opts.Cmd) != 1 || opts.Cmd[0] != "/usr/local/bin/codex-planner-host" {
		t.Fatal("configurable command")
	}
	for _, e := range opts.Env {
		if e == "CODEX_HOME=/run/codex-auth" {
			return
		}
	}
	t.Fatal("missing runtime-owned auth home")
}

func TestPlannerHostCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &PlannerHostContainer{}
	if _, err := c.Infer(ctx, hostTestRequest()); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}
