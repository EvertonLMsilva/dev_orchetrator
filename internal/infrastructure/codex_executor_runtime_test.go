package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
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
	return ports.RuntimeExecutionRequest{ProjectID: "project", TaskID: "task", Workspace: `C:\private\host`, Objective: "fix $(shell)", Scope: []string{"src/a.go"}, Constraints: []string{"no network"}, AcceptanceCriteria: []string{"tests pass"}}
}
func executionFake(events ...string) *executorSessionFake {
	return &executorSessionFake{threadFake: threadFake{messages: events}, closed: make(chan struct{})}
}
func executeFake(ctx context.Context, f *executorSessionFake) (ports.RuntimeExecutionResult, error) {
	return NewCodexExecutorRuntime(func(context.Context) (CodexExecutorSession, error) { return f, nil }).Execute(ctx, executionRequest())
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
	_, err := NewCodexExecutorRuntime(func(context.Context) (CodexExecutorSession, error) { called = true; return nil, nil }).Execute(ctx, executionRequest())
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal("pre-cancelled context started session")
	}
}
func TestCodexExecutorStartupAndCleanupFailure(t *testing.T) {
	f := executionFake()
	got, err := NewCodexExecutorRuntime(func(context.Context) (CodexExecutorSession, error) { return f, errors.New("startup-secret") }).Execute(context.Background(), executionRequest())
	if err == nil || got != (ports.RuntimeExecutionResult{}) || f.closes != 1 || strings.Contains(err.Error(), "startup-secret") {
		t.Fatal("startup failure")
	}
	f = executionFake(handshakeResponse, threadResponse, turnResponse, turnEvent("completed"))
	f.closeErr = errors.New("cleanup-secret")
	got, err = executeFake(context.Background(), f)
	if err == nil || got != (ports.RuntimeExecutionResult{}) || strings.Contains(err.Error(), "cleanup-secret") {
		t.Fatal("cleanup failure")
	}
	for _, factory := range []CodexExecutorSessionFactory{nil, func(context.Context) (CodexExecutorSession, error) { return nil, nil }} {
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
	got, err := NewCodexExecutorRuntime(func(context.Context) (CodexExecutorSession, error) {
		cancel()
		return f, nil
	}).Execute(ctx, executionRequest())
	if !errors.Is(err, context.Canceled) || got != (ports.RuntimeExecutionResult{}) || f.closes != 1 || len(f.writes) != 0 {
		t.Fatalf("creation cancellation: %#v %v", got, err)
	}
}
