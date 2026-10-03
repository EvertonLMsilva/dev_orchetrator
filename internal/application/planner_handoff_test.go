package application

import (
	"reflect"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
)

func handoffFixture(t *testing.T) (PlannerSession, domain.PlannerHandoffPayload, PlannerHandoffMetadata) {
	t.Helper()
	task, status := domain.TaskID("task"), domain.TaskStatusAnalyzing
	s, err := NewPlannerSession("old", "project", &task, ContextBudget{Limit: 10}, domain.SessionStartReasonInitial)
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.Consume(10)
	if err != nil {
		t.Fatal(err)
	}
	return s, domain.PlannerHandoffPayload{ProjectID: "project", CurrentTask: &task, TaskStatus: &status, Decisions: []string{"Need more evidence"}, Evidence: []string{"Search found the contract"}, Completed: []domain.TaskID{"done"}, Blocked: []domain.TaskID{"blocked"}, NextAction: "Review the contract"}, PlannerHandoffMetadata{ProtocolVersion: "v1", MessageID: "handoff", CorrelationID: "correlation", CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
}

func TestPlannerHandoffBuild(t *testing.T) {
	for _, withTask := range []bool{false, true} {
		s, p, m := handoffFixture(t)
		if !withTask {
			s.hasTask = false
			s.taskID = ""
			p.CurrentTask = nil
			p.TaskStatus = nil
			p.Decisions = nil
			p.Evidence = nil
			p.Completed = nil
			p.Blocked = nil
		}
		before := s
		e, err := (PlannerHandoffBuilder{}).Build(s, p, m)
		if err != nil || e.Validate() != nil || e.MessageType != domain.MessageTypePlannerHandoff || e.ProjectID != s.ProjectID() || e.SessionID == nil || *e.SessionID != s.SessionID() || !reflect.DeepEqual(e.TaskID, p.CurrentTask) || !reflect.DeepEqual(e.Payload, p) {
			t.Fatalf("handoff: %+v %v", e, err)
		}
		if e.MessageID != m.MessageID || e.CorrelationID != m.CorrelationID || e.ProtocolVersion != m.ProtocolVersion || e.CreatedAt != m.CreatedAt || s != before {
			t.Fatal("metadata or session changed")
		}
		if withTask {
			*p.CurrentTask = "changed"
			*p.TaskStatus = domain.TaskStatusDone
			p.Decisions[0] = "changed"
			p.Evidence[0] = "changed"
			p.Completed[0] = "changed"
			p.Blocked[0] = "changed"
			got := e.Payload.(domain.PlannerHandoffPayload)
			if *got.CurrentTask != "task" || *e.TaskID != "task" || *got.TaskStatus != domain.TaskStatusAnalyzing || got.Decisions[0] != "Need more evidence" || got.Evidence[0] != "Search found the contract" || got.Completed[0] != "done" || got.Blocked[0] != "blocked" {
				t.Fatal("handoff aliases caller input")
			}
		}
	}
}

func TestPlannerHandoffRejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		change func(*PlannerSession, *domain.PlannerHandoffPayload, *PlannerHandoffMetadata)
	}{
		{"invalid session", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			*s = PlannerSession{}
		}},
		{"active", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			s.state = PlannerSessionActive
			s.budget.Used = 0
		}},
		{"closed", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			*s, _ = s.Close(domain.SessionCloseReasonContextBudget)
		}},
		{"project", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.ProjectID = "other"
		}},
		{"task", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			*p.CurrentTask = "other"
		}},
		{"missing task", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.CurrentTask = nil
			p.TaskStatus = nil
		}},
		{"unexpected task", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			s.hasTask = false
			s.taskID = ""
		}},
		{"status", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			*p.TaskStatus = "UNKNOWN"
		}},
		{"empty status", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			*p.TaskStatus = ""
		}},
		{"missing status", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.TaskStatus = nil
		}},
		{"orphan status", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			s.hasTask = false
			s.taskID = ""
			p.CurrentTask = nil
		}},
		{"decision", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.Decisions = []string{" \t"}
		}},
		{"evidence", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.Evidence = []string{""}
		}},
		{"completed", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.Completed = []domain.TaskID{" "}
		}},
		{"blocked", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.Blocked = []domain.TaskID{""}
		}},
		{"next empty", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) { p.NextAction = "" }},
		{"next blank", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			p.NextAction = " \t\n"
		}},
		{"version", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			m.ProtocolVersion = " "
		}},
		{"message", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) { m.MessageID = "" }},
		{"correlation", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			m.CorrelationID = ""
		}},
		{"timestamp", func(s *PlannerSession, p *domain.PlannerHandoffPayload, m *PlannerHandoffMetadata) {
			m.CreatedAt = time.Time{}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, m := handoffFixture(t)
			tc.change(&s, &p, &m)
			e, err := (PlannerHandoffBuilder{}).Build(s, p, m)
			if err == nil || !reflect.DeepEqual(e, domain.Envelope{}) {
				t.Fatalf("partial/accepted: %+v %v", e, err)
			}
		})
	}
}

