//go:build !linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
)

func (f *candidateWorkspaceFactory) Prepare(context.Context, string, string, domain.CandidatePolicy) (ports.CandidateWorkspace, error) {
	return nil, errors.New("candidate filesystem boundary requires Linux")
}
