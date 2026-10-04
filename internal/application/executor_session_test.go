package application

import (
	"errors"
	"reflect"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func testExecutorSession(t *testing.T) ExecutorSession {
	t.Helper()
	s, err := NewExecutorSessionManager(func() (domain.SessionID, error) { return "session", nil }).Create("project", "task")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestExecutorSessionCreation(t *testing.T) {
	m := NewExecutorSessionManager(nil)
	a, err := m.Create("project", "task")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Create("project", "task")
	if err != nil {
		t.Fatal(err)
	}
	if a.Validate() != nil || a.State() != ExecutorSessionActive || a.ProjectID() != "project" || a.TaskID() != "task" || a.SessionID() == "" || a.SessionID() == b.SessionID() || a.TaskID() != b.TaskID() {
		t.Fatalf("invalid attempts: %+v %+v", a, b)
	}
	for _, ids := range [][2]string{{"", "task"}, {"project", " "}} {
		s, err := m.Create(domain.ProjectID(ids[0]), domain.TaskID(ids[1]))
		if !errors.Is(err, ErrInvalidExecutorSession) || s != (ExecutorSession{}) {
			t.Fatal("accepted invalid identity")
		}
	}
}

func TestExecutorSessionGeneratorFailure(t *testing.T) {
	failure := errors.New("generator failure")
	for _, tc := range []struct {
		id   domain.SessionID
		err  error
		want error
	}{
		{"", failure, failure}, {" ", nil, ErrInvalidExecutorSession},
	} {
		m := NewExecutorSessionManager(func() (domain.SessionID, error) { return tc.id, tc.err })
		s, err := m.Create("project", "task")
		if !errors.Is(err, tc.want) || s != (ExecutorSession{}) {
			t.Fatalf("got %+v %v", s, err)
		}
	}
}

func TestExecutorSessionTransitions(t *testing.T) {
	for _, state := range []ExecutorSessionState{ExecutorSessionCompleted, ExecutorSessionFailed, ExecutorSessionCancelled} {
		t.Run(string(state), func(t *testing.T) {
			s := testExecutorSession(t)
			var next ExecutorSession
			var err error
			switch state {
			case ExecutorSessionCompleted:
				next, err = s.Complete()
			case ExecutorSessionFailed:
				next, err = s.Fail()
			case ExecutorSessionCancelled:
				next, err = s.Cancel()
			}
			if err != nil || next.State() != state || next.Validate() != nil || next.SessionID() != s.SessionID() || next.ProjectID() != s.ProjectID() || next.TaskID() != s.TaskID() || s.State() != ExecutorSessionActive {
				t.Fatalf("transition: %+v %v", next, err)
			}
			for _, target := range []ExecutorSessionState{ExecutorSessionActive, ExecutorSessionCompleted, ExecutorSessionFailed, ExecutorSessionCancelled, "BLOCKED", "unknown", ""} {
				got, err := next.transition(target)
				if err == nil || got != next {
					t.Fatal("terminal transition accepted")
				}
			}
			for _, finish := range []func() (ExecutorSession, error){next.Complete, next.Fail, next.Cancel} {
				got, err := finish()
				if !errors.Is(err, ErrInvalidExecutorSessionTransition) || got != next {
					t.Fatal("terminal session reused")
				}
			}
		})
	}
	s := testExecutorSession(t)
	for _, state := range []ExecutorSessionState{ExecutorSessionActive, "BLOCKED", "unknown", ""} {
		got, err := s.transition(state)
		if !errors.Is(err, ErrInvalidExecutorSessionTransition) || got != s {
			t.Fatal("invalid transition accepted")
		}
	}
}

func TestExecutorSessionInvalidFailsClosed(t *testing.T) {
	s := testExecutorSession(t)
	for _, change := range []func(*ExecutorSession){
		func(s *ExecutorSession) { s.sessionID = "" }, func(s *ExecutorSession) { s.projectID = " " },
		func(s *ExecutorSession) { s.taskID = "" }, func(s *ExecutorSession) { s.state = "BLOCKED" },
	} {
		invalid := s
		change(&invalid)
		for _, finish := range []func() (ExecutorSession, error){invalid.Complete, invalid.Fail, invalid.Cancel} {
			got, err := finish()
			if !errors.Is(err, ErrInvalidExecutorSession) || got != invalid {
				t.Fatal("invalid session changed")
			}
		}
	}
	if (ExecutorSession{}).Validate() == nil {
		t.Fatal("zero session valid")
	}
}

func TestExecutorSessionOutcomeAndTaskIndependence(t *testing.T) {
	for _, outcome := range []ports.ExecutorOutcome{ports.ExecutorOutcomeBlocked, ports.ExecutorOutcomeFailed} {
		task := domain.Task{ID: "task", ProjectID: "project", Status: domain.TaskStatusInProgress}
		before := task
		result := ports.ExecutorResult{ProjectID: task.ProjectID, TaskID: task.ID, Outcome: outcome, Summary: "valid result"}
		if result.Validate() != nil {
			t.Fatal("invalid fixture")
		}
		s, err := testExecutorSession(t).Complete()
		if err != nil || s.State() != ExecutorSessionCompleted || task != before || result.Outcome != outcome {
			t.Fatal("outcome or task coupled to lifecycle")
		}
	}
}

func TestExecutorSessionProviderIndependentShape(t *testing.T) {
	typ := reflect.TypeOf(ExecutorSession{})
	want := map[string]reflect.Type{"sessionID": reflect.TypeOf(domain.SessionID("")), "projectID": reflect.TypeOf(domain.ProjectID("")), "taskID": reflect.TypeOf(domain.TaskID("")), "state": reflect.TypeOf(ExecutorSessionState(""))}
	if typ.NumField() != len(want) {
		t.Fatal("unexpected session data")
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.IsExported() || want[f.Name] != f.Type {
			t.Fatalf("unexpected field %s", f.Name)
		}
	}
}
