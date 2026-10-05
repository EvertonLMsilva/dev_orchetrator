package application

import (
	"strings"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrInvalidPlannerEvidence = ports.ErrInvalidPlannerEvidence

// PlannerEvidence preserves the operational outcome, including safe failures,
// without converting it to a planner decision or executing an action.
// BotResult retains the existing typed ActionResult union and safe error code.
type PlannerEvidence = ports.PlannerEvidence

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
	if strings.TrimSpace(string(expected.ProjectID)) == "" || strings.TrimSpace(string(expected.TaskID)) == "" ||
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

func validPlannerEvidenceKind(kind domain.ActionType) bool {
	switch kind {
	case domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitStatus, domain.ActionTypeGitDiff, domain.ActionTypeRunTests:
		return true
	default:
		return false
	}
}
