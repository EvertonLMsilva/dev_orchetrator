package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"

	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

type queryService interface {
	Project(context.Context, domain.ProjectID) (domain.Project, error)
	Task(context.Context, domain.TaskID) (domain.Task, error)
}

type continueService interface {
	ContinueTask(context.Context, domain.TaskID) (domain.Task, error)
}

type approvalService interface {
	Approve(context.Context, domain.ApprovalID) (domain.Approval, error)
}

type commandClient interface {
	CreateGlobalCommand(snowflake.ID, sdk.ApplicationCommandCreate, ...rest.RequestOpt) (sdk.ApplicationCommand, error)
	CreateInteractionResponse(snowflake.ID, string, sdk.InteractionResponse, ...rest.RequestOpt) error
}

// SetQueryService configures the read-only queries before opening the gateway.
func (g *Gateway) SetQueryService(service queryService) { g.queries = service }

// SetContinueService configures task continuation before opening the gateway.
func (g *Gateway) SetContinueService(service continueService) { g.continuation = service }

// SetApprovalService configures approval decisions before opening the gateway.
func (g *Gateway) SetApprovalService(service approvalService) { g.approvals = service }

func commandDefinitions() []sdk.ApplicationCommandCreate {
	return []sdk.ApplicationCommandCreate{
		sdk.SlashCommandCreate{Name: "project", Description: "Show project information", Options: []sdk.ApplicationCommandOption{
			sdk.ApplicationCommandOptionString{Name: "id", Description: "Project ID", Required: true},
		}},
		sdk.SlashCommandCreate{Name: "status", Description: "Show task status", Options: []sdk.ApplicationCommandOption{
			sdk.ApplicationCommandOptionString{Name: "task", Description: "Task ID", Required: true},
		}},
		sdk.SlashCommandCreate{Name: "continue", Description: "Continue task workflow", Options: []sdk.ApplicationCommandOption{
			sdk.ApplicationCommandOptionString{Name: "task", Description: "Task ID", Required: true},
		}},
		sdk.SlashCommandCreate{Name: "approve", Description: "Approve pending action", Options: []sdk.ApplicationCommandOption{
			sdk.ApplicationCommandOptionString{Name: "approval", Description: "Approval ID", Required: true},
		}},
	}
}

// RegisterCommands explicitly registers the commands using a caller-supplied
// application ID. Construction and Open never register commands remotely.
func (g *Gateway) RegisterCommands(ctx context.Context, applicationID snowflake.ID) error {
	for _, command := range commandDefinitions() {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := g.commands.CreateGlobalCommand(applicationID, command, rest.WithCtx(ctx)); err != nil {
			return err
		}
	}
	return nil
}

func (g *Gateway) handleInteraction(ctx context.Context, event gateway.EventInteractionCreate) error {
	interaction, ok := event.Interaction.(sdk.ApplicationCommandInteraction)
	if !ok {
		return nil
	}
	data, ok := interaction.Data.(sdk.SlashCommandInteractionData)
	if !ok {
		return nil
	}
	var optionName string
	switch data.CommandName() {
	case "project":
		optionName = "id"
	case "status", "continue":
		optionName = "task"
	case "approve":
		optionName = "approval"
	default:
		return nil
	}

	content := "Internal error."
	option, present := data.Option(optionName)
	var id string
	if present && option.Type == sdk.ApplicationCommandOptionTypeString && json.Unmarshal(option.Value, &id) == nil {
		switch data.CommandName() {
		case "project":
			if g.queries == nil {
				break
			}
			project, err := g.queries.Project(ctx, domain.ProjectID(id))
			if err == nil {
				content = fmt.Sprintf("Project: %s\nName: %s\nWorkspace: %s", project.ID, project.Name, project.Workspace)
			} else if errors.Is(err, application.ErrProjectNotFound) {
				content = "Project not found."
			}
		case "status":
			if g.queries == nil {
				break
			}
			task, err := g.queries.Task(ctx, domain.TaskID(id))
			if err == nil {
				content = fmt.Sprintf("Task: %s\nProject: %s\nTitle: %s\nStatus: %s", task.ID, task.ProjectID, task.Title, task.Status)
			} else if errors.Is(err, application.ErrTaskNotFound) {
				content = "Task not found."
			}
		case "approve":
			if g.approvals == nil {
				break
			}
			approval, err := g.approvals.Approve(ctx, domain.ApprovalID(id))
			switch {
			case err == nil:
				content = fmt.Sprintf("Approval: %s\nStatus: %s", approval.ID, approval.Status)
			case errors.Is(err, application.ErrApprovalNotFound):
				content = "Approval not found."
			case errors.Is(err, domain.ErrApprovalNotPending):
				content = "Approval is not pending."
			}
		case "continue":
			if g.continuation == nil {
				break
			}
			task, err := g.continuation.ContinueTask(ctx, domain.TaskID(id))
			switch {
			case err == nil:
				content = fmt.Sprintf("Task: %s\nStatus: %s", task.ID, task.Status)
			case errors.Is(err, application.ErrTaskNotFound):
				content = "Task not found."
			case errors.Is(err, application.ErrTaskCannotContinue):
				content = "Task cannot continue from its current status."
			}
		}
	}
	return g.commands.CreateInteractionResponse(interaction.ID(), interaction.Token(), sdk.InteractionResponse{
		Type: sdk.InteractionResponseTypeCreateMessage,
		Data: sdk.MessageCreate{Content: content, Flags: sdk.MessageFlagEphemeral},
	}, rest.WithCtx(ctx))
}
