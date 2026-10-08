package application

import "dev-orchestrator/internal/domain"

// DevelopmentIntent records user constraints. Only CandidatePolicy grants authority.
// Targets are literal, exact paths; absence is denied for the Development write flow.
type DevelopmentIntent struct {
	Objective             string
	RequestedWriteTargets []string
}

// ValidateWriteTargets checks user constraints against trusted project authority.
func (i DevelopmentIntent) ValidateWriteTargets(policy domain.CandidatePolicy) error {
	if policy.Validate() != nil || len(i.RequestedWriteTargets) == 0 || len(i.RequestedWriteTargets) > 1024 {
		return domain.ErrCandidateDenied
	}
	allowed := make(map[string]bool, len(policy.WriteTargets))
	for _, target := range policy.WriteTargets {
		allowed[target] = true
	}
	seen := make(map[string]bool, len(i.RequestedWriteTargets))
	for _, target := range i.RequestedWriteTargets {
		if !allowed[target] || seen[target] {
			return domain.ErrCandidateDenied
		}
		seen[target] = true
	}
	return nil
}

func (i DevelopmentIntent) permitsArtifact(a domain.CandidateArtifact, policy domain.CandidatePolicy) bool {
	requested, allowed := map[string]bool{}, map[string]bool{}
	for _, target := range i.RequestedWriteTargets {
		requested[target] = true
	}
	for _, target := range policy.WriteTargets {
		allowed[target] = true
	}
	entries := a.Candidate().Manifest.Entries
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !requested[entry.Target] || !allowed[entry.Target] {
			return false
		}
	}
	return true
}
