package ports

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
)

var ErrCandidateCleanup = errors.New("candidate cleanup blocked")

// Dedicated, provider-independent port. Implementations are trusted adapters:
// they MUST install an empty tool policy before creating any session/inference.
// ProveToolFree must fail if this cannot be demonstrated. No legacy fallback.
// No filesystem paths, tool callbacks, shell, network, or approvals are passed.
// Calls must honor cancellation, bound output, and return only after their
// workers have stopped. This contract is not proof of a concrete Codex adapter.
type CandidateGenerator interface {
	ProveToolFree(context.Context) error
	Generate(context.Context, CandidateGenerationRequest) ([]byte, error)
}
type CandidateGenerationRequest struct {
	Context       domain.CandidateContext
	Objective     string
	Inputs        []domain.CandidateFile
	WriteTargets  []string
	SchemaVersion int
}

// This is a trusted filesystem boundary, distinct from the future real applier.
// A non-nil workspace must be closed even when Prepare returns an error.
type CandidateWorkspaceFactory interface {
	Prepare(context.Context, string, string, domain.CandidatePolicy) (CandidateWorkspace, error)
}
type CandidateWorkspace interface {
	Inputs() []domain.CandidateFile
	Write(context.Context, []byte) error
	Extract(context.Context, domain.CandidateContext) (domain.CandidateArtifact, error)
	Close() error
}
