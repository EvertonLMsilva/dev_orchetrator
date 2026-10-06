package memory

import (
	"context"
	"crypto/sha256"
	"dev-orchestrator/internal/domain"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func storeApproval() (domain.WriteApproval, time.Time) {
	h := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	a := domain.WriteApproval{SchemaVersion: 1, ApprovalID: "approval", ApproverIdentity: "actor", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute), WriteBinding: domain.WriteBinding{ProjectID: "p", TaskID: "t", CorrelationID: "c", WorkspaceIdentity: "w", BaseIdentity: h("base"), OperationKind: domain.WriteApply, AllowedTargets: []string{"a"}, ApprovedDiffIdentity: h("diff"), ApprovedArtifactID: "artifact", ExpectedPostIdentity: h("post"), OperationParametersIdentity: h("parameters"), PolicyVersion: "policy", Nonce: "nonce"}}
	return a, now
}
func TestWriteApprovalStoreConcurrentSingleUse(t *testing.T) {
	s := NewWriteApprovalStore()
	a, now := storeApproval()
	ctx := context.Background()
	if err := s.Issue(ctx, a); err != nil {
		t.Fatal(err)
	}
	var grants atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, err := s.Reserve(ctx, a, fmt.Sprint(i), now)
			if err != nil {
				t.Error(err)
				return
			}
			if r.Granted {
				grants.Add(1)
			}
			if r.Record.State != domain.WriteReserved {
				t.Error("not reserved")
			}
		}(i)
	}
	close(start)
	wg.Wait()
	if grants.Load() != 1 {
		t.Fatal("grant count", grants.Load())
	}
	record, found, err := s.Find(ctx, a.ApprovalID)
	if err != nil || !found {
		t.Fatal(err)
	}
	replay, err := s.Reserve(ctx, a, "different", now.Add(time.Hour))
	if err != nil || replay.Granted || replay.Record.TransactionID != record.TransactionID {
		t.Fatal(replay, err)
	}
	result := domain.WriteTerminalResult{State: domain.WriteApplied, ResultIdentity: a.ExpectedPostIdentity}
	if _, err := s.Finish(ctx, a.ApprovalID, record.TransactionID, result); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(ctx, a.ApprovalID, record.TransactionID, result); err != nil {
		t.Fatal("terminal replay", err)
	}
	replay, err = s.Reserve(ctx, a, "new", now)
	if err != nil || replay.Granted || replay.Record.State != domain.WriteApplied || replay.Record.Result != result {
		t.Fatal(replay, err)
	}
	result.State = domain.WriteAborted
	if _, err := s.Finish(ctx, a.ApprovalID, record.TransactionID, result); err == nil {
		t.Fatal("terminal overwritten")
	}
}
func TestWriteApprovalStoreDenyAndCopies(t *testing.T) {
	s := NewWriteApprovalStore()
	a, now := storeApproval()
	ctx := context.Background()
	if err := s.Issue(ctx, a); err != nil {
		t.Fatal(err)
	}
	original := a.Clone()
	a.AllowedTargets[0] = "mutated"
	record, _, _ := s.Find(ctx, original.ApprovalID)
	if record.Approval.AllowedTargets[0] != "a" {
		t.Fatal("issue alias")
	}
	record.Approval.AllowedTargets[0] = "mutated"
	record, _, _ = s.Find(ctx, original.ApprovalID)
	if record.Approval.AllowedTargets[0] != "a" {
		t.Fatal("find alias")
	}
	if _, err := s.Reserve(ctx, a, "tx", now); err == nil {
		t.Fatal("changed approval accepted")
	}
	if _, err := s.Reserve(ctx, original, "tx", original.ExpiresAt); err == nil {
		t.Fatal("expired grant")
	}
	if _, err := s.Finish(ctx, original.ApprovalID, "tx", domain.WriteTerminalResult{State: domain.WriteAborted, ResultIdentity: original.BaseIdentity}); err == nil {
		t.Fatal("finish before reserve")
	}
	other := original.Clone()
	other.ApprovalID = "second"
	if s.Issue(ctx, other) == nil {
		t.Fatal("nonce reuse")
	}
	if s.Issue(ctx, original) == nil {
		t.Fatal("issue overwrite")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Reserve(canceled, original, "tx", now); err != context.Canceled {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, original, "tx", now)
	if err != nil || !r.Granted {
		t.Fatal(r, err)
	}
	r.Record.Approval.AllowedTargets[0] = "response mutation"
	record, _, _ = s.Find(ctx, original.ApprovalID)
	if record.Approval.AllowedTargets[0] != "a" {
		t.Fatal("reserve response alias")
	}
	if _, err := s.Finish(ctx, original.ApprovalID, "wrong", domain.WriteTerminalResult{State: domain.WriteAborted, ResultIdentity: original.BaseIdentity}); err == nil {
		t.Fatal("wrong transaction finished")
	}
}

func TestWriteApprovalStoreSameTransactionReplay(t *testing.T) {
	s := NewWriteApprovalStore()
	a, now := storeApproval()
	ctx := context.Background()
	if err := s.Issue(ctx, a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		r, err := s.Reserve(ctx, a, "same", now)
		if err != nil || r.Granted != (i == 0) || r.Record.TransactionID != "same" {
			t.Fatal(r, err)
		}
	}
	for _, result := range []domain.WriteTerminalResult{
		{State: domain.WriteAvailable, ResultIdentity: a.ExpectedPostIdentity},
		{State: domain.WriteApplied, ResultIdentity: a.BaseIdentity},
		{State: domain.WriteAborted, ResultIdentity: "invalid"},
	} {
		if _, err := s.Finish(ctx, a.ApprovalID, "same", result); err == nil {
			t.Fatal("invalid result accepted", result)
		}
	}
	r, err := s.Finish(ctx, a.ApprovalID, "same", domain.WriteTerminalResult{State: domain.WriteRecoveryRequired, ResultIdentity: a.BaseIdentity})
	if err != nil || r.State != domain.WriteRecoveryRequired {
		t.Fatal(r, err)
	}
	if err := s.Issue(ctx, a); err == nil {
		t.Fatal("consumed approval reissued")
	}
}
