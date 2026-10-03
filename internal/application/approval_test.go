package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"testing"
)

type approvalFake struct {
	approval         domain.Approval
	found            bool
	findErr, saveErr error
	findCtx, saveCtx context.Context
	id               domain.ApprovalID
	saved            domain.Approval
	calls            []string
}

func (r *approvalFake) FindByID(ctx context.Context, id domain.ApprovalID) (domain.Approval, bool, error) {
	r.calls = append(r.calls, "find")
	r.findCtx, r.id = ctx, id
	return r.approval, r.found, r.findErr
}
func (r *approvalFake) Save(ctx context.Context, a domain.Approval) error {
	r.calls = append(r.calls, "save")
	r.saveCtx, r.saved = ctx, a
	return r.saveErr
}

func TestApprovalService(t *testing.T) {
	findErr := errors.New("find failed")
	saveErr := errors.New("save failed")
	task := domain.TaskID("t")
	for _, tc := range []struct {
		name                   string
		status                 domain.ApprovalStatus
		found, canceled        bool
		findErr, saveErr, want error
		calls                  int
	}{
		{name: "pending", status: domain.ApprovalPending, found: true, calls: 2},
		{name: "not found", want: ErrApprovalNotFound, calls: 1},
		{name: "find error", findErr: findErr, want: findErr, calls: 1},
		{name: "approved", status: domain.ApprovalApproved, found: true, want: domain.ErrApprovalNotPending, calls: 1},
		{name: "rejected", status: domain.ApprovalRejected, found: true, want: domain.ErrApprovalNotPending, calls: 1},
		{name: "invalid", status: "invalid", found: true, want: domain.ErrApprovalNotPending, calls: 1},
		{name: "save error", status: domain.ApprovalPending, found: true, saveErr: saveErr, want: saveErr, calls: 2},
		{name: "canceled", canceled: true, want: context.Canceled, calls: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := domain.Approval{ID: "a", ProjectID: "p", TaskID: &task, Action: "opaque action", Status: tc.status}
			r := &approvalFake{approval: original, found: tc.found, findErr: tc.findErr, saveErr: tc.saveErr}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			got, err := NewApprovalService(r).Approve(ctx, "requested-id")
			if err != tc.want || len(r.calls) != tc.calls {
				t.Fatalf("got %+v, %v; calls %v", got, err, r.calls)
			}
			if tc.calls > 0 && (r.calls[0] != "find" || r.findCtx != ctx || r.id != "requested-id") {
				t.Fatal("find not forwarded")
			}
			updated := original
			updated.Status = domain.ApprovalApproved
			if tc.calls == 2 && (r.calls[1] != "save" || r.saveCtx != ctx || r.saved != updated) {
				t.Fatal("updated approval not saved with same context")
			}
			if err == nil && got != updated {
				t.Fatal("approval fields changed", got)
			}
			if err != nil && got != (domain.Approval{}) {
				t.Fatal("error returned approval", got)
			}
			if r.approval != original {
				t.Fatal("original changed")
			}
		})
	}
}
