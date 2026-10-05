package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type roundPlanner struct {
	calls     []ports.PlannerRequest
	decisions []ports.PlannerDecision
	err       error
}

func (p *roundPlanner) Plan(_ context.Context, r ports.PlannerRequest) (ports.PlannerDecision, error) {
	p.calls = append(p.calls, r)
	if p.err != nil {
		return ports.PlannerDecision{}, p.err
	}
	return p.decisions[len(p.calls)-1], nil
}

type roundTransport struct {
	calls         int
	real          *LocalAgentTransport
	change        func(*domain.Envelope)
	changeCommand func(*domain.Envelope)
	err           error
}

func (t *roundTransport) Handle(ctx context.Context, e domain.Envelope) (domain.Envelope, error) {
	t.calls++
	if t.err != nil {
		return domain.Envelope{}, t.err
	}
	if t.changeCommand != nil {
		t.changeCommand(&e)
	}
	r, err := t.real.Handle(ctx, e)
	if t.change != nil {
		t.change(&r)
	}
	return r, err
}

type roundAgent struct {
	calls int
	err   error
}

func (a *roundAgent) Execute(_ context.Context, action domain.Action) (ports.ActionResult, error) {
	a.calls++
	return ports.ActionResult{Type: action.Type, GitStatusResult: &ports.GitStatusResult{}}, a.err
}

type failingResolver struct{ err error }

func TestOrchestratorPreservesOriginalSessionExpectation(t *testing.T) {
	o, input, p, tr, _, _ := cycleFixture(t, ports.PlannerDecisionBlock)
	tr.changeCommand = func(e *domain.Envelope) { *e.SessionID = "other" }
	out, err := o.Run(context.Background(), input)
	if err == nil || out != (OrchestrationOutput{}) || len(p.calls) != 1 {
		t.Fatal("transport changed original session expectation")
	}
}

func (r failingResolver) Resolve(context.Context, domain.ActionType, PlannerContext) (domain.ActionParams, error) {
	return domain.ActionParams{}, r.err
}

