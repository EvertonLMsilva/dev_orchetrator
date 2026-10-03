package application

import (
	"errors"
	"strings"
	"time"

	"dev-orchestrator/internal/domain"
)

var ErrInvalidPlannerHandoff = errors.New("invalid planner handoff")

type PlannerHandoffMetadata struct {
	ProtocolVersion string
	MessageID       domain.MessageID
	CorrelationID   domain.CorrelationID
	CreatedAt       time.Time
}

type PlannerHandoffBuilder struct{}

// Build uses only explicit summaries. It neither reads repositories nor closes
// the session. The caller must explicitly retain the result of Session.Close.
func (PlannerHandoffBuilder) Build(s PlannerSession, p domain.PlannerHandoffPayload, m PlannerHandoffMetadata) (domain.Envelope, error) {
	if err := s.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	if s.State() != PlannerSessionHandoffRequired {
		return domain.Envelope{}, ErrInvalidPlannerHandoff
	}
	if err := validatePlannerHandoff(s, p); err != nil {
		return domain.Envelope{}, err
	}
	// Copy summaries and optional identities; the prepared contract owns its data.
	p.Decisions = append([]string(nil), p.Decisions...)
	p.Evidence = append([]string(nil), p.Evidence...)
	p.Completed = append([]domain.TaskID(nil), p.Completed...)
	p.Blocked = append([]domain.TaskID(nil), p.Blocked...)
	var taskID *domain.TaskID
	if p.CurrentTask != nil {
		id, status := *p.CurrentTask, *p.TaskStatus
		p.CurrentTask = &id
		p.TaskStatus = &status
		envelopeID := id
		taskID = &envelopeID
	}
	sessionID := s.SessionID()
	e := domain.Envelope{ProtocolVersion: m.ProtocolVersion, MessageType: domain.MessageTypePlannerHandoff, MessageID: m.MessageID, CorrelationID: m.CorrelationID, ProjectID: p.ProjectID, TaskID: taskID, SessionID: &sessionID, CreatedAt: m.CreatedAt, Payload: p}
	if err := e.Validate(); err != nil {
		return domain.Envelope{}, err
	}
	return e, nil
}

func validatePlannerHandoff(s PlannerSession, p domain.PlannerHandoffPayload) error {
	if p.ProjectID != s.ProjectID() || strings.TrimSpace(p.NextAction) == "" {
		return ErrInvalidPlannerHandoff
	}
	id, present := s.TaskID()
	if present {
		if p.CurrentTask == nil || *p.CurrentTask != id || p.TaskStatus == nil {
			return ErrInvalidPlannerHandoff
		}
		switch *p.TaskStatus {
		case domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing, domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress, domain.TaskStatusDone, domain.TaskStatusBlocked, domain.TaskStatusFailed, domain.TaskStatusCancelled:
		default:
			return ErrInvalidPlannerHandoff
		}
	} else if p.CurrentTask != nil || p.TaskStatus != nil {
		return ErrInvalidPlannerHandoff
	}
	for _, items := range [][]string{p.Decisions, p.Evidence} {
		for _, item := range items {
			if strings.TrimSpace(item) == "" {
				return ErrInvalidPlannerHandoff
			}
		}
	}
	for _, ids := range [][]domain.TaskID{p.Completed, p.Blocked} {
		for _, id := range ids {
			if strings.TrimSpace(string(id)) == "" {
				return ErrInvalidPlannerHandoff
			}
		}
	}
	return nil
}

// RestorePlannerSession requires explicit closure and a correlated typed handoff.
// A fresh unused budget is mandatory; summaries remain outside session lifecycle.
// NextAction is declarative context, never interpreted or executed as a command.
func RestorePlannerSession(old PlannerSession, e domain.Envelope, newID domain.SessionID, budget ContextBudget) (PlannerSession, error) {
	if err := old.Validate(); err != nil {
		return PlannerSession{}, err
	}
	if old.State() != PlannerSessionClosed || old.CloseReason() != domain.SessionCloseReasonContextBudget || strings.TrimSpace(string(newID)) == "" || newID == old.SessionID() {
		return PlannerSession{}, ErrInvalidPlannerHandoff
	}
	if err := budget.Validate(); err != nil {
		return PlannerSession{}, err
	}
	if budget.Used != 0 {
		return PlannerSession{}, ErrInvalidContextBudget
	}
	if err := e.Validate(); err != nil {
		return PlannerSession{}, err
	}
	if e.MessageType != domain.MessageTypePlannerHandoff || e.ProjectID != old.ProjectID() || e.SessionID == nil || *e.SessionID != old.SessionID() {
		return PlannerSession{}, ErrInvalidPlannerHandoff
	}
	p, ok := e.Payload.(domain.PlannerHandoffPayload)
	if !ok {
		return PlannerSession{}, ErrInvalidPlannerHandoff
	}
	if err := validatePlannerHandoff(old, p); err != nil {
		return PlannerSession{}, err
	}
	id, present := old.TaskID()
	if present {
		if e.TaskID == nil || *e.TaskID != id {
			return PlannerSession{}, ErrInvalidPlannerHandoff
		}
	} else if e.TaskID != nil {
		return PlannerSession{}, ErrInvalidPlannerHandoff
	}
	return NewPlannerSession(newID, p.ProjectID, p.CurrentTask, budget, domain.SessionStartReasonHandoff)
}
