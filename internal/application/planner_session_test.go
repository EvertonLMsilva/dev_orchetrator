package application

import (
	"errors"
	"math"
	"testing"

	"dev-orchestrator/internal/domain"
)

func newTestSession(t *testing.T) PlannerSession {
	t.Helper()
	s, err := NewPlannerSession("session", "project", nil, ContextBudget{Limit: 10}, domain.SessionStartReasonInitial)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlannerSessionCreation(t *testing.T) {
	task := domain.TaskID("task")
	s, err := NewPlannerSession("session", "project", &task, ContextBudget{Limit: 10, Used: 2}, domain.SessionStartReasonNewTask)
	if err != nil || s.Validate() != nil || s.State() != PlannerSessionActive || s.SessionID() != "session" || s.ProjectID() != "project" || s.StartReason() != domain.SessionStartReasonNewTask {
		t.Fatalf("session: %+v, %v", s, err)
	}
	task = "changed"
	id, present := s.TaskID()
	if !present || id != "task" {
		t.Fatal("task identity was aliased")
	}
	for _, reason := range []domain.SessionStartReason{domain.SessionStartReasonInitial, domain.SessionStartReasonHandoff} {
		s, err := NewPlannerSession("s", "p", nil, ContextBudget{Limit: 1, Used: 1}, reason)
		if err != nil || s.State() != PlannerSessionHandoffRequired {
			t.Fatalf("exhausted creation: %v", err)
		}
	}
	for _, tc := range []struct {
		name   string
		sid    domain.SessionID
		pid    domain.ProjectID
		task   *domain.TaskID
		budget ContextBudget
		reason domain.SessionStartReason
	}{
		{"session", " ", "p", nil, ContextBudget{Limit: 1}, domain.SessionStartReasonInitial},
		{"project", "s", " ", nil, ContextBudget{Limit: 1}, domain.SessionStartReasonInitial},
		{"empty task", "s", "p", new(domain.TaskID), ContextBudget{Limit: 1}, domain.SessionStartReasonInitial},
		{"required task", "s", "p", nil, ContextBudget{Limit: 1}, domain.SessionStartReasonNewTask},
		{"zero limit", "s", "p", nil, ContextBudget{}, domain.SessionStartReasonInitial},
		{"negative limit", "s", "p", nil, ContextBudget{Limit: -1}, domain.SessionStartReasonInitial},
		{"negative used", "s", "p", nil, ContextBudget{Limit: 1, Used: -1}, domain.SessionStartReasonInitial},
		{"excess used", "s", "p", nil, ContextBudget{Limit: 1, Used: 2}, domain.SessionStartReasonInitial},
		{"unknown reason", "s", "p", nil, ContextBudget{Limit: 1}, "unknown"},
		{"zero reason", "s", "p", nil, ContextBudget{Limit: 1}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewPlannerSession(tc.sid, tc.pid, tc.task, tc.budget, tc.reason)
			if err == nil || s != (PlannerSession{}) {
				t.Fatalf("accepted invalid creation: %+v %v", s, err)
			}
		})
	}
}

func TestPlannerSessionConsume(t *testing.T) {
	for _, tc := range []struct {
		name    string
		amount  int64
		used    int64
		state   PlannerSessionState
		wantErr error
	}{
		{"below", 3, 3, PlannerSessionActive, nil},
		{"exact", 10, 10, PlannerSessionHandoffRequired, nil},
		{"above", 11, 0, PlannerSessionHandoffRequired, ErrPlannerContextBudgetExceeded},
		{"zero", 0, 0, PlannerSessionActive, ErrInvalidPlannerConsumption},
		{"negative", -1, 0, PlannerSessionActive, ErrInvalidPlannerConsumption},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			next, err := s.Consume(tc.amount)
			if !errors.Is(err, tc.wantErr) || next.Budget().Used != tc.used || next.State() != tc.state || next.Validate() != nil {
				t.Fatalf("result: %+v %v", next, err)
			}
			if s.Budget().Used != 0 || s.State() != PlannerSessionActive {
				t.Fatal("input mutated")
			}
		})
	}
	s, _ := NewPlannerSession("s", "p", nil, ContextBudget{Limit: math.MaxInt64, Used: math.MaxInt64 - 1}, domain.SessionStartReasonInitial)
	next, err := s.Consume(math.MaxInt64)
	if !errors.Is(err, ErrPlannerContextBudgetExceeded) || next.Budget() != s.Budget() || next.State() != PlannerSessionHandoffRequired {
		t.Fatalf("overflow: %+v %v", next, err)
	}
	for _, amount := range []int64{10, 11} {
		s := newTestSession(t)
		s, _ = s.Consume(amount)
		next, err := s.Consume(1)
		if err == nil || next != s {
			t.Fatal("handoff accepted consumption")
		}
	}
	s = newTestSession(t)
	s, _ = s.Close(domain.SessionCloseReasonDone)
	next, err = s.Consume(1)
	if err == nil || next != s {
		t.Fatal("closed accepted consumption")
	}
}

func TestPlannerSessionClose(t *testing.T) {
	for _, reason := range []domain.SessionCloseReason{domain.SessionCloseReasonDone, domain.SessionCloseReasonFailed, domain.SessionCloseReasonCancelled, domain.SessionCloseReasonContextBudget, domain.SessionCloseReasonReplaced} {
		for _, handoff := range []bool{false, true} {
			s := newTestSession(t)
			if handoff {
				s, _ = s.Consume(10)
			}
			next, err := s.Close(reason)
			if err != nil || next.Validate() != nil || next.State() != PlannerSessionClosed || next.CloseReason() != reason || next.Budget() != s.Budget() {
				t.Fatalf("close: %+v %v", next, err)
			}
			again, err := next.Close(reason)
			if err == nil || again != next {
				t.Fatal("second close accepted")
			}
		}
	}
	for _, reason := range []domain.SessionCloseReason{"", "unknown"} {
		s := newTestSession(t)
		next, err := s.Close(reason)
		if err == nil || next != s {
			t.Fatal("invalid close changed session")
		}
	}
}

func TestPlannerSessionValidation(t *testing.T) {
	s := newTestSession(t)
	for _, invalid := range []PlannerSession{{}, func() PlannerSession { v := s; v.state = "unknown"; return v }(), func() PlannerSession { v := s; v.budget.Used = v.budget.Limit; return v }(), func() PlannerSession { v := s; v.closeReason = domain.SessionCloseReasonDone; return v }(), func() PlannerSession { v := s; v.state = PlannerSessionClosed; return v }()} {
		if invalid.Validate() == nil {
			t.Fatal("invalid session validated")
		}
		next, err := invalid.Consume(1)
		if err == nil || next != invalid {
			t.Fatal("invalid session consumed")
		}
		next, err = invalid.Close(domain.SessionCloseReasonDone)
		if err == nil || next != invalid {
			t.Fatal("invalid session closed")
		}
	}
}
