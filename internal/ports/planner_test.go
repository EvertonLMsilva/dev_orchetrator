package ports

import (
	"errors"
	"testing"
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
			valid := PlannerDecision{ProjectID: "project", TaskID: "task", Type: kind}
			if kind == PlannerDecisionBlock {
				valid.Reason = "Required condition is missing"
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
			missingProject, missingTask, incompatible := valid, valid, valid
			missingProject.ProjectID = ""
			missingTask.TaskID = ""
			if kind == PlannerDecisionBlock {
				incompatible.Reason = ""
			} else {
				incompatible.Reason = "Unexpected blocking reason"
			}
			invalid = append(invalid, missingProject, missingTask, incompatible, PlannerDecision{})
			for _, decision := range invalid {
				if !errors.Is(decision.Validate(), ErrInvalidPlannerDecision) {
					t.Fatalf("invalid decision accepted: %+v", decision)
				}
			}
		})
	}
}
