package application

import (
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"reflect"
	"testing"
	"time"
)

func plannerResultFixture() (domain.Envelope, PlannerEvidenceExpectation) {
	task := domain.TaskID("task")
	session := domain.SessionID("session")
	expected := PlannerEvidenceExpectation{ProjectID: "project", TaskID: task, CorrelationID: "correlation", SessionID: &session, EvidenceKind: domain.ActionTypeSearch, ProtocolVersion: "v1"}
	envelope := domain.Envelope{ProtocolVersion: "v1", MessageType: domain.MessageTypeBotResult, MessageID: "result", CorrelationID: expected.CorrelationID, ProjectID: expected.ProjectID, TaskID: &task, SessionID: &session, CreatedAt: time.Now(), Payload: BotResult{ActionType: expected.EvidenceKind, Status: BotResultSuccess, Result: &ports.ActionResult{Type: domain.ActionTypeSearch, SearchResult: &ports.SearchResult{Matches: []ports.SearchMatch{{Path: "file", Line: 3, Text: "needle"}}, Limited: true}}}}
	return envelope, expected
}

func TestPlannerEvidenceTypedResults(t *testing.T) {
	results := []ports.ActionResult{
		{Type: domain.ActionTypeSearch, SearchResult: &ports.SearchResult{Matches: []ports.SearchMatch{{Path: "file", Line: 3, Text: "needle"}}, Limited: true}},
		{Type: domain.ActionTypeReadFile, ReadFileResult: &ports.ReadFileResult{Path: "file", Content: "content", Size: 7}},
		{Type: domain.ActionTypeGitStatus, GitStatusResult: &ports.GitStatusResult{Entries: []ports.GitStatusEntry{{Path: "file", IndexStatus: "M"}}}},
		{Type: domain.ActionTypeGitDiff, GitDiffResult: &ports.GitDiffResult{Path: "file", Diff: "diff", Bytes: 4}},
		{Type: domain.ActionTypeRunTests, RunTestsResult: &ports.RunTestsResult{Target: "unit", Stdout: "out", Stderr: "err", Success: false, ExitCode: 1}},
	}
	for _, result := range results {
		t.Run(string(result.Type), func(t *testing.T) {
			e, want := plannerResultFixture()
			want.EvidenceKind = result.Type
			payload := BotResult{ActionType: result.Type, Status: BotResultSuccess, Result: &result}
			e.Payload = payload
			before := e
			got, err := ConsumePlannerEvidence(e, want)
			if err != nil {
				t.Fatal(err)
			}
			if got.ProjectID != want.ProjectID || got.TaskID != want.TaskID || got.CorrelationID != want.CorrelationID || !reflect.DeepEqual(got.BotResult, payload) {
				t.Fatalf("evidence lost semantics: %+v", got)
			}
			if !reflect.DeepEqual(e, before) {
				t.Fatal("input mutated")
			}
		})
	}
	e, want := plannerResultFixture()
	e.SessionID = nil
	want.SessionID = nil
	if _, err := ConsumePlannerEvidence(e, want); err != nil {
		t.Fatal(err)
	}
}

func TestPlannerEvidenceOperationalOutcomes(t *testing.T) {
	cases := []struct {
		status BotResultStatus
		code   string
	}{
		{BotResultBlocked, "POLICY_BLOCKED"}, {BotResultBlocked, "ACTION_NOT_ALLOWED"}, {BotResultBlocked, "PROJECT_NOT_FOUND"},
		{BotResultApprovalRequired, "APPROVAL_REQUIRED"}, {BotResultFailed, "EXECUTION_FAILED"}, {BotResultFailed, "CANCELED"},
		{BotResultFailed, "DEADLINE_EXCEEDED"}, {BotResultFailed, "AGENT_UNAVAILABLE"}, {BotResultFailed, "INVALID_RESULT"},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			e, want := plannerResultFixture()
			payload := BotResult{ActionType: want.EvidenceKind, Status: tc.status, Error: &BotError{Code: tc.code}}
			e.Payload = payload
			got, err := ConsumePlannerEvidence(e, want)
			if err != nil || !reflect.DeepEqual(got.BotResult, payload) {
				t.Fatalf("outcome lost: %+v, %v", got, err)
			}
		})
	}
}

