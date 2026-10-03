package ports

import (
	"errors"
	"testing"

	"dev-orchestrator/internal/domain"
)

func TestPlannerRequestValidate(t *testing.T) {
	valid := PlannerRequest{ProjectID: "project", TaskID: "task"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, request := range []PlannerRequest{{}, {ProjectID: "project"}, {TaskID: "task"}} {
		if !errors.Is(request.Validate(), ErrInvalidPlannerRequest) {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
}

func TestPlannerDecisionValidate(t *testing.T) {
	for _, kind := range []PlannerDecisionType{PlannerDecisionRequestEvidence, PlannerDecisionPrepareExecutor, PlannerDecisionBlock} {
		t.Run(string(kind), func(t *testing.T) {
			valid := PlannerDecision{ProjectID: "project", TaskID: "task", Type: kind, Reason: "Required condition is missing"}
			if kind == PlannerDecisionRequestEvidence {
				valid.EvidenceKind = domain.ActionTypeSearch
			}
			if err := valid.Validate(); err != nil {
				t.Fatal(err)
			}
			invalid := []PlannerDecision{}
			for _, unknown := range []PlannerDecisionType{"", "UNKNOWN", "REQUEST_EVIDENCE_V2"} {
				decision := valid
				decision.Type = unknown
				invalid = append(invalid, decision)
			}
			missingProject, missingTask := valid, valid
			missingProject.ProjectID = ""
			missingTask.TaskID = ""
			for _, reason := range []string{"", " \t\n\u2003"} {
				decision := valid
				decision.Reason = reason
				invalid = append(invalid, decision)
			}
			for _, evidence := range []domain.ActionType{"", "UNKNOWN", "SHELL", " SEARCH ", "SEARCH_V2", " \t"} {
				decision := valid
				decision.EvidenceKind = evidence
				if kind == PlannerDecisionRequestEvidence || evidence != "" {
					invalid = append(invalid, decision)
				}
			}
			for _, evidence := range []domain.ActionType{domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitStatus, domain.ActionTypeGitDiff, domain.ActionTypeRunTests} {
				decision := valid
				decision.EvidenceKind = evidence
				if kind == PlannerDecisionRequestEvidence {
					if err := decision.Validate(); err != nil {
						t.Fatalf("valid evidence %s rejected: %v", evidence, err)
					}
				} else {
					invalid = append(invalid, decision)
				}
			}
			blankProject, blankTask := valid, valid
			blankProject.ProjectID = " \t\n"
			blankTask.TaskID = " \t\n"
			invalid = append(invalid, missingProject, missingTask, blankProject, blankTask, PlannerDecision{})
			for _, decision := range invalid {
				if !errors.Is(decision.Validate(), ErrInvalidPlannerDecision) {
					t.Fatalf("invalid decision accepted: %+v", decision)
				}
			}
		})
	}
}
