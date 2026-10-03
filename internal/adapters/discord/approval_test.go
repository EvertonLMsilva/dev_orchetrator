package discord

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"errors"
	"fmt"
	sdk "github.com/disgoorg/disgo/discord"
	"testing"
)

var _ approvalService = (*application.ApprovalService)(nil)

type commandApprovals struct {
	ctx   context.Context
	id    domain.ApprovalID
	calls int
	err   error
}

func (s *commandApprovals) Approve(ctx context.Context, id domain.ApprovalID) (domain.Approval, error) {
	s.ctx, s.id = ctx, id
	s.calls++
	return domain.Approval{ID: id, Status: domain.ApprovalApproved, Action: "sensitive opaque action"}, s.err
}
func TestApproveInteractions(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"success", nil, "Approval: requested-id\nStatus: APPROVED"},
		{"missing", application.ErrApprovalNotFound, "Approval not found."},
		{"not pending", domain.ErrApprovalNotPending, "Approval is not pending."},
		{"wrapped missing", fmt.Errorf("wrapped: %w", application.ErrApprovalNotFound), "Approval not found."},
		{"wrapped not pending", fmt.Errorf("wrapped: %w", domain.ErrApprovalNotPending), "Approval is not pending."},
		{"internal", errors.New("secret original error"), "Internal error."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &commandApprovals{err: tc.err}
			r := &commandREST{}
			g := &Gateway{commands: r}
			g.SetApprovalService(s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := g.handleInteraction(ctx, commandEvent(t, "approve", `[{"name":"approval","type":3,"value":"requested-id"}]`)); err != nil {
				t.Fatal(err)
			}
			if s.calls != 1 || s.ctx != ctx || s.id != "requested-id" {
				t.Fatal("approval/context not forwarded")
			}
			message, ok := r.response.Data.(sdk.MessageCreate)
			if !ok || message.Content != tc.want || message.Flags != sdk.MessageFlagEphemeral || r.responses != 1 || r.response.Type != sdk.InteractionResponseTypeCreateMessage {
				t.Fatal("wrong response", r.response)
			}
		})
	}
}
func TestApproveBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, options string
		configured    bool
	}{
		{"missing option", "[]", true},
		{"wrong type", `[{"name":"approval","type":4,"value":1}]`, true},
		{"unconfigured", `[{"name":"approval","type":3,"value":"a"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &commandApprovals{}
			r := &commandREST{}
			g := &Gateway{commands: r}
			if tc.configured {
				g.SetApprovalService(s)
			}
			if err := g.handleInteraction(context.Background(), commandEvent(t, "approve", tc.options)); err != nil {
				t.Fatal(err)
			}
			if s.calls != 0 || r.responses != 1 || r.response.Data.(sdk.MessageCreate).Content != "Internal error." {
				t.Fatal("unexpected dispatch")
			}
		})
	}
}
