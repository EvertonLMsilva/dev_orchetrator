package ports

import (
	"dev-orchestrator/internal/domain"
	"errors"
	"strings"
)

// PlannerContext carries the canonical repository snapshot for a round.
type PlannerContext struct {
	Project     domain.Project
	CurrentTask domain.Task
}

var ErrInvalidPlannerEvidence = errors.New("invalid planner evidence")

type BotResultStatus string

const (
	BotResultSuccess          BotResultStatus = "SUCCESS"
	BotResultBlocked          BotResultStatus = "BLOCKED"
	BotResultApprovalRequired BotResultStatus = "APPROVAL_REQUIRED"
	BotResultFailed           BotResultStatus = "FAILED"
)

// BotError exposes a fixed code, never a dependency's raw error text.
type BotError struct{ Code string }
type BotResult struct {
	ActionType domain.ActionType
	Status     BotResultStatus
	Result     *ActionResult
	Error      *BotError
}

type PlannerEvidence struct {
	ProjectID     domain.ProjectID
	TaskID        domain.TaskID
	CorrelationID domain.CorrelationID
	BotResult     BotResult
}

// Validate rechecks evidence structure without replacing the expectation and
// envelope checks required by ConsumePlannerEvidence at the transport boundary.
func (e PlannerEvidence) Validate() error {
	if !validPlannerIdentity(e.ProjectID, e.TaskID) || strings.TrimSpace(string(e.CorrelationID)) == "" {
		return ErrInvalidPlannerEvidence
	}
	result := e.BotResult
	if !validPlannerEvidenceKind(result.ActionType) {
		return ErrInvalidPlannerEvidence
	}
	if result.Status == BotResultSuccess {
		if result.Error != nil || result.Result == nil || result.Result.Validate() != nil || result.Result.Type != result.ActionType {
			return ErrInvalidPlannerEvidence
		}
	} else {
		if result.Result != nil || result.Error == nil || !validPlannerEvidenceError(result.Status, result.Error.Code) {
			return ErrInvalidPlannerEvidence
		}
	}
	return nil
}

func validPlannerEvidenceKind(kind domain.ActionType) bool {
	switch kind {
	case domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitStatus, domain.ActionTypeGitDiff, domain.ActionTypeRunTests:
		return true
	default:
		return false
	}
}

// These are exactly the safe status/code pairs emitted by LocalAgentTransport.
func validPlannerEvidenceError(status BotResultStatus, code string) bool {
	switch status {
	case BotResultBlocked:
		return code == "POLICY_BLOCKED" || code == "ACTION_NOT_ALLOWED" || code == "PROJECT_NOT_FOUND"
	case BotResultApprovalRequired:
		return code == "APPROVAL_REQUIRED"
	case BotResultFailed:
		return code == "EXECUTION_FAILED" || code == "CANCELED" || code == "DEADLINE_EXCEEDED" || code == "AGENT_UNAVAILABLE" || code == "INVALID_RESULT"
	default:
		return false
	}
}
