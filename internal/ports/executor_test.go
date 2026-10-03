package ports

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"testing"
)

func executorSpecForTest() ExecutorTaskSpec {
	return ExecutorTaskSpec{Objective: "Add validation", Scope: []string{"internal/example.go"}, AcceptanceCriteria: []string{"Reject invalid inputs"}}
}
func TestExecutorTaskSpecValidate(t *testing.T) {
	if err := executorSpecForTest().Validate(); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*ExecutorTaskSpec)
	}{
		{"objective", func(s *ExecutorTaskSpec) { s.Objective = "" }},
		{"blank objective", func(s *ExecutorTaskSpec) { s.Objective = " \t\n\u2003" }},
		{"scope", func(s *ExecutorTaskSpec) { s.Scope = nil }},
		{"criteria", func(s *ExecutorTaskSpec) { s.AcceptanceCriteria = nil }},
		{"criterion", func(s *ExecutorTaskSpec) { s.AcceptanceCriteria = []string{" \t"} }},
		{"constraint", func(s *ExecutorTaskSpec) { s.Constraints = []string{" \t"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := executorSpecForTest()
			tc.change(&s)
			if !errors.Is(s.Validate(), ErrInvalidExecutorTaskSpec) {
				t.Fatal("invalid spec accepted")
			}
		})
	}
	for _, path := range []string{"", " \t", "/etc/passwd", "C:/file", "C:\\file", "C:file", "\\\\server\\share", "\\root", "../file", "a/../file", "a\\..\\file", ".", "a/./file", "a//file", "a/", "a\\file", "*.go", "a?b", "a\x00b", "a\nb", "a:b", " file", "file ", "file.", "a\"b", "a<b", "a>b", "a|b"} {
		t.Run(path, func(t *testing.T) {
			s := executorSpecForTest()
			s.Scope = []string{path}
			if !errors.Is(s.Validate(), ErrInvalidExecutorTaskSpec) {
				t.Fatal("unsafe scope accepted")
			}
		})
	}
	for _, path := range []string{"file.go", "internal/application/file.go", "docs/my file.md", "docs/ação.md", ".github/workflows/test.yml"} {
		s := executorSpecForTest()
		s.Scope = []string{path}
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestExecutorRequestValidate(t *testing.T) {
	valid := ExecutorRequest{ProjectID: "project", TaskID: "task", Spec: executorSpecForTest()}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := []ExecutorRequest{{}, {ProjectID: "project", TaskID: "task"}}
	for _, v := range []string{"", " \t\n\u2003"} {
		r := valid
		r.ProjectID = domain.ProjectID(v)
		invalid = append(invalid, r)
		r = valid
		r.TaskID = domain.TaskID(v)
		invalid = append(invalid, r)
	}
	for _, r := range invalid {
		if !errors.Is(r.Validate(), ErrInvalidExecutorRequest) {
			t.Fatal("invalid request accepted")
		}
	}
}
func TestExecutorResultValidate(t *testing.T) {
	for _, outcome := range []ExecutorOutcome{ExecutorOutcomeDone, ExecutorOutcomeBlocked, ExecutorOutcomeFailed} {
		valid := ExecutorResult{ProjectID: "project", TaskID: "task", Outcome: outcome, Summary: "Work result"}
		if err := valid.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, v := range []ExecutorOutcome{"", "UNKNOWN", "CANCELLED", " DONE "} {
			r := valid
			r.Outcome = v
			if !errors.Is(r.Validate(), ErrInvalidExecutorResult) {
				t.Fatal("unknown outcome accepted")
			}
		}
		for _, v := range []string{"", " \t\n\u2003"} {
			r := valid
			r.Summary = v
			if !errors.Is(r.Validate(), ErrInvalidExecutorResult) {
				t.Fatal("invalid summary accepted")
			}
			r = valid
			r.ProjectID = domain.ProjectID(v)
			if !errors.Is(r.Validate(), ErrInvalidExecutorResult) {
				t.Fatal("invalid project accepted")
			}
			r = valid
			r.TaskID = domain.TaskID(v)
			if !errors.Is(r.Validate(), ErrInvalidExecutorResult) {
				t.Fatal("invalid task accepted")
			}
		}
	}
}

type failedExecutor struct{}

func (failedExecutor) Execute(context.Context, ExecutorRequest) (ExecutorResult, error) {
	return ExecutorResult{ProjectID: "project", TaskID: "task", Outcome: ExecutorOutcomeFailed, Summary: "Unable to finish authorized work"}, nil
}
func TestExecutorFailedIsProtocolResult(t *testing.T) {
	var e Executor = failedExecutor{}
	r, err := e.Execute(context.Background(), ExecutorRequest{ProjectID: "project", TaskID: "task", Spec: executorSpecForTest()})
	if err != nil || r.Validate() != nil || r.Outcome != ExecutorOutcomeFailed {
		t.Fatal("FAILED must be a valid protocol result")
	}
}