func TestPlannerEvidenceRejectsUntrustedMessages(t *testing.T) {
	cases := []struct {
		name   string
		change func(*domain.Envelope, *PlannerEvidenceExpectation)
	}{
		{"message type", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.MessageType = domain.MessageTypeBotCommand }},
		{"project", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.ProjectID = "other" }},
		{"task", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { id := domain.TaskID("other"); e.TaskID = &id }},
		{"missing task", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.TaskID = nil }},
		{"correlation", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.CorrelationID = "other" }},
		{"session", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) {
			id := domain.SessionID("other")
			e.SessionID = &id
		}},
		{"missing session", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.SessionID = nil }},
		{"unexpected session", func(_ *domain.Envelope, w *PlannerEvidenceExpectation) { w.SessionID = nil }},
		{"payload", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.Payload = BotCommand{} }},
		{"nil payload", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.Payload = nil }},
		{"version empty", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.ProtocolVersion = " " }},
		{"version mismatch", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.ProtocolVersion = "v2" }},
		{"envelope", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.MessageID = "" }},
		{"timestamp", func(e *domain.Envelope, _ *PlannerEvidenceExpectation) { e.CreatedAt = time.Time{} }},
		{"expected project", func(_ *domain.Envelope, w *PlannerEvidenceExpectation) { w.ProjectID = " " }},
		{"expected task", func(_ *domain.Envelope, w *PlannerEvidenceExpectation) { w.TaskID = " " }},
		{"expected correlation", func(_ *domain.Envelope, w *PlannerEvidenceExpectation) { w.CorrelationID = " " }},
		{"expected kind", func(_ *domain.Envelope, w *PlannerEvidenceExpectation) { w.EvidenceKind = "SHELL" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, w := plannerResultFixture()
			tc.change(&e, &w)
			assertPlannerEvidenceRejected(t, e, w)
		})
	}
	payloadCases := []BotResult{
		{}, {ActionType: domain.ActionTypeReadFile, Status: BotResultSuccess, Result: &ports.ActionResult{Type: domain.ActionTypeReadFile, ReadFileResult: &ports.ReadFileResult{}}},
		{ActionType: domain.ActionTypeSearch, Status: "UNKNOWN"},
		{ActionType: domain.ActionTypeSearch, Status: BotResultSuccess},
		{ActionType: domain.ActionTypeSearch, Status: BotResultSuccess, Result: &ports.ActionResult{}},
		{ActionType: domain.ActionTypeSearch, Status: BotResultSuccess, Result: &ports.ActionResult{Type: domain.ActionTypeSearch, ReadFileResult: &ports.ReadFileResult{}}},
		{ActionType: domain.ActionTypeSearch, Status: BotResultSuccess, Result: &ports.ActionResult{Type: domain.ActionTypeReadFile, ReadFileResult: &ports.ReadFileResult{}}},
		{ActionType: domain.ActionTypeSearch, Status: BotResultSuccess, Result: &ports.ActionResult{Type: domain.ActionTypeSearch, SearchResult: &ports.SearchResult{}, ReadFileResult: &ports.ReadFileResult{}}},
	}
	for i, p := range payloadCases {
		e, w := plannerResultFixture()
		e.Payload = p
		t.Run(string(rune('A'+i)), func(t *testing.T) { assertPlannerEvidenceRejected(t, e, w) })
	}
	e, w := plannerResultFixture()
	p := e.Payload.(BotResult)
	p.Error = &BotError{Code: "EXECUTION_FAILED"}
	e.Payload = p
	assertPlannerEvidenceRejected(t, e, w)
	for _, status := range []BotResultStatus{BotResultBlocked, BotResultApprovalRequired, BotResultFailed} {
		for _, code := range []string{"", "internal secret", "WRONG_CODE"} {
			e, w := plannerResultFixture()
			e.Payload = BotResult{ActionType: w.EvidenceKind, Status: status, Error: &BotError{Code: code}}
			assertPlannerEvidenceRejected(t, e, w)
		}
		e, w := plannerResultFixture()
		p = e.Payload.(BotResult)
		p.Status = status
		p.Error = &BotError{Code: "EXECUTION_FAILED"}
		e.Payload = p
		assertPlannerEvidenceRejected(t, e, w)
		e.Payload = BotResult{ActionType: w.EvidenceKind, Status: status}
		assertPlannerEvidenceRejected(t, e, w)
	}
	e, w = plannerResultFixture()
	e.Payload = BotResult{ActionType: w.EvidenceKind, Status: BotResultBlocked, Error: &BotError{Code: "CANCELED"}}
	assertPlannerEvidenceRejected(t, e, w)
}
func assertPlannerEvidenceRejected(t *testing.T, e domain.Envelope, w PlannerEvidenceExpectation) {
	t.Helper()
	got, err := ConsumePlannerEvidence(e, w)
	if err == nil || !reflect.DeepEqual(got, PlannerEvidence{}) {
		t.Fatalf("expected closed failure: %+v, %v", got, err)
	}
}
