package domain

import "testing"

func TestTaskTransitions(t *testing.T) {
	statuses := []TaskStatus{
		TaskStatusPlanned, TaskStatusReadyForAnalysis, TaskStatusAnalyzing,
		TaskStatusReadyForCodex, TaskStatusInProgress, TaskStatusBlocked,
		TaskStatusDone, TaskStatusFailed, TaskStatusCancelled, "UNKNOWN", "",
	}
	valid := map[TaskStatus][]TaskStatus{
		TaskStatusPlanned:          {TaskStatusReadyForAnalysis, TaskStatusCancelled},
		TaskStatusReadyForAnalysis: {TaskStatusAnalyzing, TaskStatusCancelled},
		TaskStatusAnalyzing:        {TaskStatusReadyForCodex, TaskStatusBlocked, TaskStatusCancelled},
		TaskStatusReadyForCodex:    {TaskStatusInProgress, TaskStatusBlocked, TaskStatusCancelled},
		TaskStatusInProgress:       {TaskStatusDone, TaskStatusBlocked, TaskStatusFailed, TaskStatusCancelled},
		TaskStatusBlocked:          {TaskStatusReadyForAnalysis, TaskStatusReadyForCodex, TaskStatusCancelled},
	}
	for _, current := range statuses {
		for _, next := range statuses {
			t.Run(string(current)+"/"+string(next), func(t *testing.T) {
				wantValid := false
				for _, allowed := range valid[current] {
					if next == allowed {
						wantValid = true
					}
				}
				original := Task{ID: " task-1 ", ProjectID: " project-1 ", Title: " My Task ", Status: current}
				task := original
				if got := task.CanTransitionTo(next); got != wantValid {
					t.Errorf("CanTransitionTo(%q) = %v, want %v", next, got, wantValid)
				}
				if task != original {
					t.Errorf("CanTransitionTo changed original: %+v", task)
				}
				got, err := task.TransitionTo(next)
				if task != original {
					t.Errorf("TransitionTo changed original: %+v", task)
				}
				if !wantValid {
					if err != ErrInvalidTaskTransition {
						t.Errorf("TransitionTo(%q) error = %v, want sentinel %v", next, err, ErrInvalidTaskTransition)
					}
					return
				}
				if err != nil {
					t.Fatalf("TransitionTo(%q) error = %v", next, err)
				}
				want := original
				want.Status = next
				if got != want {
					t.Errorf("TransitionTo(%q) = %+v, want %+v", next, got, want)
				}
			})
		}
	}
}

func TestNewTaskPreservesOriginalValues(t *testing.T) {
	id := TaskID(" \ttask-1\n")
	projectID := ProjectID(" \tproject-1\n")
	title := " \tMy Task\n"

	got, err := NewTask(id, projectID, title)
	if err != nil {
		t.Fatalf("NewTask() error = %v", err)
	}
	if got.ID != id {
		t.Errorf("ID = %q, want %q", got.ID, id)
	}
	if got.ProjectID != projectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, projectID)
	}
	if got.Title != title {
		t.Errorf("Title = %q, want %q", got.Title, title)
	}
	if got.Status != TaskStatusPlanned {
		t.Errorf("Status = %q, want %q", got.Status, TaskStatusPlanned)
	}
}

func TestNewTaskRequiredFields(t *testing.T) {
	tests := []struct {
		name      string
		id        TaskID
		projectID ProjectID
		title     string
		wantErr   error
	}{
		{"empty ID", "", "project", "title", ErrTaskIDRequired},
		{"whitespace ID", " \t\r\n\u2003", "project", "title", ErrTaskIDRequired},
		{"empty ProjectID", "id", "", "title", ErrTaskProjectIDRequired},
		{"whitespace ProjectID", "id", " \t\r\n\u2003", "title", ErrTaskProjectIDRequired},
		{"empty Title", "id", "project", "", ErrTaskTitleRequired},
		{"whitespace Title", "id", "project", " \t\r\n\u2003", ErrTaskTitleRequired},
		{"ID precedes ProjectID and Title", "", "", "", ErrTaskIDRequired},
		{"ProjectID precedes Title", "id", "", "", ErrTaskProjectIDRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewTask(tt.id, tt.projectID, tt.title)
			if err != tt.wantErr {
				t.Errorf("NewTask() error = %v, want sentinel %v", err, tt.wantErr)
			}
			if got != (Task{}) {
				t.Errorf("NewTask() = %+v, want zero Task on error", got)
			}
		})
	}
}

func TestTaskStatusValues(t *testing.T) {
	tests := []struct {
		status TaskStatus
		want   string
	}{
		{TaskStatusPlanned, "PLANNED"},
		{TaskStatusReadyForAnalysis, "READY_FOR_ANALYSIS"},
		{TaskStatusAnalyzing, "ANALYZING"},
		{TaskStatusReadyForCodex, "READY_FOR_CODEX"},
		{TaskStatusInProgress, "IN_PROGRESS"},
		{TaskStatusDone, "DONE"},
		{TaskStatusBlocked, "BLOCKED"},
		{TaskStatusFailed, "FAILED"},
		{TaskStatusCancelled, "CANCELLED"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Errorf("status = %q, want %q", tt.status, tt.want)
			}
		})
	}
}
