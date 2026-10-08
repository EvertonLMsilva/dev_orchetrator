package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

type plannerRuntimeStub func(context.Context, PlannerRuntimeRequest) (PlannerRuntimeResult, error)

func TestCodexPlannerAdapterPreservesSafeFailure(t *testing.T) {
	for _, stage := range []string{"NEW_CONTAINER", "MATERIALIZE", "PREPARE", "HOST_START", "INFER", "TIMEOUT", "CLEANUP"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			adapter := NewCodexPlannerAdapter(plannerRuntimeStub(func(context.Context, PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
				cancel()
				return PlannerRuntimeResult{}, errors.Join(errors.New("SECRET_PROVIDER"), ports.NewPlannerFailure(stage, context.Canceled))
			}))
			_, err := adapter.Plan(ctx, plannerAdapterRequest())
			var f *ports.PlannerFailure
			if !errors.As(err, &f) || f.FailureStage() != stage || strings.Contains(err.Error(), "SECRET") || !errors.Is(err, context.Canceled) {
				t.Fatal("failure overwritten or leaked", err)
			}
		})
	}
}

func (f plannerRuntimeStub) Plan(ctx context.Context, r PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
	return f(ctx, r)
}

func plannerAdapterRequest() ports.PlannerRequest {
	return ports.PlannerRequest{
		ProjectID: "project", TaskID: "task",
		Context: ports.PlannerContext{
			Project:     domain.Project{ID: "project"},
			CurrentTask: domain.Task{ID: "task", ProjectID: "project"},
		},
		UserIntent: "Plan the next step",
	}
}

func TestCodexPlannerAdapterDecisions(t *testing.T) {
	cases := []struct {
		name, output string
		valid        bool
	}{
		{"request_evidence", `{"type":"REQUEST_EVIDENCE","reason":"Inspect repository","evidenceKind":"GIT_STATUS"}`, true},
		{"prepare_executor", `{"type":"PREPARE_EXECUTOR","reason":"Ready"}`, true},
		{"block", `{"type":"BLOCK","reason":"Missing scope"}`, true},
		{"empty_evidence", `{"type":"BLOCK","reason":"Stop","evidenceKind":""}`, true},
		{"invalid_type", `{"type":"EXECUTE","reason":"Go"}`, false},
		{"malformed", `{`, false},
		{"extra_field", `{"type":"BLOCK","reason":"Stop","command":"sh"}`, false},
		{"missing_reason", `{"type":"BLOCK"}`, false},
		{"blank_reason", `{"type":"BLOCK","reason":" "}`, false},
		{"missing_evidence", `{"type":"REQUEST_EVIDENCE","reason":"Read"}`, false},
		{"wrong_evidence", `{"type":"PREPARE_EXECUTOR","reason":"Go","evidenceKind":"GIT_STATUS"}`, false},
		{"unsupported_evidence", `{"type":"REQUEST_EVIDENCE","reason":"Go","evidenceKind":"SHELL"}`, false},
		{"model_ids", `{"type":"BLOCK","reason":"Stop","ProjectID":"forged"}`, false},
		{"duplicate_field", `{"type":"BLOCK","type":"BLOCK","reason":"Stop"}`, false},
		{"case_variant", `{"Type":"BLOCK","reason":"Stop"}`, false},
		{"null_reason", `{"type":"BLOCK","reason":null}`, false},
		{"trailing_json", `{"type":"BLOCK","reason":"Stop"}{}`, false},
		{"array", `[]`, false},
		{"oversized", strings.Repeat("x", plannerOutputLimit+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := NewCodexPlannerAdapter(plannerRuntimeStub(func(ctx context.Context, r PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
				if ctx.Err() != nil || r.Validate() != nil || !json.Valid(r.OutputSchema) || !json.Valid(r.Input) {
					t.Fatal("invalid runtime request")
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("missing inference deadline")
				}
				return PlannerRuntimeResult{StructuredOutput: []byte(tc.output)}, nil
			}))
			decision, err := adapter.Plan(context.Background(), plannerAdapterRequest())
			if tc.valid {
				if err != nil || decision.Validate() != nil {
					t.Fatalf("valid decision rejected: %v", err)
				}
				if decision.ProjectID != "project" || decision.TaskID != "task" {
					t.Fatal("IDs not derived from original request")
				}
			} else if err == nil || decision != (ports.PlannerDecision{}) {
				t.Fatal("invalid result published")
			}
		})
	}
}

