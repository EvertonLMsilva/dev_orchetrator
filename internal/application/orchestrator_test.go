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
				agent.err = agentErr
				out, err := o.Run(context.Background(), input)
				if err != nil {
					t.Fatal(err)
				}
				if out.Decision != p.decisions[1] || len(p.calls) != 2 || transport.calls != 1 || agent.calls != 1 {
					t.Fatalf("wrong round limits/output: %+v", out)
				}
				canonical, err := o.builder.Build(context.Background(), input.ProjectID, input.TaskID)
				if err != nil {
					t.Fatal(err)
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
				if err != nil || stored != canonical.CurrentTask {
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
