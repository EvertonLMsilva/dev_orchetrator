package readcontracts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStrictBoundaryAllOperations(t *testing.T) {
	for _, newRequest := range []func() any{
		func() any { return &ProjectStatusRequest{} },
		func() any { return &ProjectTasksRequest{} },
		func() any { return &GitStatusRequest{} },
		func() any { return &ExecutionStatusRequest{} },
	} {
		valid := `{"projectId":"p","correlationId":"c"}`
		if _, ok := newRequest().(*ProjectTasksRequest); ok {
			valid = `{"projectId":"p","correlationId":"c","limit":1}`
		}
		if err := Decode([]byte(valid), newRequest()); err != nil {
			t.Fatalf("invalid baseline: %v", err)
		}
		for _, field := range []string{"workspace", "path", "shell", "argv", "credentials", "token", "unknownField"} {
			raw := strings.TrimSuffix(valid, "}") + `,"` + field + `":"x"}`
			if Decode([]byte(raw), newRequest()) == nil {
				t.Fatalf("accepted %s", field)
			}
		}
		if Decode([]byte(strings.TrimSuffix(valid, "}")+`,"projectId":"q"}`), newRequest()) == nil {
			t.Fatal("accepted duplicate field")
		}
	}
	dst := ProjectStatusRequest{ProjectID: "original", CorrelationID: "original"}
	if Decode([]byte(`{"projectId":"new","correlationId":""}`), &dst) == nil || dst.ProjectID != "original" {
		t.Fatal("failed decode changed destination")
	}
	meta := Metadata{SchemaVersion: SchemaVersion, ProjectID: "p", CorrelationID: "c", ObservedAt: time.Now().UTC()}
	for _, response := range []any{
		ProjectStatusResponse{Metadata: meta, Name: "p", Queries: QueryAvailability{Available, Available, Unavailable, Unavailable}},
		ProjectTasksResponse{Metadata: meta, Tasks: []TaskSummary{}},
		GitStatusResponse{Metadata: meta, ChangedEntries: MaxChangedEntries},
		ExecutionStatusResponse{Metadata: meta, ExecutionObservation: Unavailable},
	} {
		raw, err := json.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"workspace", "path", "shell", "argv", "credentials", "sessionId"} {
			if strings.Contains(string(raw), `"`+forbidden+`"`) {
				t.Fatal("unsafe projection")
			}
		}
		var dst any
		switch response.(type) {
		case ProjectStatusResponse:
			dst = &ProjectStatusResponse{}
		case ProjectTasksResponse:
			dst = &ProjectTasksResponse{}
		case GitStatusResponse:
			dst = &GitStatusResponse{}
		case ExecutionStatusResponse:
			dst = &ExecutionStatusResponse{}
		}
		if Decode(raw, dst) != nil {
			t.Fatalf("round trip failed: %s", raw)
		}
	}
	if (GitStatusResponse{Metadata: meta, ChangedEntries: MaxChangedEntries + 1}).Validate() == nil {
		t.Fatal("count overflow")
	}
	if (ProjectTasksResponse{Metadata: meta, Tasks: []TaskSummary{{"t", "x", "DONE"}, {"t", "y", "PLANNED"}}}).Validate() == nil {
		t.Fatal("duplicate task")
	}
}

func TestExecutionOptionalTaskID(t *testing.T) {
	base := `{"schemaVersion":"1","projectId":"p","correlationId":"c","observedAt":"2026-10-07T12:00:00Z","executionObservation":"UNAVAILABLE"`
	for _, suffix := range []string{`}`, `,"taskId":"t","taskStatus":"DONE"}`} {
		if err := Decode([]byte(base+suffix), &ExecutionStatusResponse{}); err != nil {
			t.Fatalf("valid optional task: %v", err)
		}
	}
	for _, suffix := range []string{
		`,"taskId":""}`,
		`,"taskId":"","taskStatus":""}`,
		`,"taskId":" ","taskStatus":""}`,
		`,"taskId":null}`,
		`,"taskId":"","taskStatus":"DONE"}`,
		`,"taskId":"t","taskStatus":"DONE","taskId":"t"}`,
	} {
		if Decode([]byte(base+suffix), &ExecutionStatusResponse{}) == nil {
			t.Errorf("accepted adversarial task ID: %s", suffix)
		}
	}
}

