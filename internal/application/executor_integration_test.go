package application

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

// Only repository, runtime and session ID generation are deterministic boundaries.
// The application chain needs no TaskStatus, workflow or provider metadata.
func TestExecutorIntegratedResultFlows(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome ports.ExecutorOutcome
		summary string
	}{
		{"done", ports.ExecutorOutcomeDone, "done summary"},
		{"blocked", ports.ExecutorOutcomeBlocked, "blocked summary"},
		{"failed_result", ports.ExecutorOutcomeFailed, "failed summary"},
		{"summary_outcome_isolation", ports.ExecutorOutcomeDone, "BLOCKED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := packageRequest()
			request.ProjectID = domain.ProjectID("integration-project-" + tc.name)
			request.TaskID = domain.TaskID("integration-task-" + tc.name)
			before := packageRequest()
			before.ProjectID, before.TaskID = request.ProjectID, request.TaskID
			workspace := t.TempDir()
			root, err := filepath.EvalSymlinks(workspace)
			if err != nil {
				t.Fatal(err)
			}
			repo := &packageProjects{project: domain.Project{ID: request.ProjectID, Workspace: workspace}, found: true}
			limits, err := NewExecutionLimits(time.Minute, len(tc.summary))
			if err != nil {
				t.Fatal(err)
			}
			var sessions []ExecutorSession
			idCalls, runtimeCalls := 0, 0
			manager := NewExecutorSessionManager(func() (domain.SessionID, error) {
				idCalls++
				return "integration-session", nil
			})
			parent := context.Background()
			var bounded context.Context
			runtime := adapterRuntime(func(ctx context.Context, got ports.RuntimeExecutionRequest) (ports.RuntimeExecutionResult, error) {
				runtimeCalls++
				bounded = ctx
				if ctx == parent || ctx.Err() != nil {
					t.Fatal("runtime requires an active bounded context")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("execution timeout missing")
				}
				if len(sessions) != 1 || sessions[0].State() != ExecutorSessionActive {
					t.Fatal("runtime must execute during the active session")
				}
				want := ports.RuntimeExecutionRequest{
					ProjectID: request.ProjectID, TaskID: request.TaskID, Workspace: root,
					Objective: request.Spec.Objective, Scope: request.Spec.Scope,
					Constraints: request.Spec.Constraints, AcceptanceCriteria: request.Spec.AcceptanceCriteria,
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("runtime request = %#v, want %#v", got, want)
				}
				return ports.RuntimeExecutionResult{Outcome: tc.outcome, Summary: tc.summary}, nil
			})
			adapter := NewCodexProviderAdapter(NewExecutionPackageBuilder(repo), runtime,
				WithExecutorExecutionLimits(limits),
				WithExecutorSessionLifecycle(manager, func(s ExecutorSession) { sessions = append(sessions, s) }))
			result, err := adapter.Execute(parent, request)
			if err != nil {
				t.Fatal(err)
			}
			wantResult := ports.ExecutorResult{ProjectID: request.ProjectID, TaskID: request.TaskID, Outcome: tc.outcome, Summary: tc.summary}
			if result != wantResult || result.Validate() != nil {
				t.Fatalf("executor result = %#v, want %#v", result, wantResult)
			}
			if runtimeCalls != 1 || idCalls != 1 || repo.requested != request.ProjectID || len(sessions) != 2 {
				t.Fatalf("runtime=%d IDs=%d repository=%s sessions=%+v", runtimeCalls, idCalls, repo.requested, sessions)
			}
			for i, session := range sessions {
				wantState := []ExecutorSessionState{ExecutorSessionActive, ExecutorSessionCompleted}[i]
				if session.Validate() != nil || session.State() != wantState || session.SessionID() != "integration-session" || session.ProjectID() != request.ProjectID || session.TaskID() != request.TaskID {
					t.Fatalf("session[%d] = %+v", i, session)
				}
			}
			if bounded.Err() != context.Canceled || !reflect.DeepEqual(request, before) {
				t.Fatal("context not released or request mutated")
			}
			sessionID := sessions[1].SessionID()
			metadata := CodexResultMetadata{ProtocolVersion: "v1", MessageID: "integration-message", CorrelationID: "integration-correlation",
				SessionID: &sessionID, CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
			envelope, err := (ExecutorResultNormalizer{}).Normalize(result, metadata)
			if err != nil {
				t.Fatal(err)
			}
			wire, err := EncodeCodexResult(envelope)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeCodexResult(wire)
			if err != nil {
				t.Fatal(err)
			}
			for _, stage := range []struct {
				name  string
				value domain.Envelope
			}{{"normalized", envelope}, {"decoded", decoded}} {
				e := stage.value
				if err := ValidateCodexResultEnvelope(e); err != nil {
					t.Fatalf("%s invalid: %v", stage.name, err)
				}
				if e.ProjectID != request.ProjectID || e.TaskID == nil || *e.TaskID != request.TaskID {
					t.Fatalf("%s lost request correlation: %#v", stage.name, e)
				}
				payload, ok := e.Payload.(CodexResult)
				if !ok || payload.Outcome != tc.outcome || payload.Summary != tc.summary {
					t.Fatalf("%s changed outcome or summary: %#v", stage.name, e.Payload)
				}
				if e.MessageType != domain.MessageTypeCodexResult || e.ProtocolVersion != metadata.ProtocolVersion || e.MessageID != metadata.MessageID || e.CorrelationID != metadata.CorrelationID || e.SessionID == nil || *e.SessionID != sessionID || !e.CreatedAt.Equal(metadata.CreatedAt) {
					t.Fatalf("%s changed canonical metadata: %#v", stage.name, e)
				}
			}
		})
	}
}
