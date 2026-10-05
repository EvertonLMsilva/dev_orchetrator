package application

import (
	"reflect"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func roundFixture(t *testing.T) OrchestrationInput {
	t.Helper()
	e, expected := plannerResultFixture()
	evidence, err := ConsumePlannerEvidence(e, expected)
	if err != nil {
		t.Fatal(err)
	}
	return OrchestrationInput{
		PlannerRequest: ports.PlannerRequest{ProjectID: "project", TaskID: "task"},
		Context:        PlannerContext{Project: domain.Project{ID: "project"}, CurrentTask: domain.Task{ID: "task", ProjectID: "project", Status: domain.TaskStatusAnalyzing}},
		Evidence:       []PlannerEvidence{evidence},
	}
}

func TestOrchestrationInputRejectsInvalidContracts(t *testing.T) {
	for name, change := range map[string]func(*OrchestrationInput){
		"project identity":     func(r *OrchestrationInput) { r.ProjectID = " " },
		"task identity":        func(r *OrchestrationInput) { r.TaskID = "" },
		"context project":      func(r *OrchestrationInput) { r.Context.Project.ID = "other" },
		"context task":         func(r *OrchestrationInput) { r.Context.CurrentTask.ID = "other" },
		"task project":         func(r *OrchestrationInput) { r.Context.CurrentTask.ProjectID = "other" },
		"evidence project":     func(r *OrchestrationInput) { r.Evidence[0].ProjectID = "other" },
		"evidence task":        func(r *OrchestrationInput) { r.Evidence[0].TaskID = "other" },
		"evidence correlation": func(r *OrchestrationInput) { r.Evidence[0].CorrelationID = " " },
		"evidence payload":     func(r *OrchestrationInput) { r.Evidence[0].BotResult = BotResult{} },
		"evidence result":      func(r *OrchestrationInput) { r.Evidence[0].BotResult.Result = nil },
	} {
		t.Run(name, func(t *testing.T) {
			r := roundFixture(t)
			change(&r)
			if r.Validate() == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestOrchestrationOutputRejectsInvalidDecision(t *testing.T) {
	for name, change := range map[string]func(*ports.PlannerDecision){
		"type":       func(d *ports.PlannerDecision) { d.Type = "EXECUTE" },
		"reason":     func(d *ports.PlannerDecision) { d.Reason = " " },
		"project":    func(d *ports.PlannerDecision) { d.ProjectID = "other" },
		"task":       func(d *ports.PlannerDecision) { d.TaskID = "other" },
		"capability": func(d *ports.PlannerDecision) { d.EvidenceKind = domain.ActionTypeSearch },
	} {
		t.Run(name, func(t *testing.T) {
			r := roundFixture(t)
			d := ports.PlannerDecision{ProjectID: r.ProjectID, TaskID: r.TaskID, Type: ports.PlannerDecisionBlock, Reason: "missing prerequisite"}
			change(&d)
			if (OrchestrationOutput{Decision: d}).Validate(r) == nil {
				t.Fatal("invalid decision accepted")
			}
		})
	}
}

func TestOrchestrationContractsPreserveValues(t *testing.T) {
	for _, kind := range []ports.PlannerDecisionType{ports.PlannerDecisionRequestEvidence, ports.PlannerDecisionPrepareExecutor, ports.PlannerDecisionBlock} {
		r := roundFixture(t)
		before := roundFixture(t)
		d := ports.PlannerDecision{ProjectID: r.ProjectID, TaskID: r.TaskID, Type: kind, Reason: "analysis"}
		if kind == ports.PlannerDecisionRequestEvidence {
			d.EvidenceKind = domain.ActionTypeSearch
		}
		out := OrchestrationOutput{Decision: d}
		if err := out.Validate(r); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(r, before) || out.Decision != d {
			t.Fatal("contract validation changed values")
		}
		r.Evidence = nil
		if err := r.Validate(); err != nil {
			t.Fatal("initial round requires evidence", err)
		}
		r.ProjectID = ""
		if out.Validate(r) == nil {
			t.Fatal("output accepted invalid input")
		}
	}
}
