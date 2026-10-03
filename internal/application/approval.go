package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
)

var ErrApprovalNotFound = errors.New("approval not found")

type ApprovalService struct{ approvals ports.ApprovalRepository }

func NewApprovalService(approvals ports.ApprovalRepository) *ApprovalService {
	return &ApprovalService{approvals: approvals}
}
func (s *ApprovalService) Approve(ctx context.Context, id domain.ApprovalID) (domain.Approval, error) {
	if err := ctx.Err(); err != nil {
		return domain.Approval{}, err
	}
	approval, found, err := s.approvals.FindByID(ctx, id)
	if err != nil {
		return domain.Approval{}, err
	}
	if !found {
		return domain.Approval{}, ErrApprovalNotFound
	}
	updated, err := approval.Approve()
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.approvals.Save(ctx, updated); err != nil {
		return domain.Approval{}, err
	}
	return updated, nil
}
