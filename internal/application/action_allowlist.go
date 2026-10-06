package application

import "dev-orchestrator/internal/domain"

// ActionAllowlist recognizes explicitly supported capabilities independently of
// authorization policy. Its zero value denies every capability.
type ActionAllowlist struct {
	defaultsEnabled bool
	readOnly        bool
}

// NewDefaultActionAllowlist enables exactly the five MVP capabilities.
// No mutable capability collection is exposed to callers.
func NewDefaultActionAllowlist() ActionAllowlist {
	return ActionAllowlist{defaultsEnabled: true}
}

// NewReadOnlyActionAllowlist excludes test execution, which may mutate projects.
func NewReadOnlyActionAllowlist() ActionAllowlist {
	return ActionAllowlist{defaultsEnabled: true, readOnly: true}
}

// Allows checks support only; it neither authorizes nor executes an action.
// New domain action types remain denied until explicitly registered here.
func (a ActionAllowlist) Allows(actionType domain.ActionType) bool {
	if !a.defaultsEnabled {
		return false
	}
	if a.readOnly && actionType == domain.ActionTypeRunTests {
		return false
	}
	switch actionType {
	case domain.ActionTypeSearch, domain.ActionTypeReadFile,
		domain.ActionTypeGitStatus, domain.ActionTypeGitDiff,
		domain.ActionTypeRunTests:
		return true
	default:
		return false
	}
}
