package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dev-orchestrator/internal/ports"
)

type executorSessionFake struct {
	threadFake
	closed   chan struct{}
	once     sync.Once
	closes   int
	blockAt  int
	entered  chan struct{}
	closeErr error
}

type compositionDockerFake struct {
	*fakeCodexProcessDocker
	config                       DockerEnvironmentConfig
	createErr, containerStartErr error
	partial                      bool
	cancelCreate, cancelStart    context.CancelFunc
	emptyID                      bool
}

func (d *compositionDockerFake) createCodexContainer(ctx context.Context, config DockerEnvironmentConfig) (string, error) {
	d.record("create")
	d.config = config
	if d.cancelCreate != nil {
		d.cancelCreate()
	}
	if d.emptyID {
		return "", nil
	}
	if d.createErr != nil && !d.partial {
		return "", d.createErr
	}
	return "container", d.createErr
}
func (d *compositionDockerFake) Create(context.Context, DockerEnvironmentConfig) (string, error) {
	panic("probe fallback")
}
func (d *compositionDockerFake) Start(context.Context, string) error {
	d.record("container-start")
	if d.cancelStart != nil {
		d.cancelStart()
	}
	return d.containerStartErr
}
func (d *compositionDockerFake) Stop(context.Context, string) error { panic("probe fallback") }

func TestDockerCodexSessionComposition(t *testing.T) {
	for _, workspace := range []string{"/trusted/workspace-a", "/trusted/workspace-b"} {
		t.Run(workspace, func(t *testing.T) {
			reader, writer := io.Pipe()
			defer writer.Close()
			d := &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{output: reader}}
			env := NewDockerExecutionEnvironment(d)
			runtime := NewCodexExecutorRuntime(env.startCodexSession)
			go func() {
				for _, event := range []string{handshakeResponse, threadResponse, turnResponse, turnDelta("done"), turnEvent("completed")} {
					if _, err := writer.Write(codexDockerFrame(1, event+"\n")); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			request := executionRequest()
			request.Workspace = workspace
			result, err := runtime.Execute(ctx, request)
			if err != nil || result.Outcome != ports.ExecutorOutcomeDone || result.Summary != "done" {
				t.Fatalf("%#v %v", result, err)
			}
			if d.config.WorkspaceSource() != workspace || d.config.WorkspaceTarget() != "/workspace" || d.config.WorkingDirectory() != "/workspace" || d.config.Privileged() || d.config.AuthTmpfsTarget() != "" {
				t.Fatal("unsafe binding")
			}
			if !reflect.DeepEqual(d.calls, []string{"create", "container-start", "start", "closeWrite", "stop", "remove"}) {
				t.Fatal(d.calls)
			}
		})
	}
}

func TestDockerCodexSessionFailures(t *testing.T) {
	for _, stage := range []string{"create", "partial-create", "container-start", "process", "remove"} {
		t.Run(stage, func(t *testing.T) {
			d := &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{}}
			failure := errors.New("private daemon detail")
			switch stage {
			case "create":
				d.createErr = failure
			case "partial-create":
				d.createErr = failure
				d.partial = true
			case "container-start":
				d.containerStartErr = failure
			case "process":
				d.startErr = failure
			case "remove":
				d.containerStartErr = failure
				d.removeErr = failure
			}
			env := NewDockerExecutionEnvironment(d)
			_, err := NewCodexExecutorRuntime(env.startCodexSession).Execute(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
			if err == nil || strings.Contains(err.Error(), "private daemon detail") {
				t.Fatal("failure must remain opaque")
			}
			if stage != "create" && d.calls[len(d.calls)-1] != "remove" {
				t.Fatal("cleanup skipped", d.calls)
			}
		})
	}
}

func TestDockerCodexSessionCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	d := &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{output: reader}}
	env := NewDockerExecutionEnvironment(d)
	ctx, cancel := context.WithCancel(context.Background())
	session, err := env.startCodexSession(ctx, DockerEnvironmentConfig{workspace: "/trusted/project"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := session.Read(); err == nil {
		t.Fatal("read was not canceled")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if d.calls[len(d.calls)-1] != "remove" {
		t.Fatal(d.calls)
	}
}

func TestDockerCodexSessionCreationGuards(t *testing.T) {
	for _, workspace := range []string{"", "relative", "/", "/trusted/..", "/var/run/docker.sock", "/run/docker.sock", "/trusted/\x00project"} {
		d := &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{}}
		env := NewDockerExecutionEnvironment(d)
		if _, err := NewCodexExecutorRuntime(env.startCodexSession).Execute(context.Background(), ports.RuntimeExecutionRequest{Workspace: workspace}); err == nil || len(d.calls) != 0 {
			t.Fatalf("unsafe workspace %q: %v", workspace, err)
		}
	}
	for _, stage := range []string{"create", "start"} {
		ctx, cancel := context.WithCancel(context.Background())
		d := &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{}}
		if stage == "create" {
			d.cancelCreate = cancel
		} else {
			d.cancelStart = cancel
		}
		env := NewDockerExecutionEnvironment(d)
		_, err := env.startCodexSession(ctx, DockerEnvironmentConfig{workspace: "/trusted/project"})
		cancel()
		if !errors.Is(err, context.Canceled) || d.calls[len(d.calls)-1] != "remove" {
			t.Fatalf("%s cancellation: %v %v", stage, err, d.calls)
		}
		for _, call := range d.calls {
			if call == "start" {
				t.Fatal("canceled creation started process")
			}
		}
	}
	d := &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{}, emptyID: true}
	env := NewDockerExecutionEnvironment(d)
	if _, err := env.startCodexSession(context.Background(), DockerEnvironmentConfig{workspace: "/trusted/project"}); err == nil || !reflect.DeepEqual(d.calls, []string{"create"}) {
		t.Fatal("empty ID accepted")
	}
	for _, authConfig := range []bool{false, true} {
		d = &compositionDockerFake{fakeCodexProcessDocker: &fakeCodexProcessDocker{}}
		env = NewDockerExecutionEnvironment(d)
		env.authRequired = !authConfig
		if _, err := env.startCodexSession(context.Background(), DockerEnvironmentConfig{workspace: "/trusted/project", authTmpfs: authConfig}); err == nil || len(d.calls) != 0 {
			t.Fatal("authenticated session accepted")
		}
	}
	if _, err := NewDockerCodexExecutorRuntime(nil).Execute(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"}); err == nil {
		t.Fatal("nil concrete driver accepted")
	}
}