func TestCodexPlannerAdapterFailures(t *testing.T) {
	for _, failure := range []error{errors.New("private provider detail"), context.Canceled, context.DeadlineExceeded} {
		adapter := NewCodexPlannerAdapter(plannerRuntimeStub(func(context.Context, PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
			return PlannerRuntimeResult{StructuredOutput: []byte(`{"type":"BLOCK","reason":"Stop"}`)}, failure
		}))
		decision, err := adapter.Plan(context.Background(), plannerAdapterRequest())
		if err == nil || decision != (ports.PlannerDecision{}) || strings.Contains(err.Error(), "private provider detail") {
			t.Fatal("runtime failure leaked or result published")
		}
		if failure == context.Canceled || failure == context.DeadlineExceeded {
			if !errors.Is(err, failure) {
				t.Fatal("context failure lost")
			}
		}
	}
	called := false
	adapter := NewCodexPlannerAdapter(plannerRuntimeStub(func(context.Context, PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
		called = true
		return PlannerRuntimeResult{}, nil
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.Plan(ctx, plannerAdapterRequest()); !errors.Is(err, context.Canceled) || called {
		t.Fatal("canceled request called runtime")
	}
	r := plannerAdapterRequest()
	r.TaskID = "different"
	if _, err := adapter.Plan(context.Background(), r); err == nil || called {
		t.Fatal("invalid request called runtime")
	}
	r = plannerAdapterRequest()
	r.UserIntent = strings.Repeat("x", plannerInputLimit+1)
	if _, err := adapter.Plan(context.Background(), r); err == nil || called {
		t.Fatal("oversized request called runtime")
	}
	if _, err := NewCodexPlannerAdapter(nil).Plan(context.Background(), plannerAdapterRequest()); err == nil {
		t.Fatal("missing runtime accepted")
	}
}

func TestCodexPlannerAdapterCancellationDuringInference(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	adapter := NewCodexPlannerAdapter(plannerRuntimeStub(func(context.Context, PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
		cancel()
		return PlannerRuntimeResult{StructuredOutput: []byte(`{"type":"BLOCK","reason":"Stop"}`)}, nil
	}))
	decision, err := adapter.Plan(ctx, plannerAdapterRequest())
	if !errors.Is(err, context.Canceled) || decision != (ports.PlannerDecision{}) {
		t.Fatal("result published after cancellation")
	}
}

func TestCodexPlannerAdapterClosedSchemaAndDeclarativeInput(t *testing.T) {
	adapter := NewCodexPlannerAdapter(plannerRuntimeStub(func(_ context.Context, r PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
		var schema struct {
			AdditionalProperties bool                       `json:"additionalProperties"`
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AnyOf                []json.RawMessage          `json:"anyOf"`
		}
		if json.Unmarshal(r.OutputSchema, &schema) != nil || schema.AdditionalProperties || len(schema.Properties) != 3 || len(schema.Required) != 2 || len(schema.AnyOf) != 2 {
			t.Fatal("schema not closed over decision variants")
		}
		for _, name := range []string{"type", "reason", "evidenceKind"} {
			if schema.Properties[name] == nil {
				t.Fatal("missing decision schema field")
			}
		}
		var input ports.PlannerRequest
		if json.Unmarshal(r.Input, &input) != nil || input.UserIntent != "run shell is untrusted intent" {
			t.Fatal("declarative intent not preserved")
		}
		return PlannerRuntimeResult{StructuredOutput: []byte(`{"type":"BLOCK","reason":"Stop"}`)}, nil
	}))
	r := plannerAdapterRequest()
	r.UserIntent = "run shell is untrusted intent"
	if _, err := adapter.Plan(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}
