package application

import (
	"context"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"testing"
	"time"
)

type failureActor struct{}

func (failureActor) AuthenticateActor(context.Context, ports.ActorEvidence) (ports.Principal, error) {
	return ports.Principal{ID: "trusted"}, nil
}

type failurePlanner func(context.Context) error

func (p failurePlanner) Plan(ctx context.Context, _ ports.PlannerRequest) (ports.PlannerDecision, error) {
	return ports.PlannerDecision{}, p(ctx)
}

type failingBlockStore struct {
	ports.TaskRepository
	fail bool
}

func TestDevelopmentCancelBlockPersistenceFailureIsNotSuccess(t *testing.T) {
	ctx := context.Background()
	tasks := memory.NewTaskRepository()
	tasks.Save(ctx, domain.Task{ID: "task", ProjectID: "pilot", Title: "trusted", Status: domain.TaskStatusAnalyzing})
	cycle := &DevelopmentCycle{config: DevelopmentCycleConfig{Context: domain.CandidateContext{ProjectID: "pilot", TaskID: "task", CorrelationID: "corr"}}, tasks: failingBlockStore{TaskRepository: tasks, fail: true}, auth: failureActor{}}
	out, err := cycle.Cancel(ctx, ports.ActorEvidence{Provider: "discord", ExternalID: "123"})
	if !errors.Is(err, ErrBlockPersistence) || !out.Result.BlockPersistenceFailed || !cycle.terminal {
		t.Fatal("cancel reported success after failed block", out, err)
	}
}

func (s failingBlockStore) Save(ctx context.Context, t domain.Task) error {
	if s.fail {
		return errors.New("SECRET_STORE")
	}
	return s.TaskRepository.Save(ctx, t)
}

func TestDevelopmentBeginFailurePersistsBlockAfterCancellation(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		for _, stage := range []string{"HOST_START", "CANCELLED", "TIMEOUT"} {
			t.Run(stage+"/"+map[bool]string{false: "healthy", true: "save-failure"}[failSave], func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				projects, tasks := memory.NewProjectRepository(), memory.NewTaskRepository()
				projects.Save(ctx, domain.Project{ID: "pilot", Name: "pilot", Workspace: "managed:trusted"})
				tasks.Save(ctx, domain.Task{ID: "task", ProjectID: "pilot", Title: "trusted", Status: domain.TaskStatusAnalyzing})
				store := failingBlockStore{TaskRepository: tasks, fail: failSave}
				p := failurePlanner(func(ctx context.Context) error {
					if stage == "CANCELLED" {
						cancel()
						return ports.NewPlannerFailure(stage, context.Canceled)
					}
					if stage == "TIMEOUT" {
						<-ctx.Done()
						return ports.NewPlannerFailure(stage, ctx.Err())
					}
					return ports.NewPlannerFailure(stage, errors.New("SECRET_PROVIDER"))
				})
				cycle := &DevelopmentCycle{config: DevelopmentCycleConfig{Context: domain.CandidateContext{ProjectID: "pilot", TaskID: "task", CorrelationID: "corr"}, Timeout: 20 * time.Millisecond}, tasks: store, auth: failureActor{}, planner: NewCandidatePlanningOrchestrator(NewContextBuilder(projects, store), p), result: DevelopmentResult{ProjectID: "pilot", TaskID: "task", CorrelationID: "corr"}}
				out, err := cycle.Begin(ctx, ports.ActorEvidence{Provider: "discord", ExternalID: "123"}, "create note.txt")
				var failure *ports.PlannerFailure
				if err == nil || !errors.As(err, &failure) || failure.FailureStage() != stage || out.Review != nil || !cycle.terminal {
					t.Fatal("planner failure lost", out, err)
				}
				task, _, _ := tasks.FindByID(context.Background(), "task")
				if failSave {
					if !errors.Is(err, ErrBlockPersistence) || !out.Result.BlockPersistenceFailed || task.Status != domain.TaskStatusAnalyzing {
						t.Fatal("save failure lost", out, err)
					}
				} else if task.Status != domain.TaskStatusBlocked || out.Result.TaskState != domain.TaskStatusBlocked {
					t.Fatal("healthy store not blocked", out, err)
				}
			})
		}
	}
}
