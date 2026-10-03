package domain

import (
	"reflect"
	"testing"
)

func validActions() []Action {
	return []Action{
		{Type: ActionTypeSearch, ProjectID: " project ", Params: ActionParams{Search: &SearchParams{Query: " query ", Path: " path "}}},
		{Type: ActionTypeReadFile, ProjectID: " project ", Params: ActionParams{ReadFile: &ReadFileParams{Path: " path "}}},
		{Type: ActionTypeGitStatus, ProjectID: " project "},
		{Type: ActionTypeGitDiff, ProjectID: " project ", Params: ActionParams{GitDiff: &GitDiffParams{Path: " path "}}},
		{Type: ActionTypeRunTests, ProjectID: " project ", Params: ActionParams{RunTests: &RunTestsParams{Target: "unit-tests"}}},
	}
}

func TestNewActionValid(t *testing.T) {
	wantTypes := []ActionType{"SEARCH", "READ_FILE", "GIT_STATUS", "GIT_DIFF", "RUN_TESTS"}
	for i, a := range validActions() {
		for _, present := range []bool{false, true} {
			task := TaskID(" task ")
			if present {
				a.TaskID = &task
			}
			got, err := NewAction(a.Type, a.ProjectID, a.TaskID, a.Params)
			if err != nil {
				t.Fatal(err)
			}
			if a.Type != wantTypes[i] || !reflect.DeepEqual(got, a) {
				t.Fatalf("action not preserved: %+v", got)
			}
		}
	}
	for _, a := range []Action{
		{Type: ActionTypeSearch, ProjectID: "p", Params: ActionParams{Search: &SearchParams{Query: "q"}}},
		{Type: ActionTypeGitDiff, ProjectID: "p", Params: ActionParams{GitDiff: &GitDiffParams{}}},
	} {
		if err := a.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActionValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Action)
		want   error
	}{
		{"empty type", func(a *Action) { a.Type = "" }, ErrActionTypeRequired},
		{"blank type", func(a *Action) { a.Type = " \t\n" }, ErrActionTypeRequired},
		{"unknown", func(a *Action) { a.Type = "UNKNOWN" }, ErrUnknownActionType},
		{"shell", func(a *Action) { a.Type = "SHELL" }, ErrUnknownActionType},
		{"padded type", func(a *Action) { a.Type = " SEARCH " }, ErrUnknownActionType},
		{"empty project", func(a *Action) { a.ProjectID = "" }, ErrActionProjectIDRequired},
		{"blank project", func(a *Action) { a.ProjectID = " \t\n\u2003" }, ErrActionProjectIDRequired},
		{"empty task", func(a *Action) { id := TaskID(""); a.TaskID = &id }, ErrActionTaskIDRequired},
		{"blank task", func(a *Action) { id := TaskID(" \t\n\u2003"); a.TaskID = &id }, ErrActionTaskIDRequired},
		{"missing params", func(a *Action) { a.Params = ActionParams{} }, ErrInvalidActionParams},
		{"empty query", func(a *Action) { a.Params.Search.Query = "" }, ErrSearchQueryRequired},
		{"blank query", func(a *Action) { a.Params.Search.Query = " \t\u2003" }, ErrSearchQueryRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validActions()[0]
			tc.change(&a)
			before := a
			if err := a.Validate(); err != tc.want {
				t.Fatalf("Validate = %v, want %v", err, tc.want)
			}
			got, err := NewAction(a.Type, a.ProjectID, a.TaskID, a.Params)
			if err != tc.want || !reflect.DeepEqual(got, Action{}) {
				t.Fatalf("NewAction = %+v, %v", got, err)
			}
			if !reflect.DeepEqual(a, before) {
				t.Fatal("validation mutated action")
			}
		})
	}
}

func TestActionRejectsMixedOrMismatchedParams(t *testing.T) {
	actions := validActions()
	for i, a := range actions {
		for j, other := range actions {
			if i == j {
				continue
			}
			bad := a
			bad.Params = other.Params
			if err := bad.Validate(); err != ErrInvalidActionParams {
				t.Fatalf("%s with %s params: %v", a.Type, other.Type, err)
			}
		}
	}
	for _, a := range actions {
		if a.Type == ActionTypeSearch {
			a.Params.ReadFile = &ReadFileParams{Path: "p"}
		} else {
			a.Params.Search = &SearchParams{Query: "q"}
		}
		if err := a.Validate(); err != ErrInvalidActionParams {
			t.Fatalf("mixed %s: %v", a.Type, err)
		}
	}
}

func TestActionRequiredPathAndTestTarget(t *testing.T) {
	for _, value := range []string{"", " \t\n\u2003"} {
		a := validActions()[1]
		a.Params.ReadFile.Path = value
		if err := a.Validate(); err != ErrReadFilePathRequired {
			t.Fatal(err)
		}
		a = validActions()[4]
		a.Params.RunTests.Target = TestTargetID(value)
		if err := a.Validate(); err != ErrTestTargetRequired {
			t.Fatal(err)
		}
	}
	for _, value := range []TestTargetID{"go test ./...", "unit;whoami", "$(cmd)", "-flag", "../tests"} {
		a := validActions()[4]
		a.Params.RunTests.Target = value
		if err := a.Validate(); err != ErrInvalidTestTarget {
			t.Fatalf("target %q: %v", value, err)
		}
	}
}