func (f *executorSessionFake) Close() error {
	f.once.Do(func() { f.closes++; close(f.closed) })
	return f.closeErr
}
func (f *executorSessionFake) Read() ([]byte, error) {
	if len(f.writes) == f.blockAt {
		close(f.entered)
		<-f.closed
		return nil, errors.New("secret transport detail")
	}
	return f.threadFake.Read()
}
func executionRequest() ports.RuntimeExecutionRequest {
	return ports.RuntimeExecutionRequest{ProjectID: "project", TaskID: "task", Workspace: `/private/host`, Objective: "fix $(shell)", Scope: []string{"src/a.go"}, Constraints: []string{"no network"}, AcceptanceCriteria: []string{"tests pass"}}
}
func executionFake(events ...string) *executorSessionFake {
	return &executorSessionFake{threadFake: threadFake{messages: events}, closed: make(chan struct{})}
}
func executeFake(ctx context.Context, f *executorSessionFake) (ports.RuntimeExecutionResult, error) {
	return NewCodexExecutorRuntime(func(context.Context, DockerEnvironmentConfig) (CodexExecutorSession, error) { return f, nil }).Execute(ctx, executionRequest())
}
func TestCodexExecutorComposition(t *testing.T) {
	for _, tc := range []struct {
		status, text, summary string
		outcome               ports.ExecutorOutcome
	}{
		{"completed", "BLOCKED: declarative text", "BLOCKED: declarative text", ports.ExecutorOutcomeDone},
		{"failed", "secret partial text", "Codex turn failed.", ports.ExecutorOutcomeFailed},
		{"interrupted", "secret partial text", "Codex turn interrupted.", ports.ExecutorOutcomeFailed},
		{"completed", "", "Codex turn completed.", ports.ExecutorOutcomeDone},
		{"completed", " \n\t", "Codex turn completed.", ports.ExecutorOutcomeDone},
		{"completed", strings.Repeat("a", codexTurnTextLimit), strings.Repeat("a", codexTurnTextLimit), ports.ExecutorOutcomeDone},
	} {
		t.Run(tc.status+"_"+string(tc.outcome), func(t *testing.T) {
			f := executionFake(handshakeResponse, threadResponse, turnResponse, turnDelta(tc.text), turnEvent(tc.status))
			got, err := executeFake(context.Background(), f)
			if err != nil || got.Outcome != tc.outcome || got.Summary != tc.summary || f.closes != 1 {
				t.Fatalf("result %#v error %v closes %d", got, err, f.closes)
			}
			methods := []string{}
			for _, b := range f.writes {
				var envelope struct{ Method string }
				if json.Unmarshal(b, &envelope) != nil {
					t.Fatal("invalid write")
				}
				methods = append(methods, envelope.Method)
			}
			if !reflect.DeepEqual(methods, []string{"initialize", "initialized", "thread/start", "turn/start"}) {
				t.Fatal(methods)
			}
			if string(f.writes[2]) != `{"id":2,"method":"thread/start","params":{"cwd":"/workspace"}}` {
				t.Fatal("cwd")
			}
			var turn struct{ Params CodexTurnStartParams }
			if json.Unmarshal(f.writes[3], &turn) != nil {
				t.Fatal("turn input")
			}
			want := `{"Objective":"fix $(shell)","Scope":["src/a.go"],"Constraints":["no network"],"AcceptanceCriteria":["tests pass"],"Workspace":"/workspace"}`
			if turn.Params.Input[0].Text != want {
				t.Fatalf("prompt %s", turn.Params.Input[0].Text)
			}
			encoded, _ := json.Marshal(got)
			for _, secret := range []string{"thread-1", "turn-1", "item-1", `C:\private`, "codexHome", "secret partial"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatal("provider leak")
				}
			}
		})
	}
}
func TestCodexExecutorFailures(t *testing.T) {
	for _, tc := range []struct {
		name         string
		events       []string
		writeFailure int
	}{
		{"handshake", []string{`{`}, 0},
		{"thread", []string{handshakeResponse, `{`}, 0},
		{"turn", []string{handshakeResponse, threadResponse, `{`}, 0},
		{"terminal", []string{handshakeResponse, threadResponse, turnResponse, turnEvent("unknown")}, 0},
		{"transport", []string{handshakeResponse, threadResponse, turnResponse}, 0},
		{"write", []string{handshakeResponse, threadResponse}, 4},
		{"limit", []string{handshakeResponse, threadResponse, turnResponse, turnDelta(strings.Repeat("a", codexTurnTextLimit+1))}, 0},
		{"rpc", []string{`{"id":1,"error":{"code":-1,"message":"credential-secret"}}`}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := executionFake(tc.events...)
			f.writeFailure = tc.writeFailure
			got, err := executeFake(context.Background(), f)
			if err == nil || got != (ports.RuntimeExecutionResult{}) || f.closes != 1 || strings.Contains(err.Error(), "credential-secret") {
				t.Fatalf("%#v %v cleanup %d", got, err, f.closes)
			}
		})
	}
}
func TestCodexExecutorCancellation(t *testing.T) {
	for _, stage := range []int{1, 3, 4} {
		t.Run(string(rune('0'+stage)), func(t *testing.T) {
			f := executionFake(handshakeResponse, threadResponse, turnResponse)
			f.blockAt = stage
			f.entered = make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				got, err := executeFake(ctx, f)
				if got != (ports.RuntimeExecutionResult{}) {
					done <- errors.New("fabricated result")
					return
				}
				done <- err
			}()
			select {
			case <-f.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("not entered")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) || f.closes != 1 {
					t.Fatalf("%v cleanup %d", err, f.closes)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("execution hung")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := NewCodexExecutorRuntime(func(context.Context, DockerEnvironmentConfig) (CodexExecutorSession, error) {
		called = true
		return nil, nil
	}).Execute(ctx, executionRequest())
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("pre-cancelled context started session")
	}
}
func TestCodexExecutorStartupAndCleanupFailure(t *testing.T) {
	f := executionFake()
	got, err := NewCodexExecutorRuntime(func(context.Context, DockerEnvironmentConfig) (CodexExecutorSession, error) {
		return f, errors.New("startup-secret")
	}).Execute(context.Background(), executionRequest())
	if err == nil || got != (ports.RuntimeExecutionResult{}) || f.closes != 1 || strings.Contains(err.Error(), "startup-secret") {
		t.Fatal("startup failure")
	}
	f = executionFake(handshakeResponse, threadResponse, turnResponse, turnEvent("completed"))
	f.closeErr = errors.New("cleanup-secret")
	got, err = executeFake(context.Background(), f)
	if err == nil || got != (ports.RuntimeExecutionResult{}) || strings.Contains(err.Error(), "cleanup-secret") {
		t.Fatal("cleanup failure")
	}
	for _, factory := range []CodexExecutorSessionFactory{nil, func(context.Context, DockerEnvironmentConfig) (CodexExecutorSession, error) { return nil, nil }} {
		if got, err := NewCodexExecutorRuntime(factory).Execute(context.Background(), executionRequest()); err == nil || got != (ports.RuntimeExecutionResult{}) {
			t.Fatal("missing seam accepted")
		}
	}
}

func TestCodexExecutorStructuredFailureSummary(t *testing.T) {
	event := strings.Replace(turnEvent("failed"), `"error":null`, `"error":{"message":"credential-secret thread-1 turn-1"}`, 1)
	f := executionFake(handshakeResponse, threadResponse, turnResponse, event)
	got, err := executeFake(context.Background(), f)
	if err != nil || got != (ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeFailed, Summary: "Codex turn failed."}) || f.closes != 1 {
		t.Fatalf("unsafe failure summary: %#v %v", got, err)
	}
}

func TestCodexExecutorCancellationAtCreation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := executionFake()
	got, err := NewCodexExecutorRuntime(func(context.Context, DockerEnvironmentConfig) (CodexExecutorSession, error) {
		cancel()
		return f, nil
	}).Execute(ctx, executionRequest())
	if !errors.Is(err, context.Canceled) || got != (ports.RuntimeExecutionResult{}) || f.closes != 1 || len(f.writes) != 0 {
		t.Fatalf("creation cancellation: %#v %v", got, err)
	}
}