func TestRestorePlannerSession(t *testing.T) {
	for _, withTask := range []bool{false, true} {
		s, p, m := handoffFixture(t)
		if !withTask {
			s.hasTask = false
			s.taskID = ""
			p.CurrentTask = nil
			p.TaskStatus = nil
		}
		e, err := (PlannerHandoffBuilder{}).Build(s, p, m)
		if err != nil {
			t.Fatal(err)
		}
		closed, err := s.Close(domain.SessionCloseReasonContextBudget)
		if err != nil {
			t.Fatal(err)
		}
		next, err := RestorePlannerSession(closed, e, "new", ContextBudget{Limit: 20})
		id, hasTask := next.TaskID()
		oldID, oldHasTask := s.TaskID()
		if err != nil || next.Validate() != nil || next.SessionID() != "new" || next.ProjectID() != s.ProjectID() || id != oldID || hasTask != oldHasTask || next.State() != PlannerSessionActive || next.StartReason() != domain.SessionStartReasonHandoff || next.Budget() != (ContextBudget{Limit: 20}) || closed.State() != PlannerSessionClosed || closed.CloseReason() != domain.SessionCloseReasonContextBudget || s.State() != PlannerSessionHandoffRequired {
			t.Fatalf("restore: %+v %v", next, err)
		}
	}
}

func TestRestorePlannerSessionRejectsInvalid(t *testing.T) {
	cases := []struct {
		name   string
		change func(*PlannerSession, *domain.Envelope, *domain.SessionID, *ContextBudget)
	}{
		{"empty ID", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { *id = " " }},
		{"same ID", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			*id = s.SessionID()
		}},
		{"budget", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { b.Limit = 0 }},
		{"used", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { b.Used = 1 }},
		{"negative used", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { b.Used = -1 }},
		{"unclosed", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			s.state = PlannerSessionHandoffRequired
			s.closeReason = ""
		}},
		{"wrong close", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			s.closeReason = domain.SessionCloseReasonDone
		}},
		{"invalid session", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			*s = PlannerSession{}
		}},
		{"message type", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			e.MessageType = domain.MessageTypeBotCommand
		}},
		{"metadata", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { e.MessageID = "" }},
		{"missing session", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { e.SessionID = nil }},
		{"wrong session", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			other := domain.SessionID("other")
			e.SessionID = &other
		}},
		{"project", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			e.ProjectID = "other"
		}},
		{"missing task", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { e.TaskID = nil }},
		{"payload", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) { e.Payload = nil }},
		{"payload identity", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			p := e.Payload.(domain.PlannerHandoffPayload)
			p.ProjectID = "other"
			e.Payload = p
		}},
		{"payload invalid", func(s *PlannerSession, e *domain.Envelope, id *domain.SessionID, b *ContextBudget) {
			p := e.Payload.(domain.PlannerHandoffPayload)
			p.NextAction = ""
			e.Payload = p
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, p, m := handoffFixture(t)
			e, err := (PlannerHandoffBuilder{}).Build(s, p, m)
			if err != nil {
				t.Fatal(err)
			}
			s, err = s.Close(domain.SessionCloseReasonContextBudget)
			if err != nil {
				t.Fatal(err)
			}
			id, b := domain.SessionID("new"), ContextBudget{Limit: 20}
			tc.change(&s, &e, &id, &b)
			before := s
			next, err := RestorePlannerSession(s, e, id, b)
			if err == nil || next != (PlannerSession{}) || s != before {
				t.Fatalf("partial/accepted: %+v %v", next, err)
			}
		})
	}
}
