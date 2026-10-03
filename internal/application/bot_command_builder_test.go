package application

import (
	"reflect"
	"testing"
	"time"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func evidenceRequestForTest() EvidenceRequest {
	session := domain.SessionID("session")
	return EvidenceRequest{
		Decision: ports.PlannerDecision{ProjectID: "project", TaskID: "task", Type: ports.PlannerDecisionRequestEvidence, Reason: "Need evidence", EvidenceKind: domain.ActionTypeSearch},
		Params:   domain.ActionParams{Search: &domain.SearchParams{Query: "needle", Path: "internal"}},
		Metadata: BotCommandMetadata{ProtocolVersion: "v1", MessageID: "message", CorrelationID: "correlation", SessionID: &session, CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)},
	}
}

func TestBotCommandBuilderEvidenceKinds(t *testing.T) {
	variants := []struct {
		kind   domain.ActionType
		params domain.ActionParams
	}{
		{domain.ActionTypeSearch, domain.ActionParams{Search: &domain.SearchParams{Query: "needle", Path: "internal"}}},
		{domain.ActionTypeReadFile, domain.ActionParams{ReadFile: &domain.ReadFileParams{Path: "file.go"}}},
		{domain.ActionTypeGitStatus, domain.ActionParams{}},
		{domain.ActionTypeGitDiff, domain.ActionParams{GitDiff: &domain.GitDiffParams{Path: "file.go"}}},
		{domain.ActionTypeRunTests, domain.ActionParams{RunTests: &domain.RunTestsParams{Target: "unit-tests"}}},
	}
	for _, variant := range variants {
		t.Run(string(variant.kind), func(t *testing.T) {
			r := evidenceRequestForTest()
			r.Decision.EvidenceKind, r.Params = variant.kind, variant.params
			got, err := (BotCommandBuilder{}).Build(r)
			if err != nil {
				t.Fatal(err)
			}
			command, ok := got.Payload.(BotCommand)
			if !ok || command.Action.Validate() != nil || got.Validate() != nil {
				t.Fatal("invalid P2 command")
			}
			if got.MessageType != domain.MessageTypeBotCommand || command.Action.Type != r.Decision.EvidenceKind || !reflect.DeepEqual(command.Action.Params, r.Params) {
				t.Fatal("command contract not preserved")
			}
			if got.ProjectID != r.Decision.ProjectID || command.Action.ProjectID != got.ProjectID || got.TaskID == nil || *got.TaskID != r.Decision.TaskID || !sameTaskID(command.Action.TaskID, got.TaskID) {
				t.Fatal("identities not preserved")
			}
			if got.ProtocolVersion != r.Metadata.ProtocolVersion || got.MessageID != r.Metadata.MessageID || got.CorrelationID != r.Metadata.CorrelationID || got.CreatedAt != r.Metadata.CreatedAt || !reflect.DeepEqual(got.SessionID, r.Metadata.SessionID) {
				t.Fatal("metadata not preserved")
			}
			for _, other := range variants {
				if other.kind == variant.kind {
					continue
				}
				bad := r
				bad.Params = other.params
				assertEvidenceRejected(t, bad)
			}
		})
	}
	r := evidenceRequestForTest()
	r.Metadata.SessionID = nil
	if got, err := (BotCommandBuilder{}).Build(r); err != nil || got.SessionID != nil {
		t.Fatal("optional session rejected")
	}
}

func assertEvidenceRejected(t *testing.T, r EvidenceRequest) {
	t.Helper()
	got, err := (BotCommandBuilder{}).Build(r)
	if err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
		t.Fatalf("expected closed failure, got %+v, %v", got, err)
	}
}

func TestBotCommandBuilderRejectsInvalidEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*EvidenceRequest)
	}{
		{"missing project", func(r *EvidenceRequest) { r.Decision.ProjectID = "" }},
		{"missing task", func(r *EvidenceRequest) { r.Decision.TaskID = " " }},
		{"missing reason", func(r *EvidenceRequest) { r.Decision.Reason = "" }},
		{"prepare executor", func(r *EvidenceRequest) {
			r.Decision.Type = ports.PlannerDecisionPrepareExecutor
			r.Decision.EvidenceKind = ""
		}},
		{"block", func(r *EvidenceRequest) { r.Decision.Type = ports.PlannerDecisionBlock; r.Decision.EvidenceKind = "" }},
		{"unknown evidence", func(r *EvidenceRequest) { r.Decision.EvidenceKind = "SHELL" }},
		{"missing params", func(r *EvidenceRequest) { r.Params = domain.ActionParams{} }},
		{"mixed params", func(r *EvidenceRequest) { r.Params.ReadFile = &domain.ReadFileParams{Path: "file"} }},
		{"invalid search", func(r *EvidenceRequest) { r.Params.Search.Query = " " }},
		{"invalid read", func(r *EvidenceRequest) {
			r.Decision.EvidenceKind = domain.ActionTypeReadFile
			r.Params = domain.ActionParams{ReadFile: &domain.ReadFileParams{}}
		}},
		{"missing target", func(r *EvidenceRequest) {
			r.Decision.EvidenceKind = domain.ActionTypeRunTests
			r.Params = domain.ActionParams{RunTests: &domain.RunTestsParams{}}
		}},
		{"invalid target", func(r *EvidenceRequest) {
			r.Decision.EvidenceKind = domain.ActionTypeRunTests
			r.Params = domain.ActionParams{RunTests: &domain.RunTestsParams{Target: "go test ./..."}}
		}},
		{"missing version", func(r *EvidenceRequest) { r.Metadata.ProtocolVersion = " " }},
		{"missing message", func(r *EvidenceRequest) { r.Metadata.MessageID = " " }},
		{"missing correlation", func(r *EvidenceRequest) { r.Metadata.CorrelationID = "" }},
		{"missing timestamp", func(r *EvidenceRequest) { r.Metadata.CreatedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { r := evidenceRequestForTest(); tc.change(&r); assertEvidenceRejected(t, r) })
	}
}
