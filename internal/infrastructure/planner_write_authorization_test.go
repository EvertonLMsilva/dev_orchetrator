package infrastructure_test

import (
	"reflect"
	"testing"

	"dev-orchestrator/internal/infrastructure"
	"dev-orchestrator/internal/ports"
)

// P6.3d supplies symbolic authorization only; it does not mediate real Codex effects.
// Every operation below is symbolic: no filesystem, Git, network or credential
// mutation is executed, and no temporary workspace is created.
func TestPlannerReviewWriteAuthorization(t *testing.T) {
	const workspace = "/authorized/workspace"
	allow := ports.PlannerWriteReview{
		Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: "project", TaskID: "task", Workspace: workspace,
	}
	write := ports.PlannerWriteOperation{
		ProjectID: "project", TaskID: "task", Workspace: workspace, Kind: "filesystem.modify", Target: workspace + "/existing.go",
	}
	cases := []struct {
		name      string
		review    ports.PlannerWriteReview
		operation ports.PlannerWriteOperation
		want      string
	}{
		{"default_deny", ports.PlannerWriteReview{}, write, "DENY"},
		{"allow_without_review_boundary", ports.PlannerWriteReview{Decision: "ALLOW", Workspace: workspace}, write, "DENY"},
		{"wrong_boundary", ports.PlannerWriteReview{Boundary: "EXECUTOR", Decision: "ALLOW", Workspace: workspace}, write, "DENY"},
		{"explicit_deny", ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "DENY", Workspace: workspace}, write, "DENY"},
		{"auto_is_not_authorization", ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "AUTO", Workspace: workspace}, write, "DENY"},
		{"approval_is_not_allow", ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "APPROVAL", Workspace: workspace}, write, "DENY"},
		{"missing_workspace", ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "ALLOW"}, write, "DENY"},
		{"modify_in_workspace", allow, write, "ALLOW"},
		{"create_in_workspace", allow, ports.PlannerWriteOperation{Kind: "filesystem.create", Target: workspace + "/new.go"}, "ALLOW"},
		{"outside_workspace", allow, ports.PlannerWriteOperation{Kind: "filesystem.modify", Target: "/other/existing.go"}, "DENY"},
		{"sibling_prefix", allow, ports.PlannerWriteOperation{Kind: "filesystem.create", Target: workspace + "-other/new.go"}, "DENY"},
		{"parent_escape", allow, ports.PlannerWriteOperation{Kind: "filesystem.modify", Target: workspace + "/../outside.go"}, "DENY"},
		{"delete", allow, ports.PlannerWriteOperation{Kind: "filesystem.delete", Target: workspace + "/existing.go"}, "DENY"},
		{"git_commit", allow, ports.PlannerWriteOperation{Kind: "git.commit", Target: workspace}, "DENY"},
		{"git_push", allow, ports.PlannerWriteOperation{Kind: "git.push", Target: workspace}, "DENY"},
		{"git_branch", allow, ports.PlannerWriteOperation{Kind: "git.branch", Target: workspace}, "DENY"},
		{"network_write", allow, ports.PlannerWriteOperation{Kind: "network.write", Target: "https://example.invalid/resource"}, "DENY"},
		{"credentials_write", allow, ports.PlannerWriteOperation{Kind: "credentials.write", Target: workspace + "/credentials"}, "DENY"},
		{"unknown_operation", allow, ports.PlannerWriteOperation{Kind: "unknown", Target: workspace}, "DENY"},
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
			got := infrastructure.AuthorizePlannerWrite(tc.review, tc.operation)
			if got.Decision != tc.want {
				t.Fatalf("decision = %q, want %q", got.Decision, tc.want)
			}
			if got.Decision == "ALLOW" && got.Workspace != workspace {
				t.Fatal("ALLOW must carry exactly the authorized workspace scope")
			}
			if got.Decision == "DENY" && got.Workspace != "" {
				t.Fatal("DENY must not grant a workspace scope")
			}
			if !reflect.DeepEqual(tc.review, beforeReview) || !reflect.DeepEqual(tc.operation, beforeOperation) {
				t.Fatal("authorization must not mutate its inputs")
			}
		})
	}
}

func TestPlannerWriteApprovalBoundByValue(t *testing.T) {
	operation := ports.PlannerWriteOperation{ProjectID: "project", TaskID: "task", Workspace: "/workspace", Kind: "filesystem.modify", Target: "/workspace/file"}
	review := ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: operation.ProjectID, TaskID: operation.TaskID, Workspace: operation.Workspace, Operation: operation}
	cases := []struct {
		name   string
		change func(*ports.PlannerWriteOperation)
	}{
		{"project", func(o *ports.PlannerWriteOperation) { o.ProjectID = "other" }},
		{"task", func(o *ports.PlannerWriteOperation) { o.TaskID = "other" }},
		{"workspace", func(o *ports.PlannerWriteOperation) { o.Workspace = "/other" }},
		{"kind", func(o *ports.PlannerWriteOperation) { o.Kind = "filesystem.create" }},
		{"target", func(o *ports.PlannerWriteOperation) { o.Target = "/workspace/other" }},
		{"empty_project", func(o *ports.PlannerWriteOperation) { o.ProjectID = "" }},
		{"empty_task", func(o *ports.PlannerWriteOperation) { o.TaskID = "" }},
		{"relative", func(o *ports.PlannerWriteOperation) { o.Target = "file" }},
		{"backslash", func(o *ports.PlannerWriteOperation) { o.Target = "/workspace/back\\file" }},
		{"nul", func(o *ports.PlannerWriteOperation) { o.Target = "/workspace/\x00file" }},
		{"colon", func(o *ports.PlannerWriteOperation) { o.Target = "/workspace/file:stream" }},
		{"dot", func(o *ports.PlannerWriteOperation) { o.Target = "/workspace/./file" }},
		{"workspace_itself", func(o *ports.PlannerWriteOperation) { o.Target = "/workspace" }},
	}
	if got := infrastructure.AuthorizePlannerWrite(review, operation); got != review {
		t.Fatalf("exact approval = %#v, want %#v", got, review)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := operation
			tc.change(&changed)
			sink := &plannerWriteMemoryExecutor{}
			got, err := infrastructure.NewPlannerWriteDispatcher(sink).Dispatch(review, changed)
			if err != nil || got != (ports.PlannerWriteReview{Decision: "DENY"}) || len(sink.operations) != 0 {
				t.Fatalf("changed operation accepted: decision=%#v err=%v calls=%d", got, err, len(sink.operations))
			}
		})
	}
	invalid := []ports.PlannerWriteReview{review, review, review, review}
	invalid[0].Operation = ports.PlannerWriteOperation{}
	invalid[1].ProjectID = ""
	invalid[2].TaskID = ""
	invalid[3].Workspace = "/workspace/.."
	for _, r := range invalid {
		if got := infrastructure.AuthorizePlannerWrite(r, operation); got.Decision != "DENY" {
			t.Fatalf("invalid approval accepted: %#v", r)
		}
	}
}

