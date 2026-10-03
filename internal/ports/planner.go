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

// PlannerRequest carries the identities needed to correlate a decision.
type PlannerRequest struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
}

func (r PlannerRequest) Validate() error {
	if strings.TrimSpace(string(r.ProjectID)) == "" || strings.TrimSpace(string(r.TaskID)) == "" {
		return ErrInvalidPlannerRequest
	}
	return nil
}

type PlannerDecisionType string

const (
	PlannerDecisionRequestEvidence PlannerDecisionType = "REQUEST_EVIDENCE"
	PlannerDecisionPrepareExecutor PlannerDecisionType = "PREPARE_EXECUTOR"
	PlannerDecisionBlock           PlannerDecisionType = "BLOCK"
)

// PlannerDecision is a closed set of intentions, not an executable payload.
// Reason is descriptive and required only for BLOCK. A valid decision must
// also match the originating request's identities at the caller boundary.
type PlannerDecision struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
	Type      PlannerDecisionType
	Reason    string
}

func (d PlannerDecision) Validate() error {
	if (PlannerRequest{ProjectID: d.ProjectID, TaskID: d.TaskID}).Validate() != nil {
		return ErrInvalidPlannerDecision
	}
	switch d.Type {
	case PlannerDecisionRequestEvidence, PlannerDecisionPrepareExecutor:
		if d.Reason != "" {
			return ErrInvalidPlannerDecision
		}
	case PlannerDecisionBlock:
		if strings.TrimSpace(d.Reason) == "" {
			return ErrInvalidPlannerDecision
		}
	default:
		return ErrInvalidPlannerDecision
	}
	return nil
}
