package ports

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"time"
)

var (
	ErrWriteApprovalNotFound = errors.New("write approval not found")
	ErrWriteApprovalExists   = errors.New("write approval or nonce already issued")
)

type WriteReservation struct {
	Record domain.WriteApprovalRecord
	// Only the first successful atomic reservation returns Granted=true.
	// Replay returns the original transaction/receipt with Granted=false.
	Granted bool
}

// Trusted issuance only; no caller/model may submit its own approval. Issue
// refuses overwrite and nonce reuse. Reserve compares the complete approval
// atomically with consuming AVAILABLE. Finish never reopens a consumed approval.
// Implementations return detached snapshots. Durability is NOT implied here;
// a production journal/store and restart semantics are a separate P10.3 gate.
type WriteApprovalStore interface {
	Issue(context.Context, domain.WriteApproval) error
	Find(context.Context, domain.ApprovalID) (domain.WriteApprovalRecord, bool, error)
	Reserve(context.Context, domain.WriteApproval, string, time.Time) (WriteReservation, error)
	Finish(context.Context, domain.ApprovalID, string, domain.WriteTerminalResult) (domain.WriteApprovalRecord, error)
}
