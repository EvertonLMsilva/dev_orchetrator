package application

import (
	"encoding/json"
	"reflect"
	"testing"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

func TestCodexResultCodecRoundTrip(t *testing.T) {
	for _, outcome := range []ports.ExecutorOutcome{ports.ExecutorOutcomeDone, ports.ExecutorOutcomeBlocked, ports.ExecutorOutcomeFailed} {
		t.Run(string(outcome), func(t *testing.T) {
			for _, summary := range []string{"BLOCKED", "  FAILED; $(command)\ntexto  "} {
				e := canonicalResultForTest()
				e.Payload = CodexResult{Outcome: outcome, Summary: summary}
				session := domain.SessionID("session")
				e.SessionID = &session
				data, err := EncodeCodexResult(e)
				if err != nil {
					t.Fatal(err)
				}
				got, err := DecodeCodexResult(data)
				if err != nil || !reflect.DeepEqual(got, e) {
					t.Fatalf("round trip: %#v %v", got, err)
				}
				var shape map[string]json.RawMessage
				if err := json.Unmarshal(data, &shape); err != nil {
					t.Fatal(err)
				}
				if len(shape) != 9 {
					t.Fatalf("unexpected envelope fields: %s", data)
				}
				for _, key := range []string{"ProtocolVersion", "MessageType", "MessageID", "CorrelationID", "ProjectID", "TaskID", "SessionID", "CreatedAt", "Payload"} {
					if _, ok := shape[key]; !ok {
						t.Fatalf("missing %s", key)
					}
				}
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(shape["Payload"], &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload) != 2 || payload["Outcome"] == nil || payload["Summary"] == nil {
					t.Fatal("provider metadata in payload")
				}
			}
		})
	}
}

func TestCodexResultCodecRejectsInvalidJSON(t *testing.T) {
	base, err := json.Marshal(canonicalResultForTest())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"ProtocolVersion", "MessageType", "MessageID", "CorrelationID", "ProjectID", "TaskID", "CreatedAt", "Payload", "Outcome", "Summary", "provider"} {
		for _, value := range []string{"", `null`, `""`, `" "`, `123`, `true`, `[]`, `{}`, `"INVALID"`} {
			t.Run(key+"/"+value, func(t *testing.T) {
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(base, &fields); err != nil {
					t.Fatal(err)
				}
				target := fields
				if key == "Outcome" || key == "Summary" || key == "provider" {
					target = map[string]json.RawMessage{}
					if err := json.Unmarshal(fields["Payload"], &target); err != nil {
						t.Fatal(err)
					}
				}
				if value == "" {
					delete(target, key)
				} else {
					target[key] = json.RawMessage(value)
				}
				if key == "provider" && value == "" {
					return
				}
				if key == "Outcome" || key == "Summary" || key == "provider" {
					fields["Payload"], _ = json.Marshal(target)
				}
				// Versions are opaque, nonblank strings in the shared contract.
				if key == "ProtocolVersion" && value == `"INVALID"` {
					return
				}
				if key == "Summary" && value == `"INVALID"` {
					return
				}
				if (key == "ProjectID" || key == "TaskID" || key == "MessageID" || key == "CorrelationID") && value == `"INVALID"` {
					return
				}
				data, _ := json.Marshal(fields)
				got, err := DecodeCodexResult(data)
				if err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
					t.Fatalf("accepted %s: %#v %v", data, got, err)
				}
			})
		}
	}
	for _, data := range [][]byte{[]byte("{"), []byte("null"), append(append([]byte{}, base...), []byte(" {}")...), []byte(`{"provider":1}`)} {
		if got, err := DecodeCodexResult(data); err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
			t.Fatal("malformed input accepted")
		}
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(base, &fields)
	fields["MessageType"] = json.RawMessage(`"BOT_RESULT"`)
	data, _ := json.Marshal(fields)
	if _, err := DecodeCodexResult(data); err == nil {
		t.Fatal("wrong message type accepted")
	}
}

func TestCodexResultCodecRejectsInvalidEncode(t *testing.T) {
	for _, change := range []func(*domain.Envelope){
		func(e *domain.Envelope) { e.TaskID = nil },
		func(e *domain.Envelope) { e.ProjectID = "" },
		func(e *domain.Envelope) { e.Payload = CodexResult{Outcome: "INVALID", Summary: "text"} },
		func(e *domain.Envelope) {
			e.Payload = map[string]any{"Outcome": "DONE", "Summary": "text", "provider": "x"}
		},
	} {
		e := canonicalResultForTest()
		change(&e)
		if data, err := EncodeCodexResult(e); err == nil || data != nil {
			t.Fatal("invalid encode accepted")
		}
	}
}

func TestCodexResultCodecPreservesSharedVersionPolicy(t *testing.T) {
	e := canonicalResultForTest()
	e.ProtocolVersion = " custom-version "
	e.ProjectID = " project "
	*e.TaskID = " task "
	data, err := EncodeCodexResult(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCodexResult(data)
	if err != nil || !reflect.DeepEqual(got, e) {
		t.Fatalf("shared envelope policy changed: %#v %v", got, err)
	}
}

func TestCodexResultCodecRejectsProviderMetadata(t *testing.T) {
	for _, key := range []string{"threadId", "turnId", "jsonRpcId", "containerId", "dockerImage", "chatgptAccountId", "backendOrigin", "workspaceRouting", "auth", "token", "provider"} {
		for _, inPayload := range []bool{false, true} {
			data, err := EncodeCodexResult(canonicalResultForTest())
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			if inPayload {
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(fields["Payload"], &payload); err != nil {
					t.Fatal(err)
				}
				payload[key] = json.RawMessage(`"metadata"`)
				fields["Payload"], err = json.Marshal(payload)
			} else {
				fields[key] = json.RawMessage(`"metadata"`)
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := DecodeCodexResult(data); err == nil || !reflect.DeepEqual(got, domain.Envelope{}) {
				t.Fatalf("provider metadata %s accepted", key)
			}
		}
	}
}
