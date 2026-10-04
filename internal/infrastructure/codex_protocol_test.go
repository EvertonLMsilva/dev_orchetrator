package infrastructure

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCodexProtocolEncode(t *testing.T) {
	id := CodexIntegerID(7)
	for _, tc := range []struct {
		name   string
		encode func() ([]byte, error)
		want   string
	}{
		{"initialize", func() ([]byte, error) {
			return EncodeCodexInitialize(id, CodexClientInfo{Name: "orchestrator", Version: "test"})
		}, `{"id":7,"method":"initialize","params":{"clientInfo":{"name":"orchestrator","version":"test"}}}`},
		{"initialized", func() ([]byte, error) { return EncodeCodexInitialized() }, `{"method":"initialized"}`},
		{"thread_start", func() ([]byte, error) { return EncodeCodexThreadStart(CodexStringID("thread-request")) }, `{"id":"thread-request","method":"thread/start","params":{"cwd":"/workspace"}}`},
		{"turn_start", func() ([]byte, error) { return EncodeCodexTurnStart(id, "thread-1", "fake task") }, `{"id":7,"method":"turn/start","params":{"threadId":"thread-1","input":[{"type":"text","text":"fake task","text_elements":[]}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.encode()
			if err != nil {
				t.Fatal(err)
			}
			// Compact bytes also verify nested fields and absence of routing/config.
			if string(got) != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

// Fixtures follow rust-v0.159.2 ServerNotificationEnvelope and ThreadItem.
func officialAgentItemEvent(method, text string) string {
	encoded, _ := json.Marshal(text)
	timestamp := "startedAtMs"
	if method == "item/completed" {
		timestamp = "completedAtMs"
	}
	return `{"method":"` + method + `","params":{"threadId":"thread-1","turnId":"turn-1","` + timestamp + `":1234,"item":{"type":"agentMessage","id":"item-1","text":` + string(encoded) + `,"phase":"final_answer","memoryCitation":null,"delivery":null,"questions":null}},"emittedAtMs":1235}`
}

func TestCodexOfficialNotificationEnvelope(t *testing.T) {
	base := `{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"u","itemId":"i","delta":"hello"},"emittedAtMs":1235}`
	for _, timestamp := range []string{"1235", "null", "-1", "9223372036854775807"} {
		message, err := DecodeCodexMessage([]byte(strings.Replace(base, "1235", timestamp, 1)), nil)
		if err != nil {
			t.Fatalf("official timestamp %s: %v", timestamp, err)
		}
		if timestamp == "1235" && (message.Notification.EmittedAtMs == nil || *message.Notification.EmittedAtMs != 1235) {
			t.Fatal("timestamp lost")
		}
		if timestamp == "null" && message.Notification.EmittedAtMs != nil {
			t.Fatal("null timestamp changed")
		}
	}
	if _, err := DecodeCodexMessage([]byte(strings.Replace(base, `,"emittedAtMs":1235`, "", 1)), nil); err != nil {
		t.Fatal("optional timestamp rejected", err)
	}
	for _, timestamp := range []string{`"1235"`, "true", "1.5", "[]", "{}", "9223372036854775808"} {
		if _, err := DecodeCodexMessage([]byte(strings.Replace(base, "1235", timestamp, 1)), nil); err == nil {
			t.Fatalf("malformed timestamp %s accepted", timestamp)
		}
	}
	if _, err := DecodeCodexMessage([]byte(strings.Replace(base, `"emittedAtMs":1235`, `"emittedAtMs":1235,"unknown":1`, 1)), nil); err == nil {
		t.Fatal("unknown envelope field accepted")
	}
	for _, timestamp := range []string{"1235", "null"} {
		response := strings.TrimSuffix(handshakeResponse, "}") + `,"emittedAtMs":` + timestamp + `}`
		if _, err := DecodeCodexMessage([]byte(response), &CodexResponseExpectation{ID: CodexIntegerID(1), Method: CodexInitialize}); err == nil {
			t.Fatal("notification timestamp accepted on RPC response")
		}
	}
}

func TestCodexOfficialItemNotifications(t *testing.T) {
	for _, method := range []string{"item/started", "item/completed"} {
		base := officialAgentItemEvent(method, "BLOCKED; $(command)")
		message, err := DecodeCodexMessage([]byte(base), nil)
		if err != nil || message.Notification == nil || message.Notification.Method != CodexMethod(method) || message.Response != nil {
			t.Fatalf("official %s: %+v %v", method, message, err)
		}
		var event CodexItemNotification
		if method == "item/started" {
			event = message.Notification.ItemStarted.CodexItemNotification
			if *message.Notification.ItemStarted.StartedAtMs != 1234 {
				t.Fatal("start timestamp lost")
			}
		} else {
			event = message.Notification.ItemCompleted.CodexItemNotification
			if *message.Notification.ItemCompleted.CompletedAtMs != 1234 {
				t.Fatal("completion timestamp lost")
			}
		}
		if event.ThreadID != "thread-1" || event.TurnID != "turn-1" || event.Item.ID != "item-1" || event.Item.Text != "BLOCKED; $(command)" {
			t.Fatal("typed item projection changed")
		}
		for _, change := range [][2]string{
			{`"threadId":"thread-1",`, ""}, {`"turnId":"turn-1",`, ""}, {`"id":"item-1",`, ""},
			{`"text":"BLOCKED; $(command)",`, ""}, {`"type":"agentMessage",`, ""},
			{`"threadId":"thread-1"`, `"threadId":123`}, {`"turnId":"turn-1"`, `"turnId":null`},
			{`"id":"item-1"`, `"id":""`}, {`"text":"BLOCKED; $(command)"`, `"text":null`},
			{`"type":"agentMessage"`, `"type":"unknown"`}, {`"phase":"final_answer"`, `"phase":"unknown"`},
			{`"delivery":null`, `"delivery":"unknown"`}, {`"questions":null`, `"questions":true`},
			{`"memoryCitation":null`, `"memoryCitation":[]`},
			{`"memoryCitation":null`, `"memoryCitation":{"entries":[],"threadIds":[null]}`},
			{`"memoryCitation":null`, `"memoryCitation":{"entries":[{}],"threadIds":[]}`},
			{`"memoryCitation":null`, `"memoryCitation":{"entries":[],"threadIds":[],"unknown":1}`},
			{`"questions":null`, `"questions":[{"title":"question","options":[null]}]`},
			{`"questions":null`, `"questions":[{"options":null}]`},
			{`"questions":null`, `"questions":[{"title":"question","unknown":1}]`},
			{`"item":{`, `"unknown":1,"item":{`}, {`"type":"agentMessage"`, `"unknown":1,"type":"agentMessage"`},
			{`AtMs":1234`, `AtMs":"1234"`}, {`AtMs":1234`, `AtMs":null`}, {`,"startedAtMs":1234`, ""}, {`,"completedAtMs":1234`, ""},
		} {
			input := strings.Replace(base, change[0], change[1], 1)
			if input == base {
				continue
			}
			if _, err := DecodeCodexMessage([]byte(input), nil); err == nil {
				t.Fatalf("malformed item accepted: %s", input)
			}
		}
		metadata := strings.Replace(base, `"memoryCitation":null`, `"memoryCitation":{"entries":[{"path":"data only","lineStart":1,"lineEnd":2,"note":"citation"}],"threadIds":["thread"]}`, 1)
		metadata = strings.Replace(metadata, `"questions":null`, `"questions":[{"title":"question","options":["choice"]}]`, 1)
		metadata = strings.Replace(metadata, `"delivery":null`, `"delivery":"async"`, 1)
		if _, err := DecodeCodexMessage([]byte(metadata), nil); err != nil {
			t.Fatal("official ancillary data rejected", err)
		}
	}
}

func TestCodexProtocolResponses(t *testing.T) {
	for _, tc := range []struct {
		method CodexMethod
		result string
	}{
		{CodexInitialize, `{"userAgent":"fake","codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"linux"}`},
		{CodexThreadStart, `{"thread":{"id":"thread-1","cwd":"/workspace"},"cwd":"/workspace","model":"fake"}`},
		{CodexTurnStart, `{"turn":{"id":"turn-1","status":"inProgress","items":[],"error":null}}`},
	} {
		t.Run(string(tc.method), func(t *testing.T) {
			m, err := DecodeCodexMessage([]byte(`{"id":7,"result":`+tc.result+`}`), &CodexResponseExpectation{ID: CodexIntegerID(7), Method: tc.method})
			if err != nil {
				t.Fatal(err)
			}
			if m.Response == nil || m.Notification != nil || m.Response.ID != CodexIntegerID(7) || m.Response.Method != tc.method {
				t.Fatalf("wrong response: %+v", m)
			}
			switch tc.method {
			case CodexInitialize:
				if m.Response.Initialize.UserAgent != "fake" {
					t.Fatal("initialize not decoded")
				}
			case CodexThreadStart:
				if m.Response.ThreadStart.Thread.ID != "thread-1" {
					t.Fatal("thread not decoded")
				}
			case CodexTurnStart:
				if m.Response.TurnStart.Turn.Status != CodexTurnInProgress {
					t.Fatal("turn not decoded")
				}
			}
		})
	}
}

func TestCodexProtocolNotifications(t *testing.T) {
	for _, tc := range []struct {
		method CodexMethod
		params string
	}{
		{CodexThreadStarted, `{"thread":{"id":"thread-1","cwd":"/workspace"}}`},
		{CodexTurnStarted, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"inProgress","error":null}}`},
		{CodexAgentMessageDelta, `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"hello"}`},
		{CodexTurnCompleted, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"completed","error":null}}`},
		{CodexTurnCompleted, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":"fake failure"}}}`},
		{CodexTurnCompleted, `{"threadId":"thread-1","turn":{"id":"turn-1","status":"interrupted","error":null}}`},
	} {
		t.Run(string(tc.method)+tc.params, func(t *testing.T) {
			m, err := DecodeCodexMessage([]byte(`{"method":"`+string(tc.method)+`","params":`+tc.params+`}`), nil)
			if err != nil {
				t.Fatal(err)
			}
			if m.Response != nil || m.Notification == nil || m.Notification.Method != tc.method {
				t.Fatal("notification misclassified")
			}
			if tc.method == CodexAgentMessageDelta && m.Notification.AgentMessageDelta.Delta != "hello" {
				t.Fatal("lost delta")
			}
			if tc.method == CodexTurnCompleted {
				turn := m.Notification.Turn.Turn
				var want CodexTurnNotification
				if err := json.Unmarshal([]byte(tc.params), &want); err != nil {
					t.Fatal(err)
				}
				if turn.ID != "turn-1" || turn.Status != want.Turn.Status || m.Notification.Turn.ThreadID != "thread-1" {
					t.Fatal("lost terminal correlation/status")
				}
				if turn.Status == CodexTurnFailed && (turn.Error == nil || turn.Error.Message != "fake failure") {
					t.Fatal("lost failure")
				}
			}
		})
	}
}

