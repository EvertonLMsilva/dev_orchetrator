package domain

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownPolicyDecision = errors.New("unknown policy decision")
	ErrPolicyReasonRequired  = errors.New("policy reason is required")
)

type PolicyDecision string

const (
	PolicyDecisionAuto     PolicyDecision = "AUTO"
	PolicyDecisionApproval PolicyDecision = "APPROVAL"
	PolicyDecisionBlocked  PolicyDecision = "BLOCKED"
)

func (d PolicyDecision) Validate() error {
	switch d {
	case PolicyDecisionAuto, PolicyDecisionApproval, PolicyDecisionBlocked:
		return nil
	default:
		return ErrUnknownPolicyDecision
	}
}

type PolicyResult struct {
	Decision PolicyDecision
	Reason   string
}

func (r PolicyResult) Validate() error {
	if err := r.Decision.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Reason) == "" {
		return ErrPolicyReasonRequired
	}
	return nil
}

type ActionPolicy struct{}

// Evaluate only classifies. AUTO still requires workspace, path, allowlist
// and execution-limit checks before any future execution.
func (ActionPolicy) Evaluate(action Action) (PolicyResult, error) {
	if err := action.Validate(); err != nil {
		return PolicyResult{Decision: PolicyDecisionBlocked, Reason: "Invalid action: " + err.Error()}, err
	}
	switch action.Type {
	case ActionTypeSearch:
		return PolicyResult{Decision: PolicyDecisionAuto, Reason: "SEARCH is explicitly classified as a low-risk text search"}, nil
	case ActionTypeReadFile:
		return PolicyResult{Decision: PolicyDecisionAuto, Reason: "READ_FILE is explicitly classified as a low-risk file read"}, nil
	case ActionTypeGitStatus:
		return PolicyResult{Decision: PolicyDecisionAuto, Reason: "GIT_STATUS is explicitly classified as a read-only Git inspection"}, nil
	case ActionTypeGitDiff:
		return PolicyResult{Decision: PolicyDecisionAuto, Reason: "GIT_DIFF is explicitly classified as a read-only Git inspection"}, nil
	case ActionTypeRunTests:
		return PolicyResult{Decision: PolicyDecisionAuto, Reason: "RUN_TESTS is explicitly classified as a symbolic test target requiring later authorization"}, nil
	default:
		return PolicyResult{Decision: PolicyDecisionBlocked, Reason: fmt.Sprintf("Action type %q has no explicit policy classification", action.Type)}, nil
	}
}
