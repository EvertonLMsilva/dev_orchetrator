package infrastructure_test

import (
	"errors"
	"testing"

	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
)

// This sink records symbolic operations only. It performs no external I/O.
type plannerWriteMemoryExecutor struct {
	operations []ports.PlannerWriteOperation
}

func (e *plannerWriteMemoryExecutor) Execute(operation ports.PlannerWriteOperation) error {
	e.operations = append(e.operations, operation)
	return nil
}

// Only the symbolic sink implements this shared contract.
var _ ports.WriteExecutor = (*plannerWriteMemoryExecutor)(nil)

func TestPlannerWriteDispatcher(t *testing.T) {
	const workspace = "/authorized/workspace"
	allow := ports.PlannerWriteReview{
		Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: "project", TaskID: "task", Workspace: workspace,
	}
	modify := ports.PlannerWriteOperation{
		ProjectID: "project", TaskID: "task", Workspace: workspace, Kind: "filesystem.modify", Target: workspace + "/existing.go",
	}
	cases := []struct {
		name      string
		review    ports.PlannerWriteReview
		operation ports.PlannerWriteOperation
		decision  string
		calls     int
	}{
		{"missing_review", ports.PlannerWriteReview{}, modify, "DENY", 0},
		{"explicit_deny", ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "DENY", Workspace: workspace}, modify, "DENY", 0},
		{"wrong_boundary", ports.PlannerWriteReview{Boundary: "EXECUTOR", Decision: "ALLOW", Workspace: workspace}, modify, "DENY", 0},
		{"unsupported_operation", allow, ports.PlannerWriteOperation{Kind: "filesystem.delete", Target: workspace + "/existing.go"}, "DENY", 0},
		{"allow_modify", allow, modify, "ALLOW", 1},
		{"allow_create", allow, ports.PlannerWriteOperation{Kind: "filesystem.create", Target: workspace + "/new.go"}, "ALLOW", 1},
		{"declared_allow_outside_workspace", allow, ports.PlannerWriteOperation{Kind: "filesystem.modify", Target: "/other/existing.go"}, "DENY", 0},
		{"sibling_prefix", allow, ports.PlannerWriteOperation{Kind: "filesystem.create", Target: workspace + "-other/new.go"}, "DENY", 0},
		{"parent_escape", allow, ports.PlannerWriteOperation{Kind: "filesystem.modify", Target: workspace + "/../outside.go"}, "DENY", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.review == allow {
				tc.operation.ProjectID = "project"
				tc.operation.TaskID = "task"
				tc.operation.Workspace = workspace
				tc.review.Operation = tc.operation
			}
			beforeReview, beforeOperation := tc.review, tc.operation
			// The existing policy is the oracle; the dispatcher must use it as
			// its mandatory gate rather than introduce a separate policy.
			want := infrastructure.AuthorizePlannerWrite(tc.review, tc.operation)
			if want.Decision != tc.decision {
				t.Fatalf("policy decision = %q, want %q", want.Decision, tc.decision)
			}
			sink := &plannerWriteMemoryExecutor{}
			var dispatcher *infrastructure.PlannerWriteDispatcher = infrastructure.NewPlannerWriteDispatcher(sink)
			got, err := dispatcher.Dispatch(tc.review, tc.operation)
			if err != nil {
				t.Fatalf("Dispatch with in-memory executor: %v", err)
			}
			if got != want {
				t.Fatalf("dispatch review = %#v, policy review = %#v", got, want)
			}
			if len(sink.operations) != tc.calls {
				t.Fatalf("downstream calls = %d, want %d", len(sink.operations), tc.calls)
			}
			if tc.calls == 1 && sink.operations[0] != beforeOperation {
				t.Fatalf("forwarded operation = %#v, authorized operation = %#v", sink.operations[0], beforeOperation)
			}
			if tc.review != beforeReview || tc.operation != beforeOperation {
				t.Fatal("dispatch must preserve its inputs")
			}
		})
	}
}

type plannerWriteErrorExecutor struct {
	calls     int
	err       error
	operation ports.PlannerWriteOperation
}

func (e *plannerWriteErrorExecutor) Execute(operation ports.PlannerWriteOperation) error {
	e.calls++
	e.operation = operation
	return e.err
}
func TestPlannerWriteDispatcherPropagatesErrorWithoutRetry(t *testing.T) {
	operation := ports.PlannerWriteOperation{ProjectID: "project", TaskID: "task", Workspace: "/workspace", Kind: "filesystem.create", Target: "/workspace/file"}
	review := ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: "project", TaskID: "task", Workspace: "/workspace", Operation: operation}
	expected := errors.New("symbolic downstream failed")
	sink := &plannerWriteErrorExecutor{err: expected}
	got, err := infrastructure.NewPlannerWriteDispatcher(sink).Dispatch(review, operation)
	if got != review || err != expected || sink.calls != 1 || sink.operation != operation {
		t.Fatalf("decision=%#v err=%v calls=%d operation=%#v", got, err, sink.calls, sink.operation)
	}
}
