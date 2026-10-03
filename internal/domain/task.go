package domain

import (
	"errors"
	"strings"
)

var (
	ErrTaskIDRequired        = errors.New("task ID is required")
	ErrTaskProjectIDRequired = errors.New("task project ID is required")
	ErrTaskTitleRequired     = errors.New("task title is required")
	ErrInvalidTaskTransition = errors.New("invalid task transition")
)

type TaskID string

type TaskStatus string

const (
	TaskStatusPlanned          TaskStatus = "PLANNED"
	TaskStatusReadyForAnalysis TaskStatus = "READY_FOR_ANALYSIS"
	TaskStatusAnalyzing        TaskStatus = "ANALYZING"
	TaskStatusReadyForCodex    TaskStatus = "READY_FOR_CODEX"
	TaskStatusInProgress       TaskStatus = "IN_PROGRESS"
	TaskStatusDone             TaskStatus = "DONE"
	TaskStatusBlocked          TaskStatus = "BLOCKED"
	TaskStatusFailed           TaskStatus = "FAILED"
	TaskStatusCancelled        TaskStatus = "CANCELLED"
)

type Task struct {
	ID        TaskID
	ProjectID ProjectID
	Title     string
	Status    TaskStatus
}

func (t Task) CanTransitionTo(next TaskStatus) bool {
	switch t.Status {
	case TaskStatusPlanned:
		return next == TaskStatusReadyForAnalysis || next == TaskStatusCancelled
	case TaskStatusReadyForAnalysis:
		return next == TaskStatusAnalyzing || next == TaskStatusCancelled
	case TaskStatusAnalyzing:
		return next == TaskStatusReadyForCodex || next == TaskStatusBlocked || next == TaskStatusCancelled
	case TaskStatusReadyForCodex:
		return next == TaskStatusInProgress || next == TaskStatusBlocked || next == TaskStatusCancelled
	case TaskStatusInProgress:
		return next == TaskStatusDone || next == TaskStatusBlocked || next == TaskStatusFailed || next == TaskStatusCancelled
	case TaskStatusBlocked:
		return next == TaskStatusReadyForAnalysis || next == TaskStatusReadyForCodex || next == TaskStatusCancelled
	}
	return false
}

func (t Task) TransitionTo(next TaskStatus) (Task, error) {
	if !t.CanTransitionTo(next) {
		return Task{}, ErrInvalidTaskTransition
	}
	t.Status = next
	return t, nil
}

func NewTask(id TaskID, projectID ProjectID, title string) (Task, error) {
	if strings.TrimSpace(string(id)) == "" {
		return Task{}, ErrTaskIDRequired
	}
	if strings.TrimSpace(string(projectID)) == "" {
		return Task{}, ErrTaskProjectIDRequired
	}
	if strings.TrimSpace(title) == "" {
		return Task{}, ErrTaskTitleRequired
	}
	return Task{ID: id, ProjectID: projectID, Title: title, Status: TaskStatusPlanned}, nil
}
