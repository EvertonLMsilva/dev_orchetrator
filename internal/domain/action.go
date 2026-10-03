package domain

import (
	"errors"
	"strings"
)

var (
	ErrActionTypeRequired      = errors.New("action type is required")
	ErrUnknownActionType       = errors.New("unknown action type")
	ErrActionProjectIDRequired = errors.New("action project ID is required")
	ErrActionTaskIDRequired    = errors.New("action task ID is required when supplied")
	ErrInvalidActionParams     = errors.New("parameters do not match action type")
	ErrSearchQueryRequired     = errors.New("search query is required")
	ErrReadFilePathRequired    = errors.New("read file path is required")
	ErrTestTargetRequired      = errors.New("test target is required")
	ErrInvalidTestTarget       = errors.New("invalid test target ID")
)

type ActionType string

const (
	ActionTypeSearch    ActionType = "SEARCH"
	ActionTypeReadFile  ActionType = "READ_FILE"
	ActionTypeGitStatus ActionType = "GIT_STATUS"
	ActionTypeGitDiff   ActionType = "GIT_DIFF"
	ActionTypeRunTests  ActionType = "RUN_TESTS"
)

type SearchParams struct {
	Query string
	Path  string
}
type ReadFileParams struct{ Path string }
type GitDiffParams struct{ Path string }

// TestTargetID names a configured test target, never a command or argument list.
// Resolution and authorization of the target belong to later tasks.
type TestTargetID string
type RunTestsParams struct{ Target TestTargetID }

// ActionParams is a closed union. Exactly the variant matching Type must be
// present; GIT_STATUS requires all variants to be absent.
type ActionParams struct {
	Search   *SearchParams
	ReadFile *ReadFileParams
	GitDiff  *GitDiffParams
	RunTests *RunTestsParams
}

type Action struct {
	Type      ActionType
	ProjectID ProjectID
	TaskID    *TaskID
	Params    ActionParams
}

func NewAction(actionType ActionType, projectID ProjectID, taskID *TaskID, params ActionParams) (Action, error) {
	a := Action{Type: actionType, ProjectID: projectID, TaskID: taskID, Params: params}
	if err := a.Validate(); err != nil {
		return Action{}, err
	}
	return a, nil
}

func (a Action) Validate() error {
	if strings.TrimSpace(string(a.Type)) == "" {
		return ErrActionTypeRequired
	}
	switch a.Type {
	case ActionTypeSearch, ActionTypeReadFile, ActionTypeGitStatus, ActionTypeGitDiff, ActionTypeRunTests:
	default:
		return ErrUnknownActionType
	}
	if strings.TrimSpace(string(a.ProjectID)) == "" {
		return ErrActionProjectIDRequired
	}
	if a.TaskID != nil && strings.TrimSpace(string(*a.TaskID)) == "" {
		return ErrActionTaskIDRequired
	}
	p := a.Params
	if (p.Search != nil) != (a.Type == ActionTypeSearch) ||
		(p.ReadFile != nil) != (a.Type == ActionTypeReadFile) ||
		(p.GitDiff != nil) != (a.Type == ActionTypeGitDiff) ||
		(p.RunTests != nil) != (a.Type == ActionTypeRunTests) {
		return ErrInvalidActionParams
	}
	switch a.Type {
	case ActionTypeSearch:
		if strings.TrimSpace(p.Search.Query) == "" {
			return ErrSearchQueryRequired
		}
	case ActionTypeReadFile:
		if strings.TrimSpace(p.ReadFile.Path) == "" {
			return ErrReadFilePathRequired
		}
	case ActionTypeRunTests:
		if strings.TrimSpace(string(p.RunTests.Target)) == "" {
			return ErrTestTargetRequired
		}
		// A symbolic key starts with an ASCII letter or digit and may contain
		// only letters, digits, underscores and hyphens. Values are not trimmed.
		for i, c := range string(p.RunTests.Target) {
			alphanumeric := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
			if !alphanumeric && (i == 0 || c != '_' && c != '-') {
				return ErrInvalidTestTarget
			}
		}
	}
	return nil
}
