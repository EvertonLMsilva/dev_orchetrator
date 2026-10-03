package application_test

import (
	"testing"

	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
)

func TestDefaultActionAllowlist(t *testing.T) {
	allowlist := application.NewDefaultActionAllowlist()
	for _, tc := range []struct {
		actionType domain.ActionType
		want       bool
	}{
		{domain.ActionTypeSearch, true},
		{domain.ActionTypeReadFile, true},
		{domain.ActionTypeGitStatus, true},
		{domain.ActionTypeGitDiff, true},
		{domain.ActionTypeRunTests, true},
		{"", false},
		{"UNKNOWN", false},
		{"FUTURE_ACTION", false},
		{"SHELL", false},
		{"search", false},
		{" SEARCH ", false},
	} {
		t.Run(string(tc.actionType), func(t *testing.T) {
			if got := allowlist.Allows(tc.actionType); got != tc.want {
				t.Fatalf("Allows(%q) = %v; want %v", tc.actionType, got, tc.want)
			}
		})
	}
}

func TestActionAllowlistZeroValueDeniesAll(t *testing.T) {
	var allowlist application.ActionAllowlist
	for _, actionType := range []domain.ActionType{
		domain.ActionTypeSearch, domain.ActionTypeReadFile,
		domain.ActionTypeGitStatus, domain.ActionTypeGitDiff,
		domain.ActionTypeRunTests, "", "FUTURE_ACTION",
	} {
		if allowlist.Allows(actionType) {
			t.Fatalf("zero-value allowlist permits %q without explicit registration", actionType)
		}
	}
}

func TestActionAllowlistInstancesAreIndependent(t *testing.T) {
	allowlist := application.NewDefaultActionAllowlist()
	copy := allowlist
	copy = application.ActionAllowlist{}
	if copy.Allows(domain.ActionTypeSearch) {
		t.Fatal("replacement with zero value must deny SEARCH")
	}
	if !allowlist.Allows(domain.ActionTypeSearch) {
		t.Fatal("replacing an external copy changed the original allowlist")
	}
	if allowlist.Allows("FUTURE_ACTION") {
		t.Fatal("unregistered capability became permitted")
	}
}
