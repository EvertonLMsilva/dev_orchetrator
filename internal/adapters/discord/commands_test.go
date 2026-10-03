package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"

	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
)

var _ queryService = (*application.QueryService)(nil)

type commandQueries struct {
	project   domain.Project
	task      domain.Task
	err       error
	ctx       context.Context
	projectID domain.ProjectID
	taskID    domain.TaskID
	calls     int
}

func (q *commandQueries) Project(ctx context.Context, id domain.ProjectID) (domain.Project, error) {
	q.calls++
	q.ctx, q.projectID = ctx, id
	return q.project, q.err
}
func (q *commandQueries) Task(ctx context.Context, id domain.TaskID) (domain.Task, error) {
	q.calls++
	q.ctx, q.taskID = ctx, id
	return q.task, q.err
}

type commandREST struct {
	response      sdk.InteractionResponse
	responses     int
	id            snowflake.ID
	token         string
	commands      []sdk.ApplicationCommandCreate
	applicationID snowflake.ID
	err           error
}

func (r *commandREST) CreateInteractionResponse(id snowflake.ID, token string, response sdk.InteractionResponse, opts ...rest.RequestOpt) error {
	r.responses++
	r.id, r.token, r.response = id, token, response
	return r.err
}
func (r *commandREST) CreateGlobalCommand(id snowflake.ID, command sdk.ApplicationCommandCreate, opts ...rest.RequestOpt) (sdk.ApplicationCommand, error) {
	r.applicationID = id
	r.commands = append(r.commands, command)
	return nil, r.err
}

func commandEvent(t *testing.T, name, options string) gateway.EventInteractionCreate {
	t.Helper()
	var event gateway.EventInteractionCreate
	data := fmt.Sprintf(`{"id":"123","application_id":"456","token":"interaction-test","version":1,"type":2,"data":{"id":"789","name":%q,"type":1,"options":%s}}`, name, options)
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestCommandDefinitions(t *testing.T) {
	commands := commandDefinitions()
	if len(commands) != 4 {
		t.Fatalf("commands = %d", len(commands))
	}
	for i, want := range []struct{ name, option, description string }{{"project", "id", "Show project information"}, {"status", "task", "Show task status"}, {"continue", "task", "Continue task workflow"}, {"approve", "approval", "Approve pending action"}} {
		command, ok := commands[i].(sdk.SlashCommandCreate)
		if !ok || command.Type() != sdk.ApplicationCommandTypeSlash || command.Name != want.name || command.Description != want.description || len(command.Options) != 1 {
			t.Fatalf("invalid definition: %+v", commands[i])
		}
		option, ok := command.Options[0].(sdk.ApplicationCommandOptionString)
		if !ok || option.Name != want.option || !option.Required || option.Type() != sdk.ApplicationCommandOptionTypeString || (want.name == "continue" && option.Description != "Task ID") || (want.name == "approve" && option.Description != "Approval ID") {
			t.Fatalf("invalid option: %+v", command.Options[0])
		}
	}
}

func TestCommandInteractions(t *testing.T) {
	internalErr := errors.New("secret repository detail")
	for _, tc := range []struct {
		name, command, option, want string
		err                         error
	}{
		{name: "project", command: "project", option: "id", want: "Project: p\nName: Project name\nWorkspace: D:/workspace"},
		{name: "status", command: "status", option: "task", want: "Task: t\nProject: p\nTitle: Task title\nStatus: IN_PROGRESS"},
		{name: "missing project", command: "project", option: "id", err: application.ErrProjectNotFound, want: "Project not found."},
		{name: "missing task", command: "status", option: "task", err: application.ErrTaskNotFound, want: "Task not found."},
		{name: "wrapped missing", command: "project", option: "id", err: fmt.Errorf("wrapped: %w", application.ErrProjectNotFound), want: "Project not found."},
		{name: "project internal error", command: "project", option: "id", err: internalErr, want: "Internal error."},
		{name: "task internal error", command: "status", option: "task", err: internalErr, want: "Internal error."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &commandQueries{project: domain.Project{ID: "p", Name: "Project name", Workspace: "D:/workspace"}, task: domain.Task{ID: "t", ProjectID: "p", Title: "Task title", Status: domain.TaskStatusInProgress}, err: tc.err}
			r := &commandREST{}
			g := &Gateway{commands: r}
			g.SetQueryService(q)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			event := commandEvent(t, tc.command, fmt.Sprintf(`[{"name":%q,"type":3,"value":"requested-id"}]`, tc.option))
			if err := g.handleInteraction(ctx, event); err != nil {
				t.Fatal(err)
			}
			if q.calls != 1 || q.ctx != ctx {
				t.Fatal("query/context not forwarded")
			}
			if tc.command == "project" && (q.projectID != "requested-id" || q.taskID != "") {
				t.Fatal("wrong project dispatch")
			}
			if tc.command == "status" && (q.taskID != "requested-id" || q.projectID != "") {
				t.Fatal("wrong task dispatch")
			}
			message, ok := r.response.Data.(sdk.MessageCreate)
			if !ok || message.Content != tc.want || message.Flags != sdk.MessageFlagEphemeral || r.response.Type != sdk.InteractionResponseTypeCreateMessage || r.responses != 1 || r.id != 123 || r.token != "interaction-test" {
				t.Fatalf("wrong response: %+v", r)
			}
		})
	}
}

func TestCommandInteractionBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, command, options string
		configured             bool
		responses              int
	}{
		{"unknown", "unknown", "[]", true, 0},
		{"missing option", "project", "[]", true, 1},
		{"wrong option type", "status", `[{"name":"task","type":4,"value":1}]`, true, 1},
		{"unconfigured", "project", `[{"name":"id","type":3,"value":"p"}]`, false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &commandREST{}
			q := &commandQueries{}
			g := &Gateway{commands: r}
			if tc.configured {
				g.SetQueryService(q)
			}
			if err := g.handleInteraction(context.Background(), commandEvent(t, tc.command, tc.options)); err != nil {
				t.Fatal(err)
			}
			if q.calls != 0 || r.responses != tc.responses {
				t.Fatal("unexpected query/response")
			}
			if r.responses > 0 && r.response.Data.(sdk.MessageCreate).Content != "Internal error." {
				t.Fatal("wrong error response")
			}
		})
	}
	r := &commandREST{err: errors.New("response failed")}
	g := &Gateway{commands: r}
	if err := g.handleInteraction(context.Background(), commandEvent(t, "project", "[]")); err != r.err {
		t.Fatal("response error not preserved")
	}
}

func TestRegisterCommandsWithoutNetwork(t *testing.T) {
	r := &commandREST{}
	g := &Gateway{commands: r}
	if err := g.RegisterCommands(context.Background(), 456); err != nil {
		t.Fatal(err)
	}
	if r.applicationID != 456 || len(r.commands) != 4 || r.commands[0].CommandName() != "project" || r.commands[1].CommandName() != "status" || r.commands[2].CommandName() != "continue" || r.commands[3].CommandName() != "approve" {
		t.Fatal("wrong registration")
	}
	r.commands = nil
	r.err = errors.New("registration failed")
	if err := g.RegisterCommands(context.Background(), 456); err != r.err || len(r.commands) != 1 {
		t.Fatal("registration error not preserved")
	}
	r.commands = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.RegisterCommands(ctx, 456); err != context.Canceled || len(r.commands) != 0 {
		t.Fatal("canceled registration reached REST")
	}
}

func TestGatewayDispatchesInteractionWithoutNetwork(t *testing.T) {
	r := &commandREST{}
	q := &commandQueries{project: domain.Project{ID: "p"}}
	g := &Gateway{commands: r}
	g.SetQueryService(q)
	event := commandEvent(t, "project", `[{"name":"id","type":3,"value":"p"}]`)
	g.onEvent(nil, gateway.EventTypeInteractionCreate, 0, event)
	if q.calls != 1 || q.projectID != "p" || r.responses != 1 {
		t.Fatal("gateway callback did not dispatch command")
	}
	if _, ok := q.ctx.Deadline(); !ok {
		t.Fatal("interaction context has no deadline")
	}
	g.onEvent(nil, gateway.EventTypeReady, 0, event)
	g.onEvent(nil, gateway.EventTypeInteractionCreate, 0, nil)
	if err := g.handleInteraction(context.Background(), gateway.EventInteractionCreate{}); err != nil {
		t.Fatal(err)
	}
	if q.calls != 1 || r.responses != 1 {
		t.Fatal("unrelated event reached command handler")
	}
}
