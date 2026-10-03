package ports

import (
	"dev-orchestrator/internal/domain"
	"testing"
)

func TestActionResultClosedUnion(t *testing.T) {
	valid := []ActionResult{
		{Type: domain.ActionTypeSearch, SearchResult: &SearchResult{}},
		{Type: domain.ActionTypeReadFile, ReadFileResult: &ReadFileResult{}},
		{Type: domain.ActionTypeGitStatus, GitStatusResult: &GitStatusResult{}},
		{Type: domain.ActionTypeGitDiff, GitDiffResult: &GitDiffResult{}},
		{Type: domain.ActionTypeRunTests, RunTestsResult: &RunTestsResult{}},
	}
	for _, r := range valid {
		if err := r.Validate(); err != nil {
			t.Fatal(err)
		}
		wrong := r
		wrong.Type = "UNKNOWN"
		if wrong.Validate() == nil {
			t.Fatal("unknown accepted")
		}
		missing := ActionResult{Type: r.Type}
		if missing.Validate() == nil {
			t.Fatal("missing accepted")
		}
		for _, other := range valid {
			if other.Type == r.Type {
				continue
			}
			wrong = r
			wrong.Type = other.Type
			if wrong.Validate() == nil {
				t.Fatal("mismatch accepted")
			}
		}
		extra := r
		if r.SearchResult == nil {
			extra.SearchResult = &SearchResult{}
		} else {
			extra.ReadFileResult = &ReadFileResult{}
		}
		if extra.Validate() == nil {
			t.Fatal("multiple accepted")
		}
	}
	if (ActionResult{}).Validate() == nil {
		t.Fatal("zero accepted")
	}
}
