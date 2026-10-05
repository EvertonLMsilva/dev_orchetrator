package application

import (
	"errors"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

var ErrInvalidOrchestrationContext = errors.New("orchestration context does not match round identity")

// OrchestrationInput is the provider-independent data made available to a
// planner for one round. Context comes from ContextBuilder; optional Evidence
// comes from ConsumePlannerEvidence, after validating the original expectation.
// Validate rechecks structure and round identity, not transport provenance.
// This contract does not invoke Planner.
type OrchestrationInput struct {
	ProjectID domain.ProjectID
	TaskID    domain.TaskID
	Context   PlannerContext
	Evidence  []PlannerEvidence
}

func (r OrchestrationInput) Validate() error {
	request := ports.PlannerRequest{ProjectID: r.ProjectID, TaskID: r.TaskID, Context: r.Context, Evidence: r.Evidence}
	if err := request.Validate(); err != nil {
		return err
	}
	if r.Context.Project.ID != r.ProjectID || r.Context.CurrentTask.ID != r.TaskID || r.Context.CurrentTask.ProjectID != r.ProjectID {
		return ErrInvalidOrchestrationContext
	}
	for _, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if evidence.ProjectID != r.ProjectID || evidence.TaskID != r.TaskID {
			return ErrInvalidPlannerEvidence
		}
	}
	return nil
}

// OrchestrationOutput carries only a planner intention. Validation performs no
// capability execution, persistence, task transition, or session lifecycle work.
// PlannerSession and canonical Task state remain separate responsibilities.
type OrchestrationOutput struct {
	Decision ports.PlannerDecision
}

func (r OrchestrationOutput) Validate(input OrchestrationInput) error {
	if err := input.Validate(); err != nil {
		return err
	}
	if err := r.Decision.Validate(); err != nil {
		return err
	}
	if r.Decision.ProjectID != input.ProjectID || r.Decision.TaskID != input.TaskID {
		return ErrPlannerTaskIdentityMismatch
	}
	return nil
}
