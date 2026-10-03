package discord

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"

	sdk "github.com/disgoorg/disgo/discord"
)

var _ continueService = (*application.ContinueService)(nil)

type commandContinue struct {
	calls int
	ctx   context.Context
	id    domain.TaskID
	err   error
}

func (s *commandContinue) ContinueTask(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	s.calls++
	s.ctx, s.id = ctx, id
	return domain.Task{ID: id, Status: domain.TaskStatusReadyForAnalysis}, s.err
}

func TestContinueInteractions(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "Task: task-123\nStatus: READY_FOR_ANALYSIS"},
		{"missing", application.ErrTaskNotFound, "Task not found."},
		{"cannot continue", application.ErrTaskCannotContinue, "Task cannot continue from its current status."},
		{"wrapped", fmt.Errorf("wrapped: %w", application.ErrTaskCannotContinue), "Task cannot continue from its current status."},
		{"internal", errors.New("secret repository detail"), "Internal error."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &commandREST{}
			s := &commandContinue{err: tc.err}
			q := &commandQueries{}
			g := &Gateway{commands: r}
			g.SetContinueService(s)
			g.SetQueryService(q)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := g.handleInteraction(ctx, commandEvent(t, "continue", `[{"name":"task","type":3,"value":"task-123"}]`)); err != nil {
				t.Fatal(err)
			}
			if s.calls != 1 || s.ctx != ctx || s.id != "task-123" || q.calls != 0 {
				t.Fatal("wrong service dispatch")
			}
			msg, ok := r.response.Data.(sdk.MessageCreate)
			if !ok || msg.Content != tc.want || strings.Contains(msg.Content, "secret") || msg.Flags != sdk.MessageFlagEphemeral || r.responses != 1 || r.response.Type != sdk.InteractionResponseTypeCreateMessage {
				t.Fatalf("wrong response: %+v", r.response)
			}
		})
	}
}

func TestContinueInteractionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		options    string
		configured bool
	}{
		{`[]`, true}, {`[{"name":"task","type":4,"value":1}]`, true}, {`[{"name":"task","type":3,"value":"t"}]`, false},
	} {
		r := &commandREST{}
		s := &commandContinue{}
		g := &Gateway{commands: r}
		if tc.configured {
			g.SetContinueService(s)
		}
		if err := g.handleInteraction(context.Background(), commandEvent(t, "continue", tc.options)); err != nil {
			t.Fatal(err)
		}
		if s.calls != 0 || r.responses != 1 || r.response.Data.(sdk.MessageCreate).Content != "Internal error." {
			t.Fatal("unexpected service call/response")
		}
	}
}
