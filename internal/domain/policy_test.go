package domain

import (
	"reflect"
	"testing"
)

func TestPolicyDecisionValidation(t *testing.T) {
	for _, decision := range []PolicyDecision{PolicyDecisionAuto, PolicyDecisionApproval, PolicyDecisionBlocked} {
		if err := decision.Validate(); err != nil {
			t.Fatalf("%q: %v", decision, err)
		}
	}
	for _, decision := range []PolicyDecision{"", "UNKNOWN", " AUTO ", "auto"} {
		if err := decision.Validate(); err != ErrUnknownPolicyDecision {
			t.Fatalf("%q: %v", decision, err)
		}
	}
}

func TestPolicyResultValidation(t *testing.T) {
	for _, decision := range []PolicyDecision{PolicyDecisionAuto, PolicyDecisionApproval, PolicyDecisionBlocked} {
		if err := (PolicyResult{Decision: decision, Reason: "explicit policy classification"}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, reason := range []string{"", " \t\n\u2003"} {
		if err := (PolicyResult{Decision: PolicyDecisionAuto, Reason: reason}).Validate(); err != ErrPolicyReasonRequired {
			t.Fatalf("reason %q: %v", reason, err)
		}
	}
	if err := (PolicyResult{Decision: "UNKNOWN", Reason: "reason"}).Validate(); err != ErrUnknownPolicyDecision {
		t.Fatal(err)
	}
}

func TestActionPolicyEvaluate(t *testing.T) {
	for _, action := range validActions() {
		t.Run(string(action.Type), func(t *testing.T) {
			before := action
			result, err := (ActionPolicy{}).Evaluate(action)
			if err != nil || result.Decision != PolicyDecisionAuto {
				t.Fatalf("Evaluate = %+v, %v", result, err)
			}
			if err := result.Validate(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(action, before) {
				t.Fatal("policy mutated action")
			}
		})
	}
}

func TestActionPolicyFailClosed(t *testing.T) {
	cases := []Action{{}, {Type: "UNKNOWN", ProjectID: "p"}, {Type: "SHELL", ProjectID: "p"}}
	for _, valid := range validActions() {
		missingProject := valid
		missingProject.ProjectID = ""
		cases = append(cases, missingProject)
		mixed := valid
		mixed.Params = ActionParams{Search: &SearchParams{Query: "q"}, ReadFile: &ReadFileParams{Path: "p"}}
		cases = append(cases, mixed)
	}
	cases = append(cases,
		Action{Type: ActionTypeSearch, ProjectID: "p", Params: ActionParams{Search: &SearchParams{Query: " "}}},
		Action{Type: ActionTypeReadFile, ProjectID: "p", Params: ActionParams{ReadFile: &ReadFileParams{Path: " "}}},
		Action{Type: ActionTypeRunTests, ProjectID: "p", Params: ActionParams{RunTests: &RunTestsParams{Target: "go test ./..."}}},
	)
	for _, action := range cases {
		want := action.Validate()
		result, err := (ActionPolicy{}).Evaluate(action)
		if want == nil || err != want || result.Decision != PolicyDecisionBlocked {
			t.Fatalf("Evaluate(%+v) = %+v, %v; want BLOCKED, %v", action, result, err, want)
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