func TestCodexProtocolStringResponseIDAndEmptyDelta(t *testing.T) {
	id := CodexStringID("request-1")
	m, err := DecodeCodexMessage([]byte(`{"id":"request-1","result":{"turn":{"id":"turn-1","status":"inProgress","error":null,"items":[]}}}`), &CodexResponseExpectation{ID: id, Method: CodexTurnStart})
	if err != nil || m.Response == nil || m.Response.ID != id {
		t.Fatalf("string response ID: %v", err)
	}
	m, err = DecodeCodexMessage([]byte(`{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"u","itemId":"i","delta":""}}`), nil)
	if err != nil || m.Notification == nil || m.Notification.AgentMessageDelta.Delta != "" {
		t.Fatalf("valid empty delta: %v", err)
	}
}

func TestCodexProtocolRPCError(t *testing.T) {
	m, err := DecodeCodexMessage([]byte(`{"id":"req","error":{"code":-32600,"message":"provider failure","data":{"detail":"fake"}}}`), &CodexResponseExpectation{ID: CodexStringID("req"), Method: CodexInitialize})
	var rpcErr *CodexRPCErrorResponse
	if !errors.As(err, &rpcErr) || rpcErr.ID != CodexStringID("req") || rpcErr.ErrorDetail.Code != -32600 || rpcErr.ErrorDetail.Message != "provider failure" || m.Response != nil {
		t.Fatalf("wrong provider error: %v", err)
	}
}

