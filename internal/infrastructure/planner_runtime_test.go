package infrastructure

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPlannerRuntimeRequestLimits(t *testing.T) {
	valid := PlannerRuntimeRequest{Instructions: "Decide", Input: json.RawMessage(`{}`), OutputSchema: json.RawMessage(plannerDecisionSchema), Limits: PlannerRuntimeLimits{MaxOutputBytes: plannerOutputLimit, Timeout: plannerTimeout}}
	if valid.Validate() != nil {
		t.Fatal("valid request rejected")
	}
	for _, mutate := range []func(*PlannerRuntimeRequest){
		func(r *PlannerRuntimeRequest) { r.Instructions = "" },
		func(r *PlannerRuntimeRequest) { r.Input = json.RawMessage(`{`) },
		func(r *PlannerRuntimeRequest) { r.OutputSchema = nil },
		func(r *PlannerRuntimeRequest) { r.Limits.MaxOutputBytes = 0 },
		func(r *PlannerRuntimeRequest) { r.Limits.MaxOutputBytes = plannerOutputLimit + 1 },
		func(r *PlannerRuntimeRequest) { r.Limits.Timeout = 0 },
		func(r *PlannerRuntimeRequest) { r.Limits.Timeout = plannerTimeout + 1 },
	} {
		r := valid
		mutate(&r)
		if r.Validate() == nil {
			t.Fatal("invalid limits or data accepted")
		}
	}
}

func TestPlannerRuntimeRequestInferenceAuthorityOnly(t *testing.T) {
	allowed := map[string]bool{"Instructions": true, "Input": true, "OutputSchema": true, "Limits": true}
	typ := reflect.TypeOf(PlannerRuntimeRequest{})
	for i := 0; i < typ.NumField(); i++ {
		if !allowed[typ.Field(i).Name] {
			t.Fatalf("executable or configurable authority field: %s", typ.Field(i).Name)
		}
	}
	for _, name := range []string{"Tools", "Command", "Shell", "Executable", "Argv", "Workspace", "MCP", "Plugins", "Network", "AuthTokens"} {
		if _, found := typ.FieldByName(name); found {
			t.Fatalf("forbidden field %s", name)
		}
	}
	result := reflect.TypeOf(PlannerRuntimeResult{})
	if result.NumField() != 1 || result.Field(0).Name != "StructuredOutput" {
		t.Fatal("result must contain only final structured output")
	}
}
