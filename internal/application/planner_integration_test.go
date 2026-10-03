package application_test

import (
	"context"
	"testing"
	"time"

	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func plannerIntegrationState(t *testing.T) (context.Context, *memory.ProjectRepository, *memory.TaskRepository, *application.ContextBuilder) {
	t.Helper()
	ctx := context.Background()
	projects, tasks := memory.NewProjectRepository(), memory.NewTaskRepository()
	if err := projects.Save(ctx, domain.Project{ID: "project", Name: "Canonical project", Workspace: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Save(ctx, domain.Task{ID: "task", ProjectID: "project", Title: "Canonical task", Status: domain.TaskStatusAnalyzing}); err != nil {
		t.Fatal(err)
	}
	return ctx, projects, tasks, application.NewContextBuilder(projects, tasks)
}

func TestPlannerIntegrationEvidence(t *testing.T) {
	ctx, _, tasks, builder := plannerIntegrationState(t)
	canonical, err := builder.Build(ctx, "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	decision := ports.PlannerDecision{ProjectID: canonical.Project.ID, TaskID: canonical.CurrentTask.ID, Type: ports.PlannerDecisionRequestEvidence, Reason: "Locate relevant contract", EvidenceKind: domain.ActionTypeSearch}
	sessionID := domain.SessionID("session")
	command, err := (application.BotCommandBuilder{}).Build(application.EvidenceRequest{Decision: decision, Params: domain.ActionParams{Search: &domain.SearchParams{Query: "contract", Path: "internal"}}, Metadata: application.BotCommandMetadata{ProtocolVersion: "v1", MessageID: "command", CorrelationID: "evidence", SessionID: &sessionID, CreatedAt: time.Now()}})
	if err != nil || command.Validate() != nil || command.MessageType != domain.MessageTypeBotCommand {
		t.Fatalf("command: %+v %v", command, err)
	}
	payload, ok := command.Payload.(application.BotCommand)
	if !ok || payload.Action.Validate() != nil || payload.Action.Type != decision.EvidenceKind {
		t.Fatal("invalid typed BOT_COMMAND")
	}
	// Typed evidence is supplied directly: no LocalAgent invocation.
	result := domain.Envelope{ProtocolVersion: command.ProtocolVersion, MessageType: domain.MessageTypeBotResult, MessageID: "result", CorrelationID: command.CorrelationID, ProjectID: command.ProjectID, TaskID: command.TaskID, SessionID: command.SessionID, CreatedAt: time.Now(), Payload: application.BotResult{ActionType: domain.ActionTypeSearch, Status: application.BotResultSuccess, Result: &ports.ActionResult{Type: domain.ActionTypeSearch, SearchResult: &ports.SearchResult{Matches: []ports.SearchMatch{{Path: "internal/contract.go", Line: 1, Text: "contract"}}}}}}
	evidence, err := application.ConsumePlannerEvidence(result, application.PlannerEvidenceExpectation{ProjectID: decision.ProjectID, TaskID: decision.TaskID, CorrelationID: command.CorrelationID, SessionID: command.SessionID, ProtocolVersion: command.ProtocolVersion, EvidenceKind: decision.EvidenceKind})
	if err != nil || evidence.ProjectID != canonical.Project.ID || evidence.TaskID != canonical.CurrentTask.ID || evidence.BotResult.Result == nil || len(evidence.BotResult.Result.SearchResult.Matches) != 1 {
		t.Fatalf("evidence: %+v %v", evidence, err)
	}
	refined, err := application.NewTaskRefiner(tasks).Refine(ctx, decision)
	if err != nil || refined != canonical.CurrentTask {
		t.Fatalf("evidence must remain analysis: %+v %v", refined, err)
	}
	rebuilt, err := builder.Build(ctx, "project", "task")
	if err != nil || rebuilt != canonical {
		t.Fatal("evidence changed canonical state")
	}
}

func TestPlannerIntegrationExecutor(t *testing.T) {
	ctx, _, tasks, builder := plannerIntegrationState(t)
	canonical, err := builder.Build(ctx, "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	decision := ports.PlannerDecision{ProjectID: canonical.Project.ID, TaskID: canonical.CurrentTask.ID, Type: ports.PlannerDecisionPrepareExecutor, Reason: "Contract is ready for implementation"}
	refined, err := application.NewTaskRefiner(tasks).Refine(ctx, decision)
	if err != nil || refined.Status != domain.TaskStatusReadyForCodex {
		t.Fatalf("refine: %+v %v", refined, err)
	}
	rebuilt, err := builder.Build(ctx, "project", "task")
	if err != nil || rebuilt.CurrentTask != refined {
		t.Fatal("ready task not persisted")
	}
	envelope, err := (application.CodexTaskBuilder{}).Build(application.CodexTaskRequest{Decision: decision, Spec: ports.ExecutorTaskSpec{Objective: "Implement contract validation", Scope: []string{"internal/contract.go"}, Constraints: []string{"Preserve existing contracts"}, AcceptanceCriteria: []string{"Invalid inputs are rejected"}}, Metadata: application.CodexTaskMetadata{ProtocolVersion: "v1", MessageID: "codex-task", CorrelationID: "executor", CreatedAt: time.Now()}})
	if err != nil || envelope.Validate() != nil || envelope.MessageType != domain.MessageTypeCodexTask || envelope.ProjectID != refined.ProjectID || envelope.TaskID == nil || *envelope.TaskID != refined.ID {
		t.Fatalf("CODEX_TASK: %+v %v", envelope, err)
	}
	payload, ok := envelope.Payload.(application.CodexTask)
	if !ok || payload.Validate() != nil {
		t.Fatal("invalid typed CODEX_TASK")
	}
	// Contract preparation ends here; no executor is called.
	after, err := builder.Build(ctx, "project", "task")
	if err != nil || after != rebuilt {
		t.Fatal("builder changed canonical state")
	}
}

func TestPlannerIntegrationBlock(t *testing.T) {
	ctx, _, tasks, builder := plannerIntegrationState(t)
	canonical, err := builder.Build(ctx, "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	decision := ports.PlannerDecision{ProjectID: canonical.Project.ID, TaskID: canonical.CurrentTask.ID, Type: ports.PlannerDecisionBlock, Reason: "Missing authorization"}
	blocked, err := application.NewTaskRefiner(tasks).Refine(ctx, decision)
	if err != nil || blocked.Status != domain.TaskStatusBlocked {
		t.Fatalf("block: %+v %v", blocked, err)
	}
	rebuilt, err := builder.Build(ctx, "project", "task")
	if err != nil || rebuilt.CurrentTask != blocked || rebuilt.Project != canonical.Project {
		t.Fatal("blocked task not canonical")
	}
}

func TestPlannerIntegrationSessionHandoff(t *testing.T) {
	ctx, projects, tasks, builder := plannerIntegrationState(t)
	canonical, err := builder.Build(ctx, "project", "task")
	if err != nil {
		t.Fatal(err)
	}
	taskID, status := canonical.CurrentTask.ID, canonical.CurrentTask.Status
	old, err := application.NewPlannerSession("old", canonical.Project.ID, &taskID, application.ContextBudget{Limit: 10}, domain.SessionStartReasonInitial)
	if err != nil {
		t.Fatal(err)
	}
	active := old
	old, err = old.Consume(10)
	if err != nil || old.State() != application.PlannerSessionHandoffRequired {
		t.Fatalf("budget: %+v %v", old, err)
	}
	envelope, err := (application.PlannerHandoffBuilder{}).Build(old, domain.PlannerHandoffPayload{ProjectID: canonical.Project.ID, CurrentTask: &taskID, TaskStatus: &status, Decisions: []string{"Continue contract review"}, Evidence: []string{"Contract located"}, NextAction: "Rebuild canonical context and review authorization"}, application.PlannerHandoffMetadata{ProtocolVersion: "v1", MessageID: "handoff", CorrelationID: "session-flow", CreatedAt: time.Now()})
	if err != nil || envelope.Validate() != nil || envelope.MessageType != domain.MessageTypePlannerHandoff {
		t.Fatalf("handoff: %+v %v", envelope, err)
	}
	unchanged, err := builder.Build(ctx, "project", "task")
	if err != nil || unchanged != canonical || active.State() != application.PlannerSessionActive || old.State() != application.PlannerSessionHandoffRequired {
		t.Fatal("session lifecycle changed canonical state")
	}
	old, err = old.Close(domain.SessionCloseReasonContextBudget)
	if err != nil || old.State() != application.PlannerSessionClosed || old.CloseReason() != domain.SessionCloseReasonContextBudget {
		t.Fatalf("close: %+v %v", old, err)
	}
	// The repositories may advance independently after the handoff was prepared.
	blocked, err := application.NewTaskRefiner(tasks).Refine(ctx, ports.PlannerDecision{ProjectID: "project", TaskID: "task", Type: ports.PlannerDecisionBlock, Reason: "Authorization unavailable"})
	if err != nil {
		t.Fatal(err)
	}
	project := canonical.Project
	project.Name = "Updated canonical project"
	if err := projects.Save(ctx, project); err != nil {
		t.Fatal(err)
	}
	next, err := application.RestorePlannerSession(old, envelope, "new", application.ContextBudget{Limit: 20})
	if err != nil || next.SessionID() == old.SessionID() || next.ProjectID() != old.ProjectID() || next.State() != application.PlannerSessionActive || next.StartReason() != domain.SessionStartReasonHandoff || next.Budget() != (application.ContextBudget{Limit: 20}) {
		t.Fatalf("restore: %+v %v", next, err)
	}
	restoredTask, present := next.TaskID()
	if !present || restoredTask != taskID {
		t.Fatal("task identity lost")
	}
	rebuilt, err := builder.Build(ctx, next.ProjectID(), restoredTask)
	if err != nil || rebuilt.Project != project || rebuilt.CurrentTask != blocked {
		t.Fatalf("repositories must win: %+v %v", rebuilt, err)
	}
	payload := envelope.Payload.(domain.PlannerHandoffPayload)
	if *payload.TaskStatus != domain.TaskStatusAnalyzing || rebuilt.CurrentTask.Status != domain.TaskStatusBlocked || old.Budget().Used != 10 {
		t.Fatal("handoff/session replaced canonical state")
	}
}
