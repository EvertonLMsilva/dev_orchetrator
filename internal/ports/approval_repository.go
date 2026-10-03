package ports

import (
	"context"
	"dev-orchestrator/internal/domain"
)

type ApprovalRepository interface {
	Save(context.Context, domain.Approval) error
	// FindByID returns (zero Approval, false, nil) when absent.
	FindByID(context.Context, domain.ApprovalID) (domain.Approval, bool, error)
}