func TestRequests(t *testing.T) {
	valid := `{"projectId":"p1","correlationId":"c1"}`
	for _, v := range []any{&ProjectStatusRequest{}, &GitStatusRequest{}, &ExecutionStatusRequest{}} {
		if err := Decode([]byte(valid), v); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"workspace", "path", "shell", "argv", "credentials", "token", "ProjectId"} {
		if Decode([]byte(strings.TrimSuffix(valid, "}")+`,"`+field+`":"x"}`), &ProjectStatusRequest{}) == nil {
			t.Errorf("accepted %s", field)
		}
	}
	for _, raw := range []string{`null`, valid + ` {}`, `{"projectId":"","correlationId":"c"}`, `{"projectId":"p","correlationId":" "}`, `{"projectId":"p","projectId":"q","correlationId":"c"}`} {
		if Decode([]byte(raw), &ProjectStatusRequest{}) == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if Decode([]byte(`{"projectId":"p","correlationId":"c","limit":100,"offset":0}`), &ProjectTasksRequest{}) != nil {
		t.Fatal("valid pagination")
	}
	for _, limit := range []int{0, -1, 101} {
		if (ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: limit}).Validate() == nil {
			t.Fatal("limit accepted")
		}
	}
	if (ProjectTasksRequest{ProjectID: "p", CorrelationID: "c", Limit: 1, Offset: MaxOffset + 1}).Validate() == nil {
		t.Fatal("offset accepted")
	}
	if (ProjectStatusRequest{ProjectID: strings.Repeat("x", MaxIDBytes+1), CorrelationID: "c"}).Validate() == nil {
		t.Fatal("long ID")
	}
	if Decode([]byte(strings.Repeat(" ", MaxJSONBytes+1)), &ProjectStatusRequest{}) == nil {
		t.Fatal("oversized input")
	}
}

func TestResponses(t *testing.T) {
	meta := Metadata{SchemaVersion: SchemaVersion, ProjectID: "p", CorrelationID: "c", ObservedAt: time.Now().UTC()}
	for _, v := range []interface{ Validate() error }{
		ProjectStatusResponse{Metadata: meta, Name: "project", Queries: QueryAvailability{ProjectStatus: Available, ProjectTasks: Available, GitStatus: Unavailable, ExecutionStatus: Unavailable}},
		ProjectTasksResponse{Metadata: meta, Tasks: []TaskSummary{{TaskID: "t", Title: "task", TaskStatus: "IN_PROGRESS"}}},
		GitStatusResponse{Metadata: meta, ChangedEntries: 1},
		ExecutionStatusResponse{Metadata: meta, ExecutionObservation: Unavailable, TaskID: "t", TaskStatus: "IN_PROGRESS"},
	} {
		if err := v.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*Metadata){func(m *Metadata) { m.SchemaVersion = "2" }, func(m *Metadata) { m.ProjectID = "" }, func(m *Metadata) { m.CorrelationID = "" }, func(m *Metadata) { m.ObservedAt = time.Time{} }} {
		m := meta
		mutate(&m)
		if m.Validate() == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	for _, status := range []string{"RUNNING", "DONE", "", "AVAILABLE"} {
		if (ExecutionStatusResponse{Metadata: meta, ExecutionObservation: Availability(status)}).Validate() == nil {
			t.Fatal("inferred observation accepted")
		}
	}
	if (ExecutionStatusResponse{Metadata: meta, ExecutionObservation: Unavailable, TaskStatus: "DONE"}).Validate() == nil {
		t.Fatal("task status without ID")
	}
	if (ProjectTasksResponse{Metadata: meta, Tasks: make([]TaskSummary, MaxTasks+1)}).Validate() == nil {
		t.Fatal("too many tasks")
	}
	if (TaskSummary{TaskID: "t", Title: strings.Repeat("x", MaxTextBytes+1), TaskStatus: "DONE"}).Validate() == nil {
		t.Fatal("long title")
	}
	if (TaskSummary{TaskID: "t", Title: "x", TaskStatus: "RUNNING"}).Validate() == nil {
		t.Fatal("unknown task state")
	}
	if (GitStatusResponse{Metadata: meta, ChangedEntries: -1}).Validate() == nil {
		t.Fatal("negative count")
	}
	raw := `{"schemaVersion":"1","projectId":"p","correlationId":"c","observedAt":"2026-10-07T12:00:00Z","tasks":[{"taskId":"t","title":"x","taskStatus":"DONE","workspace":"secret"}],"hasMore":false}`
	if Decode([]byte(raw), &ProjectTasksResponse{}) == nil {
		t.Fatal("nested unknown field")
	}
}
