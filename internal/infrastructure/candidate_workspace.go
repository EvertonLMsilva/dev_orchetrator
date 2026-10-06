package infrastructure

import (
	"dev-orchestrator/internal/ports"
)

type candidateWorkspaceFactory struct{ scratchRoot string }

// scratchRoot is an explicit trusted temporary parent outside the source root.
// It is never delivered to the generator. No productive composition is added.
func NewCandidateWorkspaceFactory(scratchRoot string) ports.CandidateWorkspaceFactory {
	return &candidateWorkspaceFactory{scratchRoot: scratchRoot}
}
