package application

import (
	"errors"
	"strings"

	"dev-orchestrator/internal/domain"
)

var (
	ErrInvalidPlannerSession        = errors.New("invalid planner session")
	ErrInvalidContextBudget         = errors.New("invalid context budget")
	ErrInvalidPlannerConsumption    = errors.New("planner consumption must be positive")
	ErrPlannerSessionInactive       = errors.New("planner session is not active")
	ErrPlannerContextBudgetExceeded = errors.New("planner context budget exceeded")
	ErrInvalidPlannerCloseReason    = errors.New("invalid planner close reason")
	ErrPlannerSessionClosed         = errors.New("planner session is closed")
)

type PlannerSessionState string

const (
	PlannerSessionActive          PlannerSessionState = "ACTIVE"
	PlannerSessionHandoffRequired PlannerSessionState = "HANDOFF_REQUIRED"
	PlannerSessionClosed          PlannerSessionState = "CLOSED"
)

// ContextBudget uses abstract internal units, independent of any provider.
type ContextBudget struct {
	Limit int64
	Used  int64
}

func (b ContextBudget) Validate() error {
	if b.Limit <= 0 || b.Used < 0 || b.Used > b.Limit {
		return ErrInvalidContextBudget
	}
	return nil
}

// PlannerSession is disposable lifecycle control, never canonical project state.
// Its private fields prevent reopening, budget resets, and identity mutation.
type PlannerSession struct {
	sessionID   domain.SessionID
	projectID   domain.ProjectID
	taskID      domain.TaskID
	hasTask     bool
	budget      ContextBudget
	state       PlannerSessionState
	startReason domain.SessionStartReason
	closeReason domain.SessionCloseReason
}

func NewPlannerSession(sessionID domain.SessionID, projectID domain.ProjectID, taskID *domain.TaskID, budget ContextBudget, reason domain.SessionStartReason) (PlannerSession, error) {
	s := PlannerSession{sessionID: sessionID, projectID: projectID, budget: budget, state: PlannerSessionActive, startReason: reason}
	if taskID != nil {
		s.taskID = *taskID
		s.hasTask = true
	}
	if budget.Limit > 0 && budget.Used == budget.Limit {
		s.state = PlannerSessionHandoffRequired
	}
	if err := s.Validate(); err != nil {
		return PlannerSession{}, err
	}
	return s, nil
}

func (s PlannerSession) SessionID() domain.SessionID            { return s.sessionID }
func (s PlannerSession) ProjectID() domain.ProjectID            { return s.projectID }
func (s PlannerSession) TaskID() (domain.TaskID, bool)          { return s.taskID, s.hasTask }
func (s PlannerSession) Budget() ContextBudget                  { return s.budget }
func (s PlannerSession) State() PlannerSessionState             { return s.state }
func (s PlannerSession) StartReason() domain.SessionStartReason { return s.startReason }
func (s PlannerSession) CloseReason() domain.SessionCloseReason { return s.closeReason }

func (s PlannerSession) Validate() error {
	if strings.TrimSpace(string(s.sessionID)) == "" || strings.TrimSpace(string(s.projectID)) == "" {
		return ErrInvalidPlannerSession
	}
	if (s.hasTask && strings.TrimSpace(string(s.taskID)) == "") || (!s.hasTask && s.taskID != "") {
		return ErrInvalidPlannerSession
	}
	switch s.startReason {
	case domain.SessionStartReasonNewTask:
		if !s.hasTask {
			return ErrInvalidPlannerSession
		}
	case domain.SessionStartReasonInitial, domain.SessionStartReasonHandoff:
	default:
		return ErrInvalidPlannerSession
	}
	if err := s.budget.Validate(); err != nil {
		return err
	}
	switch s.state {
	case PlannerSessionActive:
		if s.budget.Used == s.budget.Limit || s.closeReason != "" {
			return ErrInvalidPlannerSession
		}
	case PlannerSessionHandoffRequired:
		if s.closeReason != "" {
			return ErrInvalidPlannerSession
		}
	case PlannerSessionClosed:
		if !validPlannerCloseReason(s.closeReason) {
			return ErrInvalidPlannerSession
		}
	default:
		return ErrInvalidPlannerSession
	}
	return nil
}

// Consume returns a new session. On insufficient capacity it returns a terminal
// handoff requirement with unchanged usage and ErrPlannerContextBudgetExceeded.
// Callers must retain that returned value even when capacity is exceeded.
// All other errors return the original value unchanged.
func (s PlannerSession) Consume(amount int64) (PlannerSession, error) {
	if err := s.Validate(); err != nil {
		return s, err
	}
	if s.state != PlannerSessionActive {
		return s, ErrPlannerSessionInactive
	}
	if amount <= 0 {
		return s, ErrInvalidPlannerConsumption
	}
	// Subtraction is safe for validated budgets and avoids overflowing addition.
	if amount > s.budget.Limit-s.budget.Used {
		s.state = PlannerSessionHandoffRequired
		return s, ErrPlannerContextBudgetExceeded
	}
	s.budget.Used += amount
	if s.budget.Used == s.budget.Limit {
		s.state = PlannerSessionHandoffRequired
	}
	return s, nil
}

func (s PlannerSession) Close(reason domain.SessionCloseReason) (PlannerSession, error) {
	if err := s.Validate(); err != nil {
		return s, err
	}
	if s.state == PlannerSessionClosed {
		return s, ErrPlannerSessionClosed
	}
	if !validPlannerCloseReason(reason) {
		return s, ErrInvalidPlannerCloseReason
	}
	s.state = PlannerSessionClosed
	s.closeReason = reason
	return s, nil
}

func validPlannerCloseReason(reason domain.SessionCloseReason) bool {
	switch reason {
	case domain.SessionCloseReasonDone, domain.SessionCloseReasonFailed, domain.SessionCloseReasonCancelled, domain.SessionCloseReasonContextBudget, domain.SessionCloseReasonReplaced:
		return true
	default:
		return false
	}
}
