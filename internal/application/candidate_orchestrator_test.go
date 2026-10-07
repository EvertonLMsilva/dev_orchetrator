package application

import (
	"context"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"testing"
)

type candidatePlanningCheck struct{ t *testing.T }

func (p candidatePlanningCheck) Plan(_ context.Context, q ports.PlannerRequest) (ports.PlannerDecision, error) {
	if q.Context.Project.Workspace != "managed:trusted" || q.Context.CurrentTask.Title != "trusted task" {
		p.t.Fatal("caller context replaced canonical state")
	}
	return ports.PlannerDecision{ProjectID: q.ProjectID, TaskID: q.TaskID, Type: ports.PlannerDecisionPrepareExecutor, Reason: "prepare controlled candidate"}, nil
}
func TestCandidatePlanningReusesCanonicalOrchestratorWithoutLegacyEffects(t *testing.T) {
	ctx := context.Background()
	projects := memory.NewProjectRepository()
	tasks := memory.NewTaskRepository()
	projects.Save(ctx, domain.Project{ID: "pilot", Name: "pilot", Workspace: "managed:trusted"})
	task := domain.Task{ID: "task", ProjectID: "pilot", Title: "trusted task", Status: domain.TaskStatusAnalyzing}
	tasks.Save(ctx, task)
	builder := NewContextBuilder(projects, tasks)
	planner := candidatePlanningCheck{t}
	input := OrchestrationInput{ProjectID: "pilot", TaskID: "task", Context: PlannerContext{Project: domain.Project{ID: "pilot", Workspace: "/untrusted/host"}, CurrentTask: domain.Task{ID: "task", ProjectID: "pilot", Title: "ALLOW"}}, UserIntent: "create permitted file"}
	out, err := NewCandidatePlanningOrchestrator(builder, planner).Run(ctx, input)
	if err != nil || out.Decision.Type != ports.PlannerDecisionPrepareExecutor || out.CodexTask != nil || out.CodexResult != nil {
		t.Fatal(out, err)
	}
	got, _, err := tasks.FindByID(ctx, task.ID)
	if err != nil || got != task {
		t.Fatal("planning changed task state", err)
	}
	if _, err := NewOrchestrator(builder, planner, nil, nil, BotCommandMetadata{}).Run(ctx, input); err == nil {
		t.Fatal("legacy executor gate weakened")
	}
}
