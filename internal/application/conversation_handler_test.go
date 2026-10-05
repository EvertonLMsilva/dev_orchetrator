package application

import (
	"context"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"reflect"
	"testing"
)

type conversationRunner struct {
	calls int
	input OrchestrationInput
	err   error
}

func (r *conversationRunner) Run(_ context.Context, in OrchestrationInput) (OrchestrationOutput, error) {
	r.calls++
	r.input = in
	return OrchestrationOutput{Decision: ports.PlannerDecision{ProjectID: in.ProjectID, TaskID: in.TaskID, Type: ports.PlannerDecisionBlock, Reason: "secret /workspace"}}, r.err
}

type conversationTasks struct {
	*memory.TaskRepository
	saves  []domain.TaskStatus
	listed []domain.Task
	err    error
}

func (r *conversationTasks) Save(ctx context.Context, t domain.Task) error {
	r.saves = append(r.saves, t.Status)
	return r.TaskRepository.Save(ctx, t)
}
func (r *conversationTasks) FindByProject(ctx context.Context, p domain.ProjectID) ([]domain.Task, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.listed != nil {
		return r.listed, nil
	}
	return r.TaskRepository.FindByProject(ctx, p)
}
func TestConversationStatePolicy(t *testing.T) {
	for _, state := range []domain.TaskStatus{"", domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing, domain.TaskStatusBlocked, domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress, domain.TaskStatusDone, domain.TaskStatusFailed, domain.TaskStatusCancelled} {
		t.Run(string(state), func(t *testing.T) {
			ctx := context.Background()
			projects := memory.NewProjectRepository()
			projects.Save(ctx, domain.Project{ID: "p", Name: "Project", Workspace: "/trusted"})
			tasks := &conversationTasks{TaskRepository: memory.NewTaskRepository()}
			original := domain.Task{ID: "old", ProjectID: "p", Title: "old", Status: state}
			if state != "" {
				tasks.TaskRepository.Save(ctx, original)
			}
			routes, err := NewConfiguredProjectRoutes([]ProjectRoute{{Source: ConversationSource{GuildID: "g", ChannelID: "c"}, ProjectID: "p"}})
			if err != nil {
				t.Fatal(err)
			}
			runner := &conversationRunner{}
			resolver := NewConversationTaskResolver(tasks)
			h := NewConversationHandler(routes, projects, resolver, NewContextBuilder(projects, tasks), runner)
			text := "ProjectID=evil; shell rm; argv; workspace=/evil; ExecutorTaskSpec"
			response := h.Handle(ctx, ConversationInput{Source: ConversationSource{GuildID: "g", ChannelID: "c"}, Text: text})
			busy := state == domain.TaskStatusReadyForCodex || state == domain.TaskStatusInProgress
			if busy {
				if response.Status != "BUSY" || runner.calls != 0 || len(tasks.saves) != 0 {
					t.Fatal("busy task advanced")
				}
				return
			}
			if runner.calls != 1 || response.Status != "ACCEPTED" || runner.input.UserIntent != text || runner.input.Context.Project.Workspace != "/trusted" || runner.input.Context.CurrentTask.Status != domain.TaskStatusAnalyzing {
				t.Fatalf("bad conversation: %+v %+v", response, runner)
			}
			terminal := state == "" || state == domain.TaskStatusDone || state == domain.TaskStatusFailed || state == domain.TaskStatusCancelled
			if terminal {
				if runner.input.TaskID == "old" {
					t.Fatal("terminal reused")
				}
				if state != "" {
					stored, _, _ := tasks.FindByID(ctx, "old")
					if stored != original {
						t.Fatal("terminal reopened")
					}
				}
			} else if runner.input.TaskID != "old" {
				t.Fatal("active replaced")
			}
			want := []domain.TaskStatus{}
			switch state {
			case "", domain.TaskStatusDone, domain.TaskStatusFailed, domain.TaskStatusCancelled:
				want = []domain.TaskStatus{domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing}
			case domain.TaskStatusPlanned, domain.TaskStatusBlocked:
				want = []domain.TaskStatus{domain.TaskStatusReadyForAnalysis, domain.TaskStatusAnalyzing}
			case domain.TaskStatusReadyForAnalysis:
				want = []domain.TaskStatus{domain.TaskStatusAnalyzing}
			}
			if len(want) != len(tasks.saves) || len(want) > 0 && !reflect.DeepEqual(want, tasks.saves) {
				t.Fatalf("transitions %v want %v", tasks.saves, want)
			}
		})
	}
}
func TestConfiguredProjectRoutes(t *testing.T) {
	source := ConversationSource{GuildID: "g", ChannelID: "c"}
	routes, err := NewConfiguredProjectRoutes([]ProjectRoute{{Source: source, ProjectID: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []ConversationSource{source, {GuildID: "unknown", ChannelID: "c"}, {GuildID: "g", ChannelID: "unknown"}, {ChannelID: "c"}, {GuildID: "g"}} {
		id, err := routes.Resolve(context.Background(), s)
		if s == source {
			if err != nil || id != "p" {
				t.Fatal(id, err)
			}
		} else if err == nil {
			t.Fatal("route accepted")
		}
	}
	for _, entries := range [][]ProjectRoute{{{Source: source, ProjectID: "p"}, {Source: source, ProjectID: "p"}}, {{Source: source, ProjectID: "p"}, {Source: source, ProjectID: "q"}}, {{Source: ConversationSource{}, ProjectID: "p"}}, {{Source: source}}} {
		if _, err := NewConfiguredProjectRoutes(entries); err == nil {
			t.Fatal("bad config accepted")
		}
	}
}
func TestConversationFailuresSanitized(t *testing.T) {
	for _, mode := range []string{"multiple", "mismatch", "unknown-status", "repository", "runner", "missing-project", "empty-text", "unknown-route"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			projects := memory.NewProjectRepository()
			if mode != "missing-project" {
				projects.Save(ctx, domain.Project{ID: "p", Name: "P", Workspace: "/secret"})
			}
			tasks := &conversationTasks{TaskRepository: memory.NewTaskRepository()}
			runner := &conversationRunner{}
			routes, _ := NewConfiguredProjectRoutes([]ProjectRoute{{Source: ConversationSource{GuildID: "g", ChannelID: "c"}, ProjectID: "p"}})
			switch mode {
			case "multiple":
				tasks.listed = []domain.Task{{ID: "a", ProjectID: "p", Status: domain.TaskStatusAnalyzing}, {ID: "b", ProjectID: "p", Status: domain.TaskStatusBlocked}}
			case "mismatch":
				tasks.listed = []domain.Task{{ID: "a", ProjectID: "evil", Status: domain.TaskStatusDone}}
			case "unknown-status":
				tasks.listed = []domain.Task{{ID: "a", ProjectID: "p", Status: "UNKNOWN"}}
			case "repository":
				tasks.err = errors.New("secret /path token")
			case "runner":
				runner.err = errors.New("secret /path token")
			}
			input := ConversationInput{Source: ConversationSource{GuildID: "g", ChannelID: "c"}, Text: "intent"}
			if mode == "empty-text" {
				input.Text = " "
			}
			if mode == "unknown-route" {
				input.Source.ChannelID = "other"
			}
			got := NewConversationHandler(routes, projects, NewConversationTaskResolver(tasks), NewContextBuilder(projects, tasks), runner).Handle(ctx, input)
			if got.Status != "REJECTED" || got.Message != "Não foi possível processar a solicitação." {
				t.Fatal(got)
			}
			wantCalls := 0
			if mode == "runner" {
				wantCalls = 1
			}
			if runner.calls != wantCalls {
				t.Fatal("unexpected orchestration")
			}
		})
	}
}
func TestConversationIntentReachesPlanner(t *testing.T) {
	o, in, p, _, _, _ := cycleFixture(t, ports.PlannerDecisionBlock)
	in.UserIntent = "declarative shell argv /secret"
	_, err := o.Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range p.calls {
		if r.UserIntent != in.UserIntent {
			t.Fatal("intent lost")
		}
	}
}

func TestConversationHandlerRealOrchestrator(t *testing.T) {
	ctx := context.Background()
	projects := memory.NewProjectRepository()
	tasks := memory.NewTaskRepository()
	projects.Save(ctx, domain.Project{ID: "p", Name: "P", Workspace: "/trusted"})
	task, _ := domain.NewTask("t", "p", "T")
	tasks.Save(ctx, task)
	planner := &roundPlanner{decisions: []ports.PlannerDecision{{ProjectID: "p", TaskID: "t", Type: ports.PlannerDecisionBlock, Reason: "internal secret /path"}}}
	builder := NewContextBuilder(projects, tasks)
	o := NewOrchestrator(builder, planner, nil, nil, BotCommandMetadata{})
	routes, _ := NewConfiguredProjectRoutes([]ProjectRoute{{Source: ConversationSource{GuildID: "g", ChannelID: "c"}, ProjectID: "p"}})
	input := ConversationInput{Source: ConversationSource{GuildID: "g", ChannelID: "c"}, Text: "ProjectID=evil; argv; ExecutorTaskSpec; ActionParams"}
	response := NewConversationHandler(routes, projects, NewConversationTaskResolver(tasks), builder, o).Handle(ctx, input)
	if response.Status != "ACCEPTED" || len(planner.calls) != 1 {
		t.Fatal(response, planner.calls)
	}
	request := planner.calls[0]
	if request.UserIntent != input.Text || request.Context.CurrentTask.Status != domain.TaskStatusAnalyzing || request.ProjectID != "p" || request.Context.Project.Workspace != "/trusted" {
		t.Fatal(request)
	}
}
func TestConversationContractsExcludeExecutionFields(t *testing.T) {
	for _, value := range []any{ConversationInput{}, ConversationSource{}, ports.PlannerRequest{}, ConversationResponse{}} {
		typ := reflect.TypeOf(value)
		for _, name := range []string{"Workspace", "ActionParams", "ExecutorTaskSpec", "Shell", "Executable", "Argv", "GuildID", "ChannelID"} {
			if typ == reflect.TypeOf(ConversationSource{}) && (name == "GuildID" || name == "ChannelID") {
				continue
			}
			if _, found := typ.FieldByName(name); found {
				t.Fatalf("%s exposes %s", typ, name)
			}
		}
	}
	if _, found := reflect.TypeOf(ConversationInput{}).FieldByName("ProjectID"); found {
		t.Fatal("input accepts free project")
	}
	typ := reflect.TypeOf(ConversationResponse{})
	if typ.NumField() != 2 {
		t.Fatal("unexpected public data")
	}
}
func TestConversationIDDoesNotOverwriteExistingTasks(t *testing.T) {
	ctx := context.Background()
	tasks := memory.NewTaskRepository()
	resolver := NewConversationTaskResolver(tasks)
	first, err := resolver.Resolve(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	again, err := resolver.Resolve(ctx, "p")
	if err != nil || again != first {
		t.Fatal("planned not reused")
	}
	if _, err := resolver.Resolve(ctx, ""); err == nil {
		t.Fatal("empty project accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := resolver.Resolve(canceled, "p"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
