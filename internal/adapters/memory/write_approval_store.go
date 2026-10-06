package memory

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"sync"
	"time"
)

// WriteApprovalStore is process-local proof of atomic consumption only. It is
// not production persistence and confers no permission to mutate files/Git.
type WriteApprovalStore struct {
	mu      sync.Mutex
	records map[domain.ApprovalID]domain.WriteApprovalRecord
	nonces  map[string]bool
}

var _ ports.WriteApprovalStore = (*WriteApprovalStore)(nil)

func NewWriteApprovalStore() *WriteApprovalStore {
	return &WriteApprovalStore{records: make(map[domain.ApprovalID]domain.WriteApprovalRecord), nonces: make(map[string]bool)}
}
func (s *WriteApprovalStore) Issue(ctx context.Context, a domain.WriteApproval) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := s.records[a.ApprovalID]; exists || s.nonces[a.Nonce] {
		return ports.ErrWriteApprovalExists
	}
	s.records[a.ApprovalID] = domain.WriteApprovalRecord{Approval: a.Clone(), State: domain.WriteAvailable}
	s.nonces[a.Nonce] = true
	return nil
}
func (s *WriteApprovalStore) Find(ctx context.Context, id domain.ApprovalID) (domain.WriteApprovalRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.WriteApprovalRecord{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.WriteApprovalRecord{}, false, err
	}
	r, found := s.records[id]
	return r.Clone(), found, nil
}
func (s *WriteApprovalStore) Reserve(ctx context.Context, expected domain.WriteApproval, transaction string, now time.Time) (ports.WriteReservation, error) {
	if err := ctx.Err(); err != nil {
		return ports.WriteReservation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ports.WriteReservation{}, err
	}
	r, found := s.records[expected.ApprovalID]
	if !found {
		return ports.WriteReservation{}, ports.ErrWriteApprovalNotFound
	}
	if !r.Approval.Equal(expected) {
		return ports.WriteReservation{}, domain.ErrWriteDenied
	}
	updated, granted, err := r.Reserve(transaction, now)
	if err != nil {
		return ports.WriteReservation{}, err
	}
	s.records[expected.ApprovalID] = updated.Clone()
	return ports.WriteReservation{Record: updated.Clone(), Granted: granted}, nil
}
func (s *WriteApprovalStore) Finish(ctx context.Context, id domain.ApprovalID, transaction string, result domain.WriteTerminalResult) (domain.WriteApprovalRecord, error) {
	if err := ctx.Err(); err != nil {
		return domain.WriteApprovalRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.WriteApprovalRecord{}, err
	}
	r, found := s.records[id]
	if !found {
		return domain.WriteApprovalRecord{}, ports.ErrWriteApprovalNotFound
	}
	updated, err := r.Finish(transaction, result)
	if err != nil {
		return domain.WriteApprovalRecord{}, err
	}
	s.records[id] = updated.Clone()
	return updated.Clone(), nil
}
