package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"dev-orchestrator/internal/ports"
)

// CodexExecutorSession is an infrastructure-only, owned transport. Close must
// unblock pending Read/Write calls and finish releasing the session resources.
type CodexExecutorSession interface {
	Write([]byte) error
	Read() ([]byte, error)
	Close() error
}

// CodexExecutorSessionFactory creates one controlled session per execution.
// It must respect ctx and release resources on failure, or return the partially
// created session for cleanup. No live factory is wired by this composition.
type CodexExecutorSessionFactory func(context.Context) (CodexExecutorSession, error)

type CodexExecutorRuntime struct{ start CodexExecutorSessionFactory }

var _ ports.ExecutorRuntime = (*CodexExecutorRuntime)(nil)
var _ CodexExecutorSession = (*CodexProcessTransport)(nil)

func NewCodexExecutorRuntime(start CodexExecutorSessionFactory) *CodexExecutorRuntime {
	return &CodexExecutorRuntime{start: start}
}

func (r *CodexExecutorRuntime) Execute(ctx context.Context, request ports.RuntimeExecutionRequest) (result ports.RuntimeExecutionResult, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	if r == nil || r.start == nil {
		return result, errors.New("codex executor session factory required")
	}
	session, startErr := r.start(ctx)
	if session == nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		return result, errors.New("codex executor session creation failed")
	}
	var once sync.Once
	var closeErr error
	cleanup := func() {
		once.Do(func() {
			if session.Close() != nil {
				closeErr = errors.New("codex executor cleanup failed")
			}
		})
	}
	stopped := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			cleanup()
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		cleanup()
		<-watcherDone
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		err = errors.Join(err, closeErr)
		if err != nil {
			result = ports.RuntimeExecutionResult{}
		}
	}()
	if startErr != nil {
		return result, errors.New("codex executor session creation failed")
	}
	transport := codexContextTransport{ctx: ctx, session: session}
	ready, handshakeErr := completeCodexHandshake(transport)
	if handshakeErr != nil {
		return result, errors.New("codex executor handshake failed")
	}
	thread, threadErr := ready.StartThread()
	if threadErr != nil {
		return result, errors.New("codex executor thread start failed")
	}
	// A typed JSON projection is deterministic and preserves declarative strings.
	// Host workspace and canonical correlation IDs are never model input.
	prompt, _ := json.Marshal(struct {
		Objective          string
		Scope              []string
		Constraints        []string
		AcceptanceCriteria []string
		Workspace          string
	}{request.Objective, request.Scope, request.Constraints, request.AcceptanceCriteria, codexProtocolWorkspace})
	turn, turnErr := thread.StartTurn(string(prompt))
	if turnErr != nil {
		return result, errors.New("codex executor turn protocol failed")
	}
	switch turn.Status {
	case CodexTurnCompletedStatus:
		result = ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeDone, Summary: turn.Text}
		if strings.TrimSpace(result.Summary) == "" {
			result.Summary = "Codex turn completed."
		}
	case CodexTurnFailed:
		result = ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeFailed, Summary: "Codex turn failed."}
	case CodexTurnInterrupted:
		result = ports.RuntimeExecutionResult{Outcome: ports.ExecutorOutcomeFailed, Summary: "Codex turn interrupted."}
	default:
		return result, errors.New("codex executor invalid terminal result")
	}
	return
}

type codexContextTransport struct {
	ctx     context.Context
	session CodexExecutorSession
}

func (t codexContextTransport) Write(b []byte) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	return t.session.Write(b)
}
func (t codexContextTransport) Read() ([]byte, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	return t.session.Read()
}
