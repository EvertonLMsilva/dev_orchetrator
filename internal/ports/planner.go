package ports

import (
	"context"
	"errors"
	"strings"

	"dev-orchestrator/internal/domain"
)

// Planner decides the next step using internal contracts only. Callers must
// validate requests before Plan and decisions before acting on them.
type Planner interface {
	Plan(context.Context, PlannerRequest) (PlannerDecision, error)
}

var (
	ErrInvalidPlannerRequest  = errors.New("invalid planner request")
	ErrInvalidPlannerDecision = errors.New("invalid planner decision")
)

// PlannerRequest carries the canonical snapshot and validated evidence for the current round.
type PlannerRequest struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
	Context   PlannerContext
	Evidence  []PlannerEvidence
	// UserIntent is the current declarative message; it grants no execution authority.
	UserIntent string
}

func (r PlannerRequest) Validate() error {
	if !validPlannerIdentity(r.ProjectID, r.TaskID) || r.Context.Project.ID != r.ProjectID || r.Context.CurrentTask.ID != r.TaskID || r.Context.CurrentTask.ProjectID != r.ProjectID {
		return ErrInvalidPlannerRequest
	}
	for _, evidence := range r.Evidence {
		if evidence.Validate() != nil || evidence.ProjectID != r.ProjectID || evidence.TaskID != r.TaskID {
			return ErrInvalidPlannerRequest
		}
	}
	return nil
}

func validPlannerIdentity(projectID domain.ProjectID, taskID domain.TaskID) bool {
	return strings.TrimSpace(string(projectID)) != "" && strings.TrimSpace(string(taskID)) != ""
}

type PlannerDecisionType string

const (
	PlannerDecisionRequestEvidence PlannerDecisionType = "REQUEST_EVIDENCE"
	PlannerDecisionPrepareExecutor PlannerDecisionType = "PREPARE_EXECUTOR"
	PlannerDecisionBlock           PlannerDecisionType = "BLOCK"
)

// PlannerDecision is a closed set of intentions, not an executable payload.
// Reason is descriptive and required for every type, never an executable command.
// EvidenceKind uses only an action category, without Action or parameters, and
// is required exclusively for REQUEST_EVIDENCE. A valid decision must
// also match the originating request's identities at the caller boundary.
type PlannerDecision struct {
	ProjectID    domain.ProjectID
	TaskID       domain.TaskID
	Type         PlannerDecisionType
	Reason       string
	EvidenceKind domain.ActionType
}

func (d PlannerDecision) Validate() error {
	if !validPlannerIdentity(d.ProjectID, d.TaskID) || strings.TrimSpace(d.Reason) == "" {
		return ErrInvalidPlannerDecision
	}
	switch d.Type {
	case PlannerDecisionRequestEvidence:
		switch d.EvidenceKind {
		case domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitStatus, domain.ActionTypeGitDiff, domain.ActionTypeRunTests:
		default:
			return ErrInvalidPlannerDecision
		}
	case PlannerDecisionPrepareExecutor, PlannerDecisionBlock:
		if d.EvidenceKind != "" {
			return ErrInvalidPlannerDecision
		}
	default:
		return ErrInvalidPlannerDecision
	}
	return nil
}
