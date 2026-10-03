package application

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func refinementDecision(kind ports.PlannerDecisionType) ports.PlannerDecision {
	d := ports.PlannerDecision{ProjectID: "project-1", TaskID: "task-1", Type: kind, Reason: "Analysis result"}
	if kind == ports.PlannerDecisionRequestEvidence {
		d.EvidenceKind = domain.ActionTypeSearch
	}
	return d
}

func TestTaskRefinerDecisions(t *testing.T) {
	for _, tc := range []struct {
		kind   ports.PlannerDecisionType
		target domain.TaskStatus
		saves  int
	}{
		{ports.PlannerDecisionRequestEvidence, domain.TaskStatusAnalyzing, 0},
		{ports.PlannerDecisionPrepareExecutor, domain.TaskStatusReadyForCodex, 1},
		{ports.PlannerDecisionBlock, domain.TaskStatusBlocked, 1},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			original := plannedTask(t)
			original.Status = domain.TaskStatusAnalyzing
			repo := &fakeTaskRepository{task: original, found: true}
			ctx := context.Background()
			got, err := NewTaskRefiner(repo).Refine(ctx, refinementDecision(tc.kind))
			want := original
			want.Status = tc.target
			if err != nil || got != want {
				t.Fatalf("got=%+v err=%v want=%+v", got, err, want)
			}
			if repo.saveCalls != tc.saves || repo.findCalls != 1 {
				t.Fatalf("find=%d save=%d", repo.findCalls, repo.saveCalls)
			}
			if tc.saves == 1 && (repo.saved != want || repo.saveCtx != ctx) {
				t.Fatal("incorrect persistence")
			}
			if repo.task != original || repo.findID != original.ID || repo.findCtx != ctx {
				t.Fatal("original or lookup changed")
			}
		})
	}
}

func TestTaskRefinerFailures(t *testing.T) {
	findErr, saveErr := errors.New("find failure"), errors.New("save failure")
	for _, tc := range []struct {
		name         string
		change       func(*fakeTaskRepository, *ports.PlannerDecision)
		want         error
		finds, saves int
	}{
		{"invalid", func(r *fakeTaskRepository, d *ports.PlannerDecision) { d.Reason = "" }, ports.ErrInvalidPlannerDecision, 0, 0},
		{"unknown", func(r *fakeTaskRepository, d *ports.PlannerDecision) { d.Type = "UNKNOWN" }, ports.ErrInvalidPlannerDecision, 0, 0},
		{"missing", func(r *fakeTaskRepository, d *ports.PlannerDecision) { r.found = false }, ErrTaskNotFound, 1, 0},
		{"project mismatch", func(r *fakeTaskRepository, d *ports.PlannerDecision) { r.task.ProjectID = "other" }, ErrPlannerTaskIdentityMismatch, 1, 0},
		{"task mismatch", func(r *fakeTaskRepository, d *ports.PlannerDecision) { r.task.ID = "other" }, ErrPlannerTaskIdentityMismatch, 1, 0},
		{"find failure", func(r *fakeTaskRepository, d *ports.PlannerDecision) { r.findErr = findErr }, findErr, 1, 0},
		{"save failure", func(r *fakeTaskRepository, d *ports.PlannerDecision) { r.saveErr = saveErr }, saveErr, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := plannedTask(t)
			task.Status = domain.TaskStatusAnalyzing
			repo := &fakeTaskRepository{task: task, found: true}
			d := refinementDecision(ports.PlannerDecisionPrepareExecutor)
			tc.change(repo, &d)
			got, err := NewTaskRefiner(repo).Refine(context.Background(), d)
			if !errors.Is(err, tc.want) || got != (domain.Task{}) {
				t.Fatalf("got=%+v err=%v want=%v", got, err, tc.want)
			}
			if repo.findCalls != tc.finds || repo.saveCalls != tc.saves {
				t.Fatalf("find=%d save=%d", repo.findCalls, repo.saveCalls)
			}
		})
	}
}

func TestTaskRefinerRejectsOtherStates(t *testing.T) {
	for _, status := range []domain.TaskStatus{domain.TaskStatusPlanned, domain.TaskStatusReadyForAnalysis, domain.TaskStatusReadyForCodex, domain.TaskStatusInProgress, domain.TaskStatusBlocked, domain.TaskStatusDone, domain.TaskStatusFailed, domain.TaskStatusCancelled, "UNKNOWN", ""} {
		for _, kind := range []ports.PlannerDecisionType{ports.PlannerDecisionRequestEvidence, ports.PlannerDecisionPrepareExecutor, ports.PlannerDecisionBlock} {
			t.Run(string(status)+"/"+string(kind), func(t *testing.T) {
				task := plannedTask(t)
				task.Status = status
				repo := &fakeTaskRepository{task: task, found: true}
				got, err := NewTaskRefiner(repo).Refine(context.Background(), refinementDecision(kind))
				if !errors.Is(err, ErrTaskNotAnalyzing) || got != (domain.Task{}) || repo.saveCalls != 0 {
					t.Fatalf("got=%+v err=%v saves=%d", got, err, repo.saveCalls)
				}
			})
		}
	}
}

func TestTaskRefinerCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := &fakeTaskRepository{}
	got, err := NewTaskRefiner(repo).Refine(ctx, refinementDecision(ports.PlannerDecisionRequestEvidence))
	if !errors.Is(err, context.Canceled) || got != (domain.Task{}) || repo.findCalls != 0 || repo.saveCalls != 0 {
		t.Fatalf("got=%+v err=%v repo=%+v", got, err, repo)
	}
}