func TestOrchestratorEvidenceCycle(t *testing.T) {
	for _, second := range []ports.PlannerDecisionType{ports.PlannerDecisionBlock, ports.PlannerDecisionPrepareExecutor, ports.PlannerDecisionRequestEvidence} {
		for _, agentErr := range []error{nil, ErrPolicyBlocked, ErrPolicyRequiresApproval, errors.New("execution failure")} {
			t.Run(string(second)+"/"+fmtError(agentErr), func(t *testing.T) {
				o, input, p, transport, agent, tasks := cycleFixture(t, second)
				if second == ports.PlannerDecisionPrepareExecutor {
					configured, _, _, _ := executorFixture(t)
					o.executorCycle = configured.executorCycle
					o.executorCycle.Tasks = tasks
				}
				agent.err = agentErr
				canonical, err := o.builder.Build(context.Background(), input.ProjectID, input.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				out, err := o.Run(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				if out.Decision != p.decisions[1] || len(p.calls) != 2 || transport.calls != 1 || agent.calls != 1 {
					t.Fatalf("wrong round limits/output: %+v", out)
				}
				if !reflect.DeepEqual(p.calls[0].Context, canonical) || len(p.calls[0].Evidence) != 0 || !reflect.DeepEqual(p.calls[1].Context, canonical) || len(p.calls[1].Evidence) != 1 {
					t.Fatal("canonical context or evidence missing")
				}
				evidence := p.calls[1].Evidence[0]
				if evidence.Validate() != nil || p.calls[1].Validate() != nil {
					t.Fatal("invalid evidence forwarded")
				}
				want := BotResultSuccess
				if agentErr == ErrPolicyBlocked {
					want = BotResultBlocked
				}
				if agentErr == ErrPolicyRequiresApproval {
					want = BotResultApprovalRequired
				}
				if agentErr != nil && want == BotResultSuccess {
					want = BotResultFailed
				}
				if evidence.BotResult.Status != want {
					t.Fatal("operational status changed")
				}
				stored, _, err := tasks.FindByID(context.Background(), input.TaskID)
				wantTask := canonical.CurrentTask
				if second == ports.PlannerDecisionPrepareExecutor {
					wantTask.Status = domain.TaskStatusDone
				}
				if err != nil || stored != wantTask {
					t.Fatal("task mutated")
				}
			})
		}
	}
}
func fmtError(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}

func cycleFixture(t *testing.T, second ports.PlannerDecisionType) (*Orchestrator, OrchestrationInput, *roundPlanner, *roundTransport, *roundAgent, *memory.TaskRepository) {
	t.Helper()
	ctx := context.Background()
	projects := memory.NewProjectRepository()
	tasks := memory.NewTaskRepository()
	if err := projects.Save(ctx, domain.Project{ID: "project", Name: "Project", Workspace: "/workspace"}); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Save(ctx, domain.Task{ID: "task", ProjectID: "project", Title: "Task", Status: domain.TaskStatusAnalyzing}); err != nil {
		t.Fatal(err)
	}
	input := roundFixture(t)
	input.Evidence = nil
	// Caller context is deliberately stale; the orchestrator must reread repositories.
	input.Context.CurrentTask.Title = "stale"
	initial := ports.PlannerDecision{ProjectID: "project", TaskID: "task", Type: ports.PlannerDecisionRequestEvidence, Reason: "need status", EvidenceKind: domain.ActionTypeGitStatus}
	next := initial
	next.Type = second
	if second != ports.PlannerDecisionRequestEvidence {
		next.EvidenceKind = ""
	}
	p := &roundPlanner{decisions: []ports.PlannerDecision{initial, next}}
	agent := &roundAgent{}
	transport := &roundTransport{real: NewLocalAgentTransport(agent)}
	session := domain.SessionID("session")
	metadata := BotCommandMetadata{ProtocolVersion: "v1", MessageID: "command", CorrelationID: "correlation", SessionID: &session, CreatedAt: time.Now()}
	o := NewOrchestrator(NewContextBuilder(projects, tasks), p, TrustedEvidenceResolver{}, transport, metadata)
	return o, input, p, transport, agent, tasks
}

func TestOrchestratorFailClosed(t *testing.T) {
	sentinel := errors.New("boundary failure")
	for name, change := range map[string]func(*Orchestrator, *OrchestrationInput, *roundPlanner, *roundTransport){
		"invalid input": func(_ *Orchestrator, i *OrchestrationInput, _ *roundPlanner, _ *roundTransport) { i.TaskID = "" },
		"planner project": func(_ *Orchestrator, _ *OrchestrationInput, p *roundPlanner, _ *roundTransport) {
			p.decisions[0].ProjectID = "other"
		},
		"planner task": func(_ *Orchestrator, _ *OrchestrationInput, p *roundPlanner, _ *roundTransport) {
			p.decisions[0].TaskID = "other"
		},
		"second planner identity": func(_ *Orchestrator, _ *OrchestrationInput, p *roundPlanner, _ *roundTransport) {
			p.decisions[1].TaskID = "other"
		},
		"planner boundary": func(_ *Orchestrator, _ *OrchestrationInput, p *roundPlanner, _ *roundTransport) { p.err = sentinel },
		"resolver": func(o *Orchestrator, _ *OrchestrationInput, _ *roundPlanner, _ *roundTransport) {
			o.resolver = failingResolver{sentinel}
		},
		"transport boundary": func(_ *Orchestrator, _ *OrchestrationInput, _ *roundPlanner, tr *roundTransport) { tr.err = sentinel },
		"result correlation": func(_ *Orchestrator, _ *OrchestrationInput, _ *roundPlanner, tr *roundTransport) {
			tr.change = func(e *domain.Envelope) { e.CorrelationID = "other" }
		},
		"result session": func(_ *Orchestrator, _ *OrchestrationInput, _ *roundPlanner, tr *roundTransport) {
			tr.change = func(e *domain.Envelope) { e.SessionID = nil }
		},
		"result action": func(_ *Orchestrator, _ *OrchestrationInput, _ *roundPlanner, tr *roundTransport) {
			tr.change = func(e *domain.Envelope) {
				r := e.Payload.(BotResult)
				r.ActionType = domain.ActionTypeReadFile
				e.Payload = r
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			o, input, p, tr, _, _ := cycleFixture(t, ports.PlannerDecisionBlock)
			change(o, &input, p, tr)
			out, err := o.Run(context.Background(), input)
			if err == nil || out != (OrchestrationOutput{}) {
				t.Fatalf("failure fabricated output: %+v %v", out, err)
			}
			if name == "planner boundary" || name == "resolver" || name == "transport boundary" {
				if !errors.Is(err, sentinel) {
					t.Fatal("boundary error lost")
				}
			}
			if name != "second planner identity" && len(p.calls) > 1 {
				t.Fatal("continued after failure")
			}
		})
	}
}

func TestOrchestratorReturnsInitialNonEvidenceDecision(t *testing.T) {
	for _, kind := range []ports.PlannerDecisionType{ports.PlannerDecisionBlock, ports.PlannerDecisionPrepareExecutor} {
		o, input, p, tr, agent, _ := cycleFixture(t, kind)
		p.decisions[0] = p.decisions[1]
		if kind == ports.PlannerDecisionPrepareExecutor {
			out, err := o.Run(context.Background(), input)
			if !errors.Is(err, ErrInvalidOrchestrator) || out != (OrchestrationOutput{}) || len(p.calls) != 1 || tr.calls != 0 || agent.calls != 0 {
				t.Fatal("unconfigured executor did not fail closed")
			}
			continue
		}
		out, err := o.Run(context.Background(), input)
		if err != nil || out.Decision != p.decisions[0] || len(p.calls) != 1 || tr.calls != 0 || agent.calls != 0 {
			t.Fatal("non-evidence decision executed", err)
		}
	}
}

func TestTrustedEvidenceResolver(t *testing.T) {
	canonical := roundFixture(t).Context
	resolver := TrustedEvidenceResolver{}
	for _, kind := range []domain.ActionType{domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitDiff, domain.ActionTypeRunTests, "SHELL"} {
		if _, err := resolver.Resolve(context.Background(), kind, canonical); err == nil {
			t.Fatal("guessed params", kind)
		}
	}
	resolver.Params = map[domain.ActionType]domain.ActionParams{domain.ActionTypeReadFile: {ReadFile: &domain.ReadFileParams{Path: "trusted.go"}}}
	params, err := resolver.Resolve(context.Background(), domain.ActionTypeReadFile, canonical)
	if err != nil || params.ReadFile.Path != "trusted.go" {
		t.Fatal("trusted config lost", err)
	}
}

func TestPlannerRequestEvidenceBoundary(t *testing.T) {
	r := roundFixture(t)
	request := ports.PlannerRequest{ProjectID: r.ProjectID, TaskID: r.TaskID, Context: r.Context, Evidence: r.Evidence}
	if request.Validate() != nil {
		t.Fatal("validated evidence rejected")
	}
	evidenceType := reflect.TypeOf(request.Evidence).Elem()
	if reflect.TypeOf(BotResult{}).AssignableTo(evidenceType) || reflect.TypeOf(domain.Envelope{}).AssignableTo(evidenceType) {
		t.Fatal("raw result accepted by evidence contract")
	}
	for _, field := range []string{"project", "task", "structure"} {
		copy := request
		copy.Evidence = append([]PlannerEvidence(nil), request.Evidence...)
		switch field {
		case "project":
			copy.Evidence[0].ProjectID = "other"
		case "task":
			copy.Evidence[0].TaskID = "other"
		case "structure":
			copy.Evidence[0].BotResult = BotResult{}
		}
		if copy.Validate() == nil {
			t.Fatal("bad evidence accepted", field)
		}
	}
}

type cycleExecutor struct {
	calls  int
	result ports.ExecutorResult
	err    error
	check  func(ports.ExecutorRequest)
}

func (e *cycleExecutor) Execute(_ context.Context, r ports.ExecutorRequest) (ports.ExecutorResult, error) {
	e.calls++
	if e.check != nil {
		e.check(r)
	}
	return e.result, e.err
}
func executorFixture(t *testing.T) (*Orchestrator, OrchestrationInput, *cycleExecutor, *memory.TaskRepository) {
	t.Helper()
	o, input, p, _, _, tasks := cycleFixture(t, ports.PlannerDecisionPrepareExecutor)
	p.decisions[0] = p.decisions[1]
	e := &cycleExecutor{result: ports.ExecutorResult{ProjectID: input.ProjectID, TaskID: input.TaskID, Outcome: ports.ExecutorOutcomeDone, Summary: "execution mentioned BLOCKED"}}
	r := codexTaskRequestForTest()
	o.executorCycle = &ExecutorCycleConfig{Resolver: TrustedExecutorTaskSpecResolver{Specs: map[ExecutorTaskIdentity]ports.ExecutorTaskSpec{{input.ProjectID, input.TaskID}: r.Spec}}, Executor: e, Tasks: tasks, TaskMetadata: r.Metadata, ResultMetadata: CodexResultMetadata{ProtocolVersion: r.Metadata.ProtocolVersion, MessageID: "result", CorrelationID: r.Metadata.CorrelationID, SessionID: r.Metadata.SessionID, CreatedAt: r.Metadata.CreatedAt}}
	return o, input, e, tasks
}
func TestOrchestratorExecutorCycle(t *testing.T) {
	for outcome, status := range map[ports.ExecutorOutcome]domain.TaskStatus{ports.ExecutorOutcomeDone: domain.TaskStatusDone, ports.ExecutorOutcomeBlocked: domain.TaskStatusBlocked, ports.ExecutorOutcomeFailed: domain.TaskStatusFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			o, input, e, tasks := executorFixture(t)
			e.result.Outcome = outcome
			e.check = func(r ports.ExecutorRequest) {
				stored, _, _ := tasks.FindByID(context.Background(), input.TaskID)
				if stored.Status != domain.TaskStatusInProgress || !reflect.DeepEqual(r.Spec, codexTaskRequestForTest().Spec) {
					t.Fatal("execution before valid state/spec")
				}
			}
			out, err := o.Run(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			stored, _, _ := tasks.FindByID(context.Background(), input.TaskID)
			if e.calls != 1 || stored.Status != status || out.CodexTask == nil || out.CodexResult == nil {
				t.Fatal("cycle incomplete")
			}
			if !reflect.DeepEqual(out.CodexTask.Payload.(CodexTask).Spec, codexTaskRequestForTest().Spec) || out.CodexResult.Payload.(CodexResult).Outcome != outcome || out.CodexResult.ProjectID != input.ProjectID || *out.CodexResult.TaskID != input.TaskID || out.CodexResult.CorrelationID != out.CodexTask.CorrelationID {
				t.Fatal("contract changed")
			}
			_, err = o.Run(context.Background(), input)
			if err == nil || e.calls != 1 {
				t.Fatal("incompatible state retried")
			}
		})
	}
}
func TestOrchestratorExecutorFailClosed(t *testing.T) {
	for _, kind := range []string{"missing config", "resolver", "invalid spec", "task metadata", "result metadata", "metadata correlation", "project", "task", "invalid result", "boundary", "cancel", "state"} {
		t.Run(kind, func(t *testing.T) {
			o, input, e, tasks := executorFixture(t)
			sentinel := errors.New("boundary")
			wantCalls := 0
			wantState := domain.TaskStatusAnalyzing
			switch kind {
			case "missing config":
				o.executorCycle = nil
			case "resolver":
				o.executorCycle.Resolver = TrustedExecutorTaskSpecResolver{}
			case "invalid spec":
				o.executorCycle.Resolver = TrustedExecutorTaskSpecResolver{Specs: map[ExecutorTaskIdentity]ports.ExecutorTaskSpec{{input.ProjectID, input.TaskID}: {Objective: "bad"}}}
			case "task metadata":
				o.executorCycle.TaskMetadata.MessageID = ""
			case "result metadata":
				o.executorCycle.ResultMetadata.MessageID = ""
			case "metadata correlation":
				o.executorCycle.ResultMetadata.CorrelationID = "other"
			case "project":
				e.result.ProjectID = "other"
				wantCalls = 1
			case "task":
				e.result.TaskID = "other"
				wantCalls = 1
			case "invalid result":
				e.result.Outcome = "CANCELLED"
				wantCalls = 1
			case "boundary":
				e.err = sentinel
				wantCalls = 1
			case "cancel":
				e.err = context.Canceled
				wantCalls = 1
			case "state":
				task, _, _ := tasks.FindByID(context.Background(), input.TaskID)
				task.Status = domain.TaskStatusReadyForCodex
				tasks.Save(context.Background(), task)
				wantState = domain.TaskStatusReadyForCodex
			}
			if wantCalls == 1 {
				wantState = domain.TaskStatusInProgress
			}
			out, err := o.Run(context.Background(), input)
			if err == nil || out != (OrchestrationOutput{}) || e.calls != wantCalls {
				t.Fatalf("failure not closed: %+v %v calls=%d", out, err, e.calls)
			}
			if kind == "boundary" && !errors.Is(err, sentinel) || kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("boundary lost")
			}
			stored, _, _ := tasks.FindByID(context.Background(), input.TaskID)
			if stored.Status != wantState {
				t.Fatalf("state=%s", stored.Status)
			}
			if wantCalls == 1 {
				if _, retryErr := o.Run(context.Background(), input); retryErr == nil || e.calls != 1 {
					t.Fatal("uncertain result retried")
				}
			}
		})
	}
}

