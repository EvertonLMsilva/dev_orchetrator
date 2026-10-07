package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"time"
)

type WriteAuthorizationRequest struct {
	ApprovalID    domain.ApprovalID
	TransactionID string
	Candidate     domain.WriteCandidate
	// Trusted context, never selected by candidate/model output.
	Context domain.WriteBinding
}
type WriteAuthorizationService struct {
	store ports.WriteApprovalStore
	now   func() time.Time
}

// Clock and store are explicit trusted dependencies; no approval issuance,
// runtime execution or filesystem effect is performed by this service.
func NewWriteAuthorizationService(store ports.WriteApprovalStore, now func() time.Time) *WriteAuthorizationService {
	return &WriteAuthorizationService{store: store, now: now}
}
func (s *WriteAuthorizationService) Reserve(ctx context.Context, q WriteAuthorizationRequest) (ports.WriteReservation, error) {
	if err := ctx.Err(); err != nil {
		return ports.WriteReservation{}, err
	}
	if s == nil || s.store == nil || s.now == nil {
		return ports.WriteReservation{}, domain.ErrWriteDenied
	}
	record, found, err := s.store.Find(ctx, q.ApprovalID)
	if err != nil {
		return ports.WriteReservation{}, err
	}
	if !found {
		return ports.WriteReservation{}, ports.ErrWriteApprovalNotFound
	}
	if record.Validate() != nil || record.Approval.ApprovalID != q.ApprovalID {
		return ports.WriteReservation{}, domain.ErrWriteDenied
	}
	now := s.now()
	validationTime := now
	// Expiry controls new grants, not observation of an already-consumed receipt.
	// Context/candidate validation is still mandatory for every replay.
	if record.State != domain.WriteAvailable {
		validationTime = record.ReservedAt
	}
	if err := domain.ValidateWriteAuthorization(record.Approval, q.Candidate, q.Context, validationTime); err != nil {
		return ports.WriteReservation{}, err
	}
	return s.store.Reserve(ctx, record.Approval, q.TransactionID, now)
}