// Even an exactly matching approval cannot authorize unsafe paths or kinds.
func TestPlannerWriteMatchingUnsafeApproval(t *testing.T) {
	for _, target := range []string{"/other/file", "/workspace-other/file", "/workspace/../file", "/workspace/./file", "/workspace", "relative", "/workspace/file:stream", "/workspace/back\\file", "/workspace/\x00file"} {
		operation := ports.PlannerWriteOperation{ProjectID: "project", TaskID: "task", Workspace: "/workspace", Kind: "filesystem.create", Target: target}
		review := ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: "project", TaskID: "task", Workspace: "/workspace", Operation: operation}
		sink := &plannerWriteMemoryExecutor{}
		got, err := infrastructure.NewPlannerWriteDispatcher(sink).Dispatch(review, operation)
		if err != nil || got.Decision != "DENY" || len(sink.operations) != 0 {
			t.Fatalf("unsafe target %q accepted: %#v %v", target, got, err)
		}
	}
}

// Invalid context remains DENY even when review and operation match exactly.
func TestPlannerWriteMatchingInvalidContext(t *testing.T) {
	cases := []struct{ project, task, workspace string }{
		{"", "task", "/workspace"},
		{"project", "", "/workspace"},
		{"project", "task", ""},
		{"project", "task", "relative"},
		{"project", "task", "/workspace/.."},
		{"project", "task", "/workspace/"},
		{"project", "task", "/work:space"},
		{"project", "task", "/work\\space"},
		{"project", "task", "/work\x00space"},
	}
	for _, tc := range cases {
		operation := ports.PlannerWriteOperation{ProjectID: tc.project, TaskID: tc.task, Workspace: tc.workspace, Kind: "filesystem.create", Target: tc.workspace + "/file"}
		review := ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: tc.project, TaskID: tc.task, Workspace: tc.workspace, Operation: operation}
		sink := &plannerWriteMemoryExecutor{}
		got, err := infrastructure.NewPlannerWriteDispatcher(sink).Dispatch(review, operation)
		if err != nil || got != (ports.PlannerWriteReview{Decision: "DENY"}) || len(sink.operations) != 0 {
			t.Fatalf("invalid matching context accepted: %#v decision=%#v err=%v", tc, got, err)
		}
	}
}

// A complete operation binding cannot compensate for invalid review state.
func TestPlannerWriteInvalidReviewState(t *testing.T) {
	operation := ports.PlannerWriteOperation{ProjectID: "project", TaskID: "task", Workspace: "/workspace", Kind: "filesystem.modify", Target: "/workspace/file"}
	for _, state := range []struct{ boundary, decision string }{
		{"", "ALLOW"}, {"EXECUTOR", "ALLOW"}, {"PLANNER_REVIEW", ""},
		{"PLANNER_REVIEW", "DENY"}, {"PLANNER_REVIEW", "AUTO"}, {"PLANNER_REVIEW", "APPROVAL"},
	} {
		review := ports.PlannerWriteReview{Boundary: state.boundary, Decision: state.decision, ProjectID: "project", TaskID: "task", Workspace: "/workspace", Operation: operation}
		sink := &plannerWriteMemoryExecutor{}
		got, err := infrastructure.NewPlannerWriteDispatcher(sink).Dispatch(review, operation)
		if err != nil || got != (ports.PlannerWriteReview{Decision: "DENY"}) || len(sink.operations) != 0 {
			t.Fatalf("invalid review state accepted: %#v decision=%#v err=%v", state, got, err)
		}
	}
}

func TestPlannerWriteDispatcherRejectsForbiddenKinds(t *testing.T) {
	for _, kind := range []string{"", "filesystem.delete", "git.commit", "git.push", "git.branch", "network.write", "credentials.write", "unknown"} {
		operation := ports.PlannerWriteOperation{ProjectID: "project", TaskID: "task", Workspace: "/workspace", Kind: kind, Target: "/workspace/file"}
		review := ports.PlannerWriteReview{Boundary: "PLANNER_REVIEW", Decision: "ALLOW", ProjectID: "project", TaskID: "task", Workspace: "/workspace", Operation: operation}
		sink := &plannerWriteMemoryExecutor{}
		got, err := infrastructure.NewPlannerWriteDispatcher(sink).Dispatch(review, operation)
		if err != nil || got != (ports.PlannerWriteReview{Decision: "DENY"}) || len(sink.operations) != 0 {
			t.Fatalf("forbidden kind %q reached downstream: %#v %v", kind, got, err)
		}
	}
}
