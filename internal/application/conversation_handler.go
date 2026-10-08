package application

import (
	"context"
	"dev-orchestrator/internal/ports"
	"strings"
)

// Text is opaque declarative intent, never a command or execution specification.
type ConversationInput struct {
	Source ConversationSource
	Text   string
	// RequestedWriteTargets constrains Development writes; it grants no authority.
	RequestedWriteTargets []string
	// Evidence is supplied by the verified adapter event, never parsed from Text.
	Actor             ports.ActorEvidence
	DevelopmentAction string
	Confirmation      string
}
type ConversationResponse struct {
	Status  string
	Message string
}
type ConversationOrchestrator interface {
	Run(context.Context, OrchestrationInput) (OrchestrationOutput, error)
}
type ConversationHandler struct {
	routes       ProjectRouteResolver
	projects     ports.ProjectRepository
	tasks        *ConversationTaskResolver
	builder      *ContextBuilder
	orchestrator ConversationOrchestrator
}

func NewConversationHandler(routes ProjectRouteResolver, projects ports.ProjectRepository, tasks *ConversationTaskResolver, builder *ContextBuilder, orchestrator ConversationOrchestrator) *ConversationHandler {
	return &ConversationHandler{routes, projects, tasks, builder, orchestrator}
}
func (h *ConversationHandler) Handle(ctx context.Context, input ConversationInput) ConversationResponse {
	rejected := ConversationResponse{Status: "REJECTED", Message: "Não foi possível processar a solicitação."}
	if ctx.Err() != nil || h == nil || h.routes == nil || h.projects == nil || h.tasks == nil || h.builder == nil || h.orchestrator == nil || !validConversationSource(input.Source) || strings.TrimSpace(input.Text) == "" {
		return rejected
	}
	projectID, err := h.routes.Resolve(ctx, input.Source)
	if err != nil {
		return rejected
	}
	project, found, err := h.projects.FindByID(ctx, projectID)
	if err != nil || !found || project.ID != projectID || strings.TrimSpace(string(projectID)) == "" {
		return rejected
	}
	task, err := h.tasks.Resolve(ctx, projectID)
	if err != nil || task.ProjectID != projectID {
		return rejected
	}
	switch task.Status {
	case "READY_FOR_CODEX":
		return ConversationResponse{Status: "BUSY", Message: "A tarefa já está preparada para execução. Aguarde a conclusão antes de enviar uma nova solicitação."}
	case "IN_PROGRESS":
		return ConversationResponse{Status: "BUSY", Message: "A tarefa está em execução. Aguarde a conclusão antes de enviar uma nova solicitação."}
	}
	task, err = h.tasks.Prepare(ctx, task)
	if err != nil {
		return rejected
	}
	canonical, err := h.builder.Build(ctx, projectID, task.ID)
	if err != nil || canonical.Project.ID != projectID || canonical.CurrentTask.ID != task.ID || canonical.CurrentTask.ProjectID != projectID || canonical.CurrentTask.Status != "ANALYZING" {
		return rejected
	}
	// Only a fixed public acknowledgement crosses this boundary. Provider reasons,
	// envelopes, task IDs, paths and internal errors are never copied into it.
	_, err = h.orchestrator.Run(ctx, OrchestrationInput{ProjectID: projectID, TaskID: task.ID, Context: canonical, UserIntent: input.Text})
	if err != nil {
		return rejected
	}
	return ConversationResponse{Status: "ACCEPTED", Message: "Solicitação processada."}
}
