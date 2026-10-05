package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
)

var ErrUnresolvedEvidenceRequest = errors.New("evidence request has no trusted typed parameters")

// EvidenceRequestResolver never receives a planner's descriptive reason.
type EvidenceRequestResolver interface {
	Resolve(context.Context, domain.ActionType, PlannerContext) (domain.ActionParams, error)
}

// TrustedEvidenceResolver is configured by the composition root, never by model output.
// Git status needs no parameters. All other categories require explicit configuration.
type TrustedEvidenceResolver struct {
	Params map[domain.ActionType]domain.ActionParams
}

func (r TrustedEvidenceResolver) Resolve(ctx context.Context, kind domain.ActionType, canonical PlannerContext) (domain.ActionParams, error) {
	if err := ctx.Err(); err != nil {
		return domain.ActionParams{}, err
	}
	params, configured := r.Params[kind]
	if !configured && kind != domain.ActionTypeGitStatus {
		return domain.ActionParams{}, ErrUnresolvedEvidenceRequest
	}
	taskID := canonical.CurrentTask.ID
	if _, err := domain.NewAction(kind, canonical.Project.ID, &taskID, params); err != nil {
		return domain.ActionParams{}, err
	}
	if canonical.CurrentTask.ProjectID != canonical.Project.ID {
		return domain.ActionParams{}, ErrInvalidOrchestrationContext
	}
	return params, nil
}
