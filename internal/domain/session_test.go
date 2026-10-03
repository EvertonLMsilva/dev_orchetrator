package domain

import (
	"reflect"
	"testing"
)

func TestSessionTypeValues(t *testing.T) {
	for _, tt := range []struct {
		value SessionType
		want  string
	}{
		{SessionTypePlanner, "PLANNER"},
		{SessionTypeExecutor, "EXECUTOR"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.value) != tt.want {
				t.Fatalf("SessionType = %q, want %q", tt.value, tt.want)
			}
		})
	}
}

func TestSessionStartReasonValues(t *testing.T) {
	for _, tt := range []struct {
		value SessionStartReason
		want  string
	}{
		{SessionStartReasonNewTask, "NEW_TASK"},
		{SessionStartReasonHandoff, "HANDOFF"},
		{SessionStartReasonInitial, "INITIAL"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.value) != tt.want {
				t.Fatalf("SessionStartReason = %q, want %q", tt.value, tt.want)
			}
		})
	}
}

func TestSessionCloseReasonValues(t *testing.T) {
	for _, tt := range []struct {
		value SessionCloseReason
		want  string
	}{
		{SessionCloseReasonDone, "DONE"},
		{SessionCloseReasonFailed, "FAILED"},
		{SessionCloseReasonCancelled, "CANCELLED"},
		{SessionCloseReasonContextBudget, "CONTEXT_BUDGET"},
		{SessionCloseReasonReplaced, "REPLACED"},
	} {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.value) != tt.want {
				t.Fatalf("SessionCloseReason = %q, want %q", tt.value, tt.want)
			}
		})
	}
}

func TestSessionPayloadFields(t *testing.T) {
	for _, tt := range []struct {
		name   string
		value  any
		reason reflect.Type
	}{
		{"started", SessionStartedPayload{}, reflect.TypeOf(SessionStartReason(""))},
		{"closed", SessionClosedPayload{}, reflect.TypeOf(SessionCloseReason(""))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			typ := reflect.TypeOf(tt.value)
			if typ.NumField() != 3 {
				t.Fatalf("field count = %d, want 3", typ.NumField())
			}
			fields := []struct {
				name string
				typ  reflect.Type
			}{
				{"SessionType", reflect.TypeOf(SessionType(""))},
				{"SessionID", reflect.TypeOf(SessionID(""))},
				{"Reason", tt.reason},
			}
			for i, want := range fields {
				got := typ.Field(i)
				if got.Name != want.name || got.Type != want.typ || got.Tag != "" || got.Anonymous || got.PkgPath != "" {
					t.Fatalf("field %d = %#v, want exported %s of type %v without tags or embedding", i, got, want.name, want.typ)
				}
			}
		})
	}
}

func TestSessionPayloadValuesPreserved(t *testing.T) {
	for _, sessionType := range []SessionType{SessionTypePlanner, SessionTypeExecutor, " custom ", ""} {
		for _, sessionID := range []SessionID{" session-1 \t", ""} {
			for _, reason := range []SessionStartReason{SessionStartReasonNewTask, SessionStartReasonHandoff, SessionStartReasonInitial, " custom ", ""} {
				payload := SessionStartedPayload{SessionType: sessionType, SessionID: sessionID, Reason: reason}
				if payload.SessionType != sessionType || payload.SessionID != sessionID || payload.Reason != reason {
					t.Fatalf("started payload did not preserve values: %#v", payload)
				}
			}
			for _, reason := range []SessionCloseReason{SessionCloseReasonDone, SessionCloseReasonFailed, SessionCloseReasonCancelled, SessionCloseReasonContextBudget, SessionCloseReasonReplaced, " custom ", ""} {
				payload := SessionClosedPayload{SessionType: sessionType, SessionID: sessionID, Reason: reason}
				if payload.SessionType != sessionType || payload.SessionID != sessionID || payload.Reason != reason {
					t.Fatalf("closed payload did not preserve values: %#v", payload)
				}
			}
		}
	}
}
