package ports

import (
	"context"
	"dev-orchestrator/internal/domain"
)

type testApprovalRepository struct{}

var _ ApprovalRepository = (*testApprovalRepository)(nil)

func (*testApprovalRepository) Save(context.Context, domain.Approval) error { return nil }
func (*testApprovalRepository) FindByID(context.Context, domain.ApprovalID) (domain.Approval, bool, error) {
	return domain.Approval{}, false, nil
}
