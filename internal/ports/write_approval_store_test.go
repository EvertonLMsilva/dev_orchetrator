package ports_test

import (
	"context"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"testing"
)

// Exercise the port's absent/error contract through the reference memory adapter.
func TestWriteApprovalStorePortAbsentAndCancellation(t *testing.T) {
	var store ports.WriteApprovalStore = memory.NewWriteApprovalStore()
	r, found, err := store.Find(context.Background(), "missing")
	if err != nil || found || r.State != "" {
		t.Fatal(r, found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Issue(ctx, domain.WriteApproval{}); err != context.Canceled {
		t.Fatal(err)
	}
	if _, _, err := store.Find(ctx, "missing"); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := store.Reserve(ctx, domain.WriteApproval{}, "tx", domain.WriteApproval{}.IssuedAt); err != context.Canceled {
		t.Fatal(err)
	}
	if _, err := store.Finish(ctx, "missing", "tx", domain.WriteTerminalResult{}); err != context.Canceled {
		t.Fatal(err)
	}
}
