package infrastructure

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const turnResponse = `{"id":3,"result":{"turn":{"id":"turn-1","status":"inProgress","error":null}}}`

func turnEvent(status string) string {
	return `{"method":"turn/completed","params":{"threadId":"thread-1","turn":{"id":"turn-1","status":"` + status + `","error":null}}}`
}
func turnDelta(text string) string {
	b, _ := json.Marshal(text)
	return `{"method":"item/agentMessage/delta","params":{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":` + string(b) + `}}`
}
func turnThread(t *testing.T, f *threadFake) CodexThreadID {
	t.Helper()
	f.messages = append([]string{handshakeResponse, threadResponse}, f.messages...)
	s, err := completeCodexHandshake(f)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.StartThread()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func TestCodexTurnBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		status CodexTurnStatus
		text   string
		fail   bool
	}{
		{"completed", []string{turnResponse, turnDelta("hello"), threadNotification, turnDelta(" world"), turnEvent("completed")}, CodexTurnCompletedStatus, "hello world", false},
		{"failed", []string{turnResponse, turnEvent("failed")}, CodexTurnFailed, "", false},
		{"interrupted", []string{turnResponse, turnEvent("interrupted")}, CodexTurnInterrupted, "", false},
		{"notification_handling", []string{threadNotification, turnResponse, threadNotification, turnEvent("completed")}, CodexTurnCompletedStatus, "", false},
		{"wrong_id", []string{strings.Replace(turnResponse, `"id":3`, `"id":4`, 1)}, "", "", true},
		{"wrong_id_type", []string{strings.Replace(turnResponse, `"id":3`, `"id":"3"`, 1)}, "", "", true},
		{"rpc_error", []string{`{"id":3,"error":{"code":-1,"message":"rejected"}}`}, "", "", true},
		{"malformed_response", []string{`{"id":3,"result":{}}`}, "", "", true},
		{"malformed_json", []string{`{`}, "", "", true},
		{"invalid_turn_id", []string{strings.Replace(turnResponse, `"turn-1"`, `""`, 1)}, "", "", true},
		{"missing_turn_id", []string{`{"id":3,"result":{"turn":{"status":"inProgress"}}}`}, "", "", true},
		{"wrong_turn", []string{turnResponse, strings.Replace(turnEvent("completed"), "turn-1", "other", 1)}, "", "", true},
		{"wrong_thread", []string{turnResponse, strings.Replace(turnEvent("completed"), "thread-1", "other", 1)}, "", "", true},
		{"wrong_delta", []string{turnResponse, strings.Replace(turnDelta("x"), "turn-1", "other", 1)}, "", "", true},
		{"wrong_delta_thread", []string{turnResponse, strings.Replace(turnDelta("x"), "thread-1", "other", 1)}, "", "", true},
		{"eof_before_terminal", []string{turnResponse, turnDelta("partial")}, "", "", true},
		{"unsupported_terminal", []string{turnResponse, turnEvent("unknown")}, "", "", true},
		{"terminal_before_response", []string{turnEvent("completed"), turnResponse}, "", "", true},
		{"delta_before_response", []string{turnDelta("x"), turnResponse}, "", "", true},
		{"output_limit", []string{turnResponse, turnDelta(strings.Repeat("a", codexTurnTextLimit)), turnDelta("x"), turnEvent("completed")}, "", "", true},
		{"output_at_limit", []string{turnResponse, turnDelta(strings.Repeat("a", codexTurnTextLimit)), turnEvent("completed")}, CodexTurnCompletedStatus, strings.Repeat("a", codexTurnTextLimit), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &threadFake{messages: tc.events}
			thread := turnThread(t, f)
			result, err := thread.StartTurn("fake task")
			if (err != nil) != tc.fail {
				t.Fatalf("error: %v", err)
			}
			if tc.fail && result != (CodexTurnResult{}) {
				t.Fatal("partial result on failure")
			}
			if !tc.fail && (result.Status != tc.status || result.Text != tc.text || result.TurnID.value != "turn-1") {
				t.Fatal("result mismatch")
			}
			if tc.name == "rpc_error" {
				var rpc *CodexRPCErrorResponse
				if !errors.As(err, &rpc) {
					t.Fatal("lost RPC error")
				}
			}
			if len(f.writes) != 4 || string(f.writes[3]) != `{"id":3,"method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"fake task","text_elements":[]}]}}` {
				t.Fatal("encode/order")
			}
			if _, err := thread.StartTurn("again"); err == nil || len(f.writes) != 4 {
				t.Fatal("reused capability")
			}
		})
	}
}
func TestCodexTurnPrerequisite(t *testing.T) {
	for _, id := range []CodexThreadID{{}, {value: "arbitrary"}} {
		if _, err := id.StartTurn("text"); err == nil {
			t.Fatal("untrusted thread accepted")
		}
	}
}
func TestCodexTurnTransportFailure(t *testing.T) {
	for _, f := range []*threadFake{{writeFailure: 4}, {readErr: errors.New("read failure")}} {
		thread := turnThread(t, f)
		if result, err := thread.StartTurn("text"); err == nil || result != (CodexTurnResult{}) {
			t.Fatal("transport failure accepted")
		}
	}
}

func TestCodexTurnCapabilityCopyAndEventLimit(t *testing.T) {
	f := &threadFake{messages: []string{turnResponse, turnEvent("completed")}}
	thread := turnThread(t, f)
	copy := thread
	if _, err := thread.StartTurn("text"); err != nil {
		t.Fatal(err)
	}
	if _, err := copy.StartTurn("text"); err == nil || len(f.writes) != 4 {
		t.Fatal("copied capability reused")
	}
	f = &threadFake{messages: append([]string{turnResponse}, makeNotifications(4096)...)}
	thread = turnThread(t, f)
	if _, err := thread.StartTurn("text"); err == nil {
		t.Fatal("unbounded event traffic")
	}
}

func TestCodexTurnFailedDetail(t *testing.T) {
	event := strings.Replace(turnEvent("failed"), `"error":null`, `"error":{"message":"provider failed"}`, 1)
	f := &threadFake{messages: []string{turnResponse, event}}
	thread := turnThread(t, f)
	result, err := thread.StartTurn("text")
	if err != nil || result.Error == nil || result.Error.Message != "provider failed" || result.Status != CodexTurnFailed {
		t.Fatal("lost terminal failure data")
	}
}
