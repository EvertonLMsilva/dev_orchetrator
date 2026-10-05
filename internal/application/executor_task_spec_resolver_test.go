package application

import (
	"context"
	"dev-orchestrator/internal/ports"
	"errors"
	"reflect"
	"testing"
)

func TestTrustedExecutorTaskSpecResolver(t *testing.T) {
	r := codexTaskRequestForTest()
	canonical := roundFixture(t).Context
	resolver := TrustedExecutorTaskSpecResolver{Specs: map[ExecutorTaskIdentity]ports.ExecutorTaskSpec{{r.Decision.ProjectID, r.Decision.TaskID}: r.Spec}}
	got, err := resolver.Resolve(context.Background(), r.Decision, canonical)
	if err != nil || !reflect.DeepEqual(got, r.Spec) {
		t.Fatal("trusted authority changed", err)
	}
	got.Scope[0] = "changed.go"
	got.AcceptanceCriteria[0] = "changed"
	if r.Spec.Scope[0] != "internal/example.go" || r.Spec.AcceptanceCriteria[0] != "Invalid inputs are rejected" {
		t.Fatal("authority aliases configuration")
	}
	for _, kind := range []string{"missing", "project", "task", "decision", "type", "invalid", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			local := resolver
			d := r.Decision
			c := canonical
			ctx := context.Background()
			switch kind {
			case "missing":
				local.Specs = nil
			case "project":
				c.Project.ID = "other"
			case "task":
				c.CurrentTask.ID = "other"
			case "decision":
				d.Reason = ""
			case "type":
				d.Type = ports.PlannerDecisionBlock
			case "invalid":
				local.Specs = map[ExecutorTaskIdentity]ports.ExecutorTaskSpec{{d.ProjectID, d.TaskID}: {Objective: "invalid"}}
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			got, err := local.Resolve(ctx, d, c)
			if err == nil || !reflect.DeepEqual(got, ports.ExecutorTaskSpec{}) {
				t.Fatal("resolver not closed")
			}
			if kind == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost")
			}
		})
	}
}