func TestCodexProtocolRejectsUnsupportedAndMalformed(t *testing.T) {
	expected := &CodexResponseExpectation{ID: CodexIntegerID(7), Method: CodexInitialize}
	for _, input := range []string{
		`{`, `null`, `[]`, `{} `, `{"id":7,"result":{}} {}`,
		`{"id":7,"result":null}`, `{"id":8,"result":{}}`, `{"id":"7","result":{}}`, `{"id":7.5,"result":{}}`, `{"id":null,"result":{}}`,
		`{"id":7,"result":{},"error":{"code":1,"message":"bad"}}`,
		`{"id":7,"method":"initialize","params":{}}`,
		`{"id":7,"error":{"message":"missing code"}}`, `{"id":7,"error":{"code":1}}`,
		`{"id":7,"result":{"userAgent":"fake"}}`,
		`{"method":"unknown","params":{}}`, `{"method":"turn/failed","params":{}}`,
		`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"inProgress"}}}`,
		`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"unknown"}}}`,
		`{"method":"turn/started","params":{"threadId":"t","turn":{"id":"u","status":"completed"}}}`,
		`{"method":"item/agentMessage/delta","params":{"threadId":"t","turnId":"u","itemId":"i"}}`,
		`{"method":"item/agentMessage/delta","params":null}`,
		`{"method":"thread/started","params":{"thread":{"id":"t","cwd":"/host"}}}`,
	} {
		t.Run(input, func(t *testing.T) {
			if _, err := DecodeCodexMessage([]byte(input), expected); err == nil {
				t.Fatal("accepted unsupported/malformed protocol")
			}
		})
	}
	for _, method := range []CodexMethod{CodexThreadStart, CodexTurnStart, "unknown"} {
		if _, err := DecodeCodexMessage([]byte(`{"id":7,"result":{}}`), &CodexResponseExpectation{ID: CodexIntegerID(7), Method: method}); err == nil {
			t.Fatal("accepted invalid response")
		}
	}
	if _, err := DecodeCodexMessage([]byte(`{"id":7,"result":{}}`), nil); err == nil {
		t.Fatal("uncorrelated response")
	}
}

func TestCodexProtocolIDsAndInvalidRequests(t *testing.T) {
	for _, raw := range []string{`"request"`, `0`, `-1`, `9223372036854775807`, `-9223372036854775808`} {
		var id CodexRequestID
		if err := json.Unmarshal([]byte(raw), &id); err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(id)
		if err != nil || string(got) != raw {
			t.Fatalf("ID lost: %s %v", got, err)
		}
	}
	for _, raw := range []string{`null`, `true`, `{}`, `1.0`, `9223372036854775808`} {
		var id CodexRequestID
		if json.Unmarshal([]byte(raw), &id) == nil {
			t.Fatalf("accepted ID %s", raw)
		}
	}
	if _, err := EncodeCodexThreadStart(CodexRequestID{}); err == nil {
		t.Fatal("missing ID")
	}
	if _, err := EncodeCodexInitialize(CodexIntegerID(0), CodexClientInfo{}); err == nil {
		t.Fatal("missing client")
	}
	if _, err := EncodeCodexTurnStart(CodexIntegerID(0), "", "fake"); err == nil {
		t.Fatal("missing thread")
	}
}