type recordingCycleTasks struct {
	ports.TaskRepository
	statuses []domain.TaskStatus
}

func (r *recordingCycleTasks) Save(ctx context.Context, task domain.Task) error {
	r.statuses = append(r.statuses, task.Status)
	return r.TaskRepository.Save(ctx, task)
}

type cycleSpecResolver struct {
	spec  ports.ExecutorTaskSpec
	err   error
	check func(ports.PlannerDecision, PlannerContext)
}

func (r cycleSpecResolver) Resolve(_ context.Context, d ports.PlannerDecision, c PlannerContext) (ports.ExecutorTaskSpec, error) {
	if r.check != nil {
		r.check(d, c)
	}
	return r.spec, r.err
}
func TestOrchestratorWorkflowSequenceAndTrustedContext(t *testing.T) {
	o, input, e, tasks := executorFixture(t)
	recorder := &recordingCycleTasks{TaskRepository: tasks}
	config := *o.executorCycle
	config.Tasks = recorder
	config.Resolver = cycleSpecResolver{spec: codexTaskRequestForTest().Spec, check: func(d ports.PlannerDecision, c PlannerContext) {
		if c.CurrentTask.Title == "stale" || c.Project.ID != d.ProjectID || c.CurrentTask.ID != d.TaskID {
			t.Fatal("resolver received caller snapshot")
		}
	}}
	o = NewOrchestrator(o.builder, o.planner, o.resolver, o.transport, o.metadata, config)
	out, err := o.Run(context.Background(), input)
	if err != nil || out.Validate(input) != nil || e.calls != 1 {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recorder.statuses, []domain.TaskStatus{domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress, domain.TaskStatusDone}) {
		t.Fatal("workflow sequence", recorder.statuses)
	}
	for _, kind := range []string{"partial", "project", "task", "correlation", "session", "payload"} {
		t.Run(kind, func(t *testing.T) {
			copy := out
			task := *out.CodexTask
			result := *out.CodexResult
			copy.CodexTask = &task
			copy.CodexResult = &result
			switch kind {
			case "partial":
				copy.CodexResult = nil
			case "project":
				task.ProjectID = "other"
			case "task":
				id := domain.TaskID("other")
				result.TaskID = &id
			case "correlation":
				result.CorrelationID = "other"
			case "session":
				result.SessionID = nil
			case "payload":
				task.Payload = "untyped"
			}
			if copy.Validate(input) == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}
func TestOrchestratorUntrustedSpecAndStateChanges(t *testing.T) {
	for _, kind := range []string{"resolver error", "invalid spec", "state changed", "project changed"} {
		t.Run(kind, func(t *testing.T) {
			o, input, e, tasks := executorFixture(t)
			sentinel := errors.New("resolver boundary")
			resolver := cycleSpecResolver{spec: codexTaskRequestForTest().Spec}
			switch kind {
			case "resolver error":
				resolver.err = sentinel
			case "invalid spec":
				resolver.spec.Scope = []string{"../escape"}
			default:
				resolver.check = func(_ ports.PlannerDecision, _ PlannerContext) {
					task, _, _ := tasks.FindByID(context.Background(), input.TaskID)
					if kind == "state changed" {
						task.Status = domain.TaskStatusInProgress
					} else {
						task.ProjectID = "other"
					}
					if err := tasks.Save(context.Background(), task); err != nil {
						t.Fatal(err)
					}
				}
			}
			o.executorCycle.Resolver = resolver
			out, err := o.Run(context.Background(), input)
			if err == nil || out != (OrchestrationOutput{}) || e.calls != 0 {
				t.Fatal("invalid authority/state executed")
			}
			if kind == "resolver error" && !errors.Is(err, sentinel) {
				t.Fatal("resolver error lost")
			}
		})
	}
}
