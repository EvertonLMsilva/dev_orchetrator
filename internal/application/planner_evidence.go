package application

import (
	"errors"
	"strings"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrInvalidPlannerEvidence = errors.New("invalid planner evidence")

// PlannerEvidence preserves the operational outcome, including safe failures,
// without converting it to a planner decision or executing an action.
// BotResult retains the existing typed ActionResult union and safe error code.
type PlannerEvidence struct {
	ProjectID     domain.ProjectID
	TaskID        domain.TaskID
	CorrelationID domain.CorrelationID
	BotResult     BotResult
}

// PlannerEvidenceExpectation contains only the original request's correlation
// metadata and requested capability. Session presence and value must match;
// the transport echoes ProtocolVersion and optional SessionID unchanged.
type PlannerEvidenceExpectation struct {
	ProjectID       domain.ProjectID
	TaskID          domain.TaskID
	CorrelationID   domain.CorrelationID
	SessionID       *domain.SessionID
	ProtocolVersion string
	EvidenceKind    domain.ActionType
}

// ConsumePlannerEvidence returns zero evidence on structural or correlation
// errors. Valid operational failures remain evidence, never consumer errors.
// Returned typed payloads share the existing result data and are not mutated.
func ConsumePlannerEvidence(e domain.Envelope, expected PlannerEvidenceExpectation) (PlannerEvidence, error) {
	if (ports.PlannerRequest{ProjectID: expected.ProjectID, TaskID: expected.TaskID}).Validate() != nil ||
		strings.TrimSpace(string(expected.CorrelationID)) == "" || strings.TrimSpace(expected.ProtocolVersion) == "" {
		return PlannerEvidence{}, ErrInvalidPlannerEvidence
	}
	if !validPlannerEvidenceKind(expected.EvidenceKind) {
		return PlannerEvidence{}, ErrInvalidPlannerEvidence
	}
	if e.Validate() != nil || e.MessageType != domain.MessageTypeBotResult || e.ProtocolVersion != expected.ProtocolVersion ||
		e.ProjectID != expected.ProjectID || e.TaskID == nil || *e.TaskID != expected.TaskID || e.CorrelationID != expected.CorrelationID {
		return PlannerEvidence{}, ErrInvalidPlannerEvidence
	}
	if e.SessionID == nil || expected.SessionID == nil {
		if e.SessionID != nil || expected.SessionID != nil {
			return PlannerEvidence{}, ErrInvalidPlannerEvidence
		}
	} else if *e.SessionID != *expected.SessionID {
		return PlannerEvidence{}, ErrInvalidPlannerEvidence
	}
	result, ok := e.Payload.(BotResult)
	if !ok || result.ActionType != expected.EvidenceKind {
		return PlannerEvidence{}, ErrInvalidPlannerEvidence
	}
	evidence := PlannerEvidence{ProjectID: e.ProjectID, TaskID: *e.TaskID, CorrelationID: e.CorrelationID, BotResult: result}
	if err := evidence.Validate(); err != nil {
		return PlannerEvidence{}, err
	}
	return evidence, nil
}

// Validate rechecks evidence structure without replacing the expectation and
// envelope checks required by ConsumePlannerEvidence at the transport boundary.
func (e PlannerEvidence) Validate() error {
	if (ports.PlannerRequest{ProjectID: e.ProjectID, TaskID: e.TaskID}).Validate() != nil || strings.TrimSpace(string(e.CorrelationID)) == "" {
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
