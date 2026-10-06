package infrastructure

import (
	"path"
	"strings"

	"dev-orchestrator/internal/ports"
)

// AuthorizePlannerWrite decides symbolic authorization only.
// It does not mediate real Codex effects; real WRITE remains DENY.
func AuthorizePlannerWrite(review ports.PlannerWriteReview, operation ports.PlannerWriteOperation) ports.PlannerWriteReview {
	deny := ports.PlannerWriteReview{Decision: "DENY"}
	if review.Boundary != "PLANNER_REVIEW" || review.Decision != "ALLOW" {
		return deny
	}
	if review.ProjectID == "" || review.TaskID == "" ||
		review.ProjectID != operation.ProjectID || review.TaskID != operation.TaskID ||
		review.Workspace != operation.Workspace || review.Operation != operation {
		return deny
	}
	if operation.Kind != "filesystem.modify" && operation.Kind != "filesystem.create" {
		return deny
	}
	for _, value := range []string{review.Workspace, operation.Target} {
		if !path.IsAbs(value) || strings.ContainsAny(value, "\\\x00:") || path.Clean(value) != value {
			return deny
		}
	}
	if !strings.HasPrefix(operation.Target, strings.TrimSuffix(review.Workspace, "/")+"/") || operation.Target == review.Workspace {
		return deny
	}
	return review
}
