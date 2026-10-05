package application

import (
	"reflect"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func codexTaskRequestForTest() CodexTaskRequest {
	session := domain.SessionID("session")
	return CodexTaskRequest{
		Decision: ports.PlannerDecision{ProjectID: "project", TaskID: "task", Type: ports.PlannerDecisionPrepareExecutor, Reason: "Work is authorized"},
		Spec:     ports.ExecutorTaskSpec{Objective: "Add validation", Scope: []string{"internal/example.go"}, AcceptanceCriteria: []string{"Invalid inputs are rejected"}},
		Metadata: CodexTaskMetadata{ProtocolVersion: "v1", MessageID: "message", CorrelationID: "correlation", SessionID: &session, CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)},
	}
}

func TestCodexTaskBuilderPreservesContract(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		r := codexTaskRequestForTest()
		if multiple {
			r.Spec.Scope = append(r.Spec.Scope, "internal/example_test.go")
			r.Spec.Constraints = []string{"Preserve public API", "Do not add dependencies"}
			r.Spec.AcceptanceCriteria = append(r.Spec.AcceptanceCriteria, "Valid inputs are accepted")
		} else {
			r.Metadata.SessionID = nil
		}
		got, err := (CodexTaskBuilder{}).Build(r)
		if err != nil {
			t.Fatal(err)
		}
		payload, ok := got.Payload.(CodexTask)
		if !ok || !reflect.DeepEqual(payload.Spec, r.Spec) || payload.Validate() != nil || got.Validate() != nil {
			t.Fatal("invalid task contract")
		}
		if got.MessageType != domain.MessageTypeCodexTask || got.ProjectID != r.Decision.ProjectID || got.TaskID == nil || *got.TaskID != r.Decision.TaskID {
			t.Fatal("identity not preserved")
		}
		if got.ProtocolVersion != r.Metadata.ProtocolVersion || got.MessageID != r.Metadata.MessageID || got.CorrelationID != r.Metadata.CorrelationID || got.CreatedAt != r.Metadata.CreatedAt || !reflect.DeepEqual(got.SessionID, r.Metadata.SessionID) {
			t.Fatal("metadata not preserved")
		}
		// The returned authority must not change when the caller edits its input.
		r.Spec.Scope[0] = "other.go"
		r.Spec.AcceptanceCriteria[0] = "Changed"
		if multiple {
			r.Spec.Constraints[0] = "Changed"
			*r.Metadata.SessionID = "changed"
		}
		if payload.Spec.Scope[0] != "internal/example.go" || payload.Spec.AcceptanceCriteria[0] != "Invalid inputs are rejected" || (multiple && (payload.Spec.Constraints[0] != "Preserve public API" || *got.SessionID != "session")) {
			t.Fatal("task aliases input")
		}
	}
}

func TestCodexTaskBuilderRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name   string
		change func(*CodexTaskRequest)
	}{
		{"zero decision", func(r *CodexTaskRequest) { r.Decision = ports.PlannerDecision{} }},
		{"project", func(r *CodexTaskRequest) { r.Decision.ProjectID = " " }},
		{"task", func(r *CodexTaskRequest) { r.Decision.TaskID = "" }},
		{"reason", func(r *CodexTaskRequest) { r.Decision.Reason = " " }},
		{"unknown decision", func(r *CodexTaskRequest) { r.Decision.Type = "UNKNOWN" }},
		{"request evidence", func(r *CodexTaskRequest) {
			r.Decision.Type = ports.PlannerDecisionRequestEvidence
			r.Decision.EvidenceKind = domain.ActionTypeSearch
		}},
		{"block", func(r *CodexTaskRequest) { r.Decision.Type = ports.PlannerDecisionBlock }},
		{"unexpected evidence", func(r *CodexTaskRequest) { r.Decision.EvidenceKind = domain.ActionTypeSearch }},
		{"zero spec", func(r *CodexTaskRequest) { r.Spec = ports.ExecutorTaskSpec{} }},
		{"objective empty", func(r *CodexTaskRequest) { r.Spec.Objective = "" }},
		{"objective blank", func(r *CodexTaskRequest) { r.Spec.Objective = " \t\n\u2003" }},
		{"scope empty", func(r *CodexTaskRequest) { r.Spec.Scope = nil }},
		{"constraint empty", func(r *CodexTaskRequest) { r.Spec.Constraints = []string{""} }},
		{"constraint blank", func(r *CodexTaskRequest) { r.Spec.Constraints = []string{" \t\u2003"} }},
		{"criteria empty", func(r *CodexTaskRequest) { r.Spec.AcceptanceCriteria = nil }},
		{"criterion empty", func(r *CodexTaskRequest) { r.Spec.AcceptanceCriteria = []string{""} }},
		{"criterion blank", func(r *CodexTaskRequest) { r.Spec.AcceptanceCriteria = []string{" \n\u2003"} }},
		{"version", func(r *CodexTaskRequest) { r.Metadata.ProtocolVersion = " " }},
		{"message", func(r *CodexTaskRequest) { r.Metadata.MessageID = "" }},
		{"correlation", func(r *CodexTaskRequest) { r.Metadata.CorrelationID = " " }},
		{"timestamp", func(r *CodexTaskRequest) { r.Metadata.CreatedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := codexTaskRequestForTest()
			tc.change(&r)
			got, err := (CodexTaskBuilder{}).Build(r)
			if err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
				t.Fatalf("expected closed failure: %+v, %v", got, err)
			}
		})
	}
}

func TestExecutorTaskSpecScope(t *testing.T) {
	for _, path := range []string{"", " \t", "/etc/passwd", "C:/file", "C:\\file", "C:file", "\\\\server\\share", "\\root", "../file", "a/../file", "a\\..\\file", ".", "a/./file", "a//file", "a/", "a\\file", "*.go", "a?b", "a\x00b", "a\nb", "a:b", " file", "file "} {
		t.Run(path, func(t *testing.T) {
			r := codexTaskRequestForTest()
			r.Spec.Scope = []string{path}
			if r.Spec.Validate() == nil || (CodexTask{Spec: r.Spec}).Validate() == nil {
				t.Fatal("unsafe scope accepted")
			}
			got, err := (CodexTaskBuilder{}).Build(r)
			if err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
				t.Fatal("unsafe scope built")
			}
		})
	}
	for _, path := range []string{"file.go", "internal/application/file.go", "docs/my file.md", "docs/ação.md", ".github/workflows/test.yml"} {
		r := codexTaskRequestForTest()
		r.Spec.Scope = []string{path}
		if _, err := (CodexTaskBuilder{}).Build(r); err != nil {
			t.Fatalf("safe scope %q: %v", path, err)
		}
	}
}
