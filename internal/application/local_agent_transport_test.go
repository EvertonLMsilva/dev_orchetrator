package application

import (
	"context"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"os"
	"testing"
	"time"
)

type transportAgent struct {
	calls  int
	action domain.Action
	ctx    context.Context
	result ports.ActionResult
	err    error
}

func (f *transportAgent) Execute(ctx context.Context, a domain.Action) (ports.ActionResult, error) {
	f.calls++
	f.action = a
	f.ctx = ctx
	return f.result, f.err
}
func commandEnvelope(a domain.Action) domain.Envelope {
	return domain.Envelope{ProtocolVersion: "1", MessageType: domain.MessageTypeBotCommand, MessageID: "command", CorrelationID: "correlation", ProjectID: a.ProjectID, TaskID: a.TaskID, CreatedAt: time.Now(), Payload: BotCommand{Action: a}}
}
func TestTransportRoutes(t *testing.T) {
	for _, a := range dispatcherActions() {
		t.Run(string(a.Type), func(t *testing.T) {
			task := domain.TaskID("task")
			a.TaskID = &task
			caps := &dispatcherCapabilities{}
			agent := NewLocalAgent(&dispatcherRepo{found: true}, nil, NewDefaultActionAllowlist(), caps)
			e := commandEnvelope(a)
			ctx := context.Background()
			out, err := NewLocalAgentTransport(agent).Handle(ctx, e)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := out.Payload.(BotResult)
			if !ok || p.Status != BotResultSuccess || p.Result == nil || p.Result.Type != a.Type || p.Result.Validate() != nil || out.Validate() != nil || out.MessageType != domain.MessageTypeBotResult || out.MessageID == e.MessageID || out.CorrelationID != e.CorrelationID || out.ProjectID != e.ProjectID || out.TaskID == nil || *out.TaskID != task || len(caps.calls) != 1 || caps.ctx != ctx {
				t.Fatalf("incorrect result: %+v %+v", out, p)
			}
		})
	}
}
func TestTransportRejectsBeforeExecute(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domain.Envelope)
	}{
		{"type", func(e *domain.Envelope) { e.MessageType = domain.MessageTypeBotResult }},
		{"envelope", func(e *domain.Envelope) { e.CorrelationID = "" }},
		{"payload", func(e *domain.Envelope) { e.Payload = "secret" }},
		{"project", func(e *domain.Envelope) { e.ProjectID = "other" }},
		{"task", func(e *domain.Envelope) { id := domain.TaskID("other"); e.TaskID = &id }},
		{"unknown", func(e *domain.Envelope) { p := e.Payload.(BotCommand); p.Action.Type = "SHELL"; e.Payload = p }},
		{"params", func(e *domain.Envelope) {
			p := e.Payload.(BotCommand)
			p.Action.Params = domain.ActionParams{}
			e.Payload = p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &transportAgent{}
			e := commandEnvelope(dispatcherActions()[0])
			tc.mutate(&e)
			_, err := NewLocalAgentTransport(f).Handle(context.Background(), e)
			if !errors.Is(err, ErrInvalidBotCommand) || f.calls != 0 {
				t.Fatalf("err=%v calls=%d", err, f.calls)
			}
		})
	}
}
func TestTransportSafeErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status BotResultStatus
		code   string
	}{
		{ErrPolicyBlocked, BotResultBlocked, "POLICY_BLOCKED"}, {ErrPolicyRequiresApproval, BotResultApprovalRequired, "APPROVAL_REQUIRED"}, {ErrActionNotAllowed, BotResultBlocked, "ACTION_NOT_ALLOWED"}, {errors.New("secret /private command stack"), BotResultFailed, "EXECUTION_FAILED"}, {context.Canceled, BotResultFailed, "CANCELED"}, {context.DeadlineExceeded, BotResultFailed, "DEADLINE_EXCEEDED"},
	} {
		f := &transportAgent{err: tc.err}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out, err := NewLocalAgentTransport(f).Handle(ctx, commandEnvelope(dispatcherActions()[0]))
		if err != nil {
			t.Fatal(err)
		}
		p := out.Payload.(BotResult)
		if p.Status != tc.status || p.Error == nil || p.Error.Code != tc.code || p.Result != nil || f.ctx != ctx {
			t.Fatalf("unsafe mapping: %+v", p)
		}
	}
}
func TestTransportPolicyDoesNotExecute(t *testing.T) {
	for _, decision := range []domain.PolicyDecision{domain.PolicyDecisionBlocked, domain.PolicyDecisionApproval} {
		caps := &dispatcherCapabilities{}
		agent := NewLocalAgent(&dispatcherRepo{found: true}, dispatcherPolicy{decision}, NewDefaultActionAllowlist(), caps)
		_, err := NewLocalAgentTransport(agent).Handle(context.Background(), commandEnvelope(dispatcherActions()[0]))
		if err != nil || len(caps.calls) != 0 {
			t.Fatal("policy bypass")
		}
	}
}
func TestTransportRejectsInvalidAgentResult(t *testing.T) {
	for _, r := range []ports.ActionResult{{}, {Type: domain.ActionTypeGitStatus, GitStatusResult: &ports.GitStatusResult{}}} {
		out, err := NewLocalAgentTransport(&transportAgent{result: r}).Handle(context.Background(), commandEnvelope(dispatcherActions()[0]))
		if err != nil {
			t.Fatal(err)
		}
		p := out.Payload.(BotResult)
		if p.Status != BotResultFailed || p.Result != nil || p.Error.Code != "INVALID_RESULT" {
			t.Fatal(p)
		}
	}
}
func TestTransportReadFileIntegration(t *testing.T) {
	w := t.TempDir()
	if err := os.WriteFile(w+"/note.txt", []byte("safe content"), 0600); err != nil {
		t.Fatal(err)
	}
	repo := memory.NewProjectRepository()
	if err := repo.Save(context.Background(), domain.Project{ID: "project", Workspace: w}); err != nil {
		t.Fatal(err)
	}
	agent := NewLocalAgent(repo, nil, NewDefaultActionAllowlist(), nil)
	a := domain.Action{Type: domain.ActionTypeReadFile, ProjectID: "project", Params: domain.ActionParams{ReadFile: &domain.ReadFileParams{Path: "note.txt"}}}
	out, err := NewLocalAgentTransport(agent).Handle(context.Background(), commandEnvelope(a))
	if err != nil {
		t.Fatal(err)
	}
	p := out.Payload.(BotResult)
	if p.Status != BotResultSuccess || p.Result.ReadFileResult.Content != "safe content" {
		t.Fatal(p)
	}
}

