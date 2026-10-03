package memory

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"sync"
)

type ApprovalRepository struct {
	mu        sync.RWMutex
	approvals map[domain.ApprovalID]domain.Approval
}

var _ ports.ApprovalRepository = (*ApprovalRepository)(nil)

func NewApprovalRepository() *ApprovalRepository {
	return &ApprovalRepository{approvals: make(map[domain.ApprovalID]domain.Approval)}
}
func (r *ApprovalRepository) Save(ctx context.Context, approval domain.Approval) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.approvals[approval.ID] = approval
	return nil
}
func (r *ApprovalRepository) FindByID(ctx context.Context, id domain.ApprovalID) (domain.Approval, bool, error) {
	if err := ctx.Err(); err != nil {
		return domain.Approval{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	approval, found := r.approvals[id]
	return approval, found, nil
}
