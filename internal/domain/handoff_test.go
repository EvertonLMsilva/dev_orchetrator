package domain

import (
	"reflect"
	"testing"
)

func TestPlannerHandoffPayloadRepresentation(t *testing.T) {
	taskID := TaskID(" P0.4C2 ")
	status := TaskStatusInProgress
	tests := []struct {
		name    string
		payload PlannerHandoffPayload
		field   string
		want    any
	}{
		{"project ID", PlannerHandoffPayload{ProjectID: " project-1 "}, "ProjectID", ProjectID(" project-1 ")},
		{"nil current task", PlannerHandoffPayload{}, "CurrentTask", (*TaskID)(nil)},
		{"current task", PlannerHandoffPayload{CurrentTask: &taskID}, "CurrentTask", &taskID},
		{"nil task status", PlannerHandoffPayload{}, "TaskStatus", (*TaskStatus)(nil)},
		{"task status", PlannerHandoffPayload{TaskStatus: &status}, "TaskStatus", &status},
		{"nil decisions", PlannerHandoffPayload{}, "Decisions", []string(nil)},
		{"decisions order", PlannerHandoffPayload{Decisions: []string{" second ", "first", "second"}}, "Decisions", []string{" second ", "first", "second"}},
		{"empty decisions", PlannerHandoffPayload{Decisions: []string{}}, "Decisions", []string{}},
		{"nil evidence", PlannerHandoffPayload{}, "Evidence", []string(nil)},
		{"evidence order", PlannerHandoffPayload{Evidence: []string{" log B\n", "log A", "log B"}}, "Evidence", []string{" log B\n", "log A", "log B"}},
		{"empty evidence", PlannerHandoffPayload{Evidence: []string{}}, "Evidence", []string{}},
		{"nil completed", PlannerHandoffPayload{}, "Completed", []TaskID(nil)},
		{"completed order", PlannerHandoffPayload{Completed: []TaskID{"P0.3", "P0.2", "P0.3"}}, "Completed", []TaskID{"P0.3", "P0.2", "P0.3"}},
		{"empty completed", PlannerHandoffPayload{Completed: []TaskID{}}, "Completed", []TaskID{}},
		{"nil blocked", PlannerHandoffPayload{}, "Blocked", []TaskID(nil)},
		{"blocked order", PlannerHandoffPayload{Blocked: []TaskID{"task-B", "task-A", "task-B"}}, "Blocked", []TaskID{"task-B", "task-A", "task-B"}},
		{"empty blocked", PlannerHandoffPayload{Blocked: []TaskID{}}, "Blocked", []TaskID{}},
		{"next action", PlannerHandoffPayload{NextAction: " Continue\nwork "}, "NextAction", " Continue\nwork "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reflect.ValueOf(tt.payload).FieldByName(tt.field).Interface()
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("%s = %#v, want %#v", tt.field, got, tt.want)
			}
		})
	}
}

func TestPlannerHandoffPayloadAllFields(t *testing.T) {
	taskID := TaskID("P0.4C2")
	status := TaskStatusInProgress
	payload := PlannerHandoffPayload{
		ProjectID:   "project-1",
		CurrentTask: &taskID,
		TaskStatus:  &status,
		Decisions:   []string{"keep scope", "use pointers"},
		Evidence:    []string{"test result", "build result"},
		Completed:   []TaskID{"P0.4C1", "P0.4B"},
		Blocked:     []TaskID{"task-B", "task-A"},
		NextAction:  " Continue the task\n",
	}
	if payload.ProjectID != ProjectID("project-1") || payload.CurrentTask == nil || *payload.CurrentTask != taskID || payload.TaskStatus == nil || *payload.TaskStatus != status ||
		!reflect.DeepEqual(payload.Decisions, []string{"keep scope", "use pointers"}) ||
		!reflect.DeepEqual(payload.Evidence, []string{"test result", "build result"}) ||
		!reflect.DeepEqual(payload.Completed, []TaskID{"P0.4C1", "P0.4B"}) ||
		!reflect.DeepEqual(payload.Blocked, []TaskID{"task-B", "task-A"}) || payload.NextAction != " Continue the task\n" {
		t.Fatalf("fields were not preserved: %#v", payload)
	}
	if reflect.TypeOf(payload).NumField() != 8 {
		t.Fatal("payload must have exactly eight fields")
	}
}

func TestPlannerHandoffPayloadWithoutCurrentTaskAndStatus(t *testing.T) {
	payload := PlannerHandoffPayload{ProjectID: "project-1", NextAction: "select a task"}
	if payload.CurrentTask != nil || payload.TaskStatus != nil {
		t.Fatal("current task and task status must both support nil")
	}
}