func TestTransportTaskCoherence(t *testing.T) {
	for _, ids := range [][2]domain.TaskID{{"task", "other"}, {"task", ""}, {"", "task"}, {" ", " "}} {
		a := dispatcherActions()[0]
		a.TaskID = &ids[0]
		e := commandEnvelope(a)
		e.TaskID = &ids[1]
		f := &transportAgent{}
		_, err := NewLocalAgentTransport(f).Handle(context.Background(), e)
		if !errors.Is(err, ErrInvalidBotCommand) || f.calls != 0 {
			t.Fatal("incoherent task accepted")
		}
	}
}
func TestTransportDispatcherCancellation(t *testing.T) {
	repo := memory.NewProjectRepository()
	agent := NewLocalAgent(repo, nil, NewDefaultActionAllowlist(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := NewLocalAgentTransport(agent).Handle(ctx, commandEnvelope(dispatcherActions()[0]))
	if err != nil {
		t.Fatal(err)
	}
	p := out.Payload.(BotResult)
	if p.Status != BotResultFailed || p.Error.Code != "CANCELED" {
		t.Fatal(p)
	}
}
func TestTransportMissingAgent(t *testing.T) {
	out, err := NewLocalAgentTransport(nil).Handle(context.Background(), commandEnvelope(dispatcherActions()[0]))
	if err != nil {
		t.Fatal(err)
	}
	p := out.Payload.(BotResult)
	if p.Status != BotResultFailed || p.Error.Code != "AGENT_UNAVAILABLE" {
		t.Fatal(p)
	}
}
