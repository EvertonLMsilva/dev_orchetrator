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
		{CodexTurnStart, `{"turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}`},
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
		{CodexTurnStarted, `{"threadId":"thread-1","turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}`},
		{CodexAgentMessageDelta, `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","delta":"hello"}`},
		{CodexTurnCompleted, `{"threadId":"thread-1","turn":{"id":"turn-1","items":[],"status":"completed","error":null}}`},
		{CodexTurnCompleted, `{"threadId":"thread-1","turn":{"id":"turn-1","items":[],"status":"failed","error":{"message":"fake failure"}}}`},
		{CodexTurnCompleted, `{"threadId":"thread-1","turn":{"id":"turn-1","items":[],"status":"interrupted","error":null}}`},
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
	m, err := DecodeCodexMessage([]byte(`{"id":"request-1","result":{"turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}}`), &CodexResponseExpectation{ID: id, Method: CodexTurnStart})
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
		`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","items":[],"status":"inProgress"}}}`,
		`{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","items":[],"status":"unknown"}}}`,
		`{"method":"turn/started","params":{"threadId":"t","turn":{"id":"u","items":[],"status":"completed"}}}`,
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

// Inventory from the JSON/TS schemas generated by codex-cli 0.159.2. Required
// fields follow JSON Schema: reasoning arrays and command source have defaults.
func normalCodexItems() []struct {
	name, wire string
	required   []string
} {
	return []struct {
		name, wire string
		required   []string
	}{
		{"userMessage", `{"type":"userMessage","id":"item-1","clientId":null,"content":[{"type":"text","text":"task","text_elements":[{"byteRange":{"start":0,"end":4},"placeholder":null}]}]}`, []string{"content"}},
		{"agentMessage", `{"type":"agentMessage","id":"item-1","text":"BLOCKED; $(command)","phase":null,"delivery":null,"memoryCitation":null,"questions":null}`, []string{"text"}},
		{"plan", `{"type":"plan","id":"item-1","text":"BLOCKED; $(command)"}`, []string{"text"}},
		{"reasoning", `{"type":"reasoning","id":"item-1","summary":["summary"],"content":["data"]}`, nil},
		{"commandExecution", `{"type":"commandExecution","id":"item-1","command":"go test ./...","cwd":"/workspace","source":"agent","status":"completed","commandActions":[{"type":"read","command":"cat file","name":"file","path":"/workspace/file"},{"type":"listFiles","command":"ls","path":null},{"type":"search","command":"rg text","query":"text","path":null},{"type":"unknown","command":"go test ./..."}],"pluginId":null,"scriptPath":null,"processId":null,"aggregatedOutput":"BLOCKED","exitCode":0,"durationMs":12}`, []string{"command", "cwd", "status", "commandActions"}},
		{"fileChange", `{"type":"fileChange","id":"item-1","status":"completed","changes":[{"path":"a","kind":{"type":"add"},"diff":"data"},{"path":"b","kind":{"type":"delete"},"diff":"data"},{"path":"c","kind":{"type":"update","move_path":null},"diff":"data"}]}`, []string{"status", "changes"}},
		{"imageView", `{"type":"imageView","id":"item-1","path":"/workspace/image.png"}`, []string{"path"}},
		{"sleep", `{"type":"sleep","id":"item-1","durationMs":0}`, []string{"durationMs"}},
		{"contextCompaction", `{"type":"contextCompaction","id":"item-1"}`, nil},
	}
}

func officialItemEvent(method, item string) string {
	timestamp := "startedAtMs"
	if method == "item/completed" {
		timestamp = "completedAtMs"
	}
	return `{"method":"` + method + `","params":{"threadId":"thread-1","turnId":"turn-1","` + timestamp + `":1234,"item":` + item + `},"emittedAtMs":1235}`
}

func TestCodexNormalThreadItemStrictContracts(t *testing.T) {
	for _, item := range normalCodexItems() {
		for _, method := range []string{"item/started", "item/completed"} {
			t.Run(item.name+"/"+method, func(t *testing.T) {
				base := officialItemEvent(method, item.wire)
				message, err := DecodeCodexMessage([]byte(base), nil)
				if err != nil {
					t.Fatal("official item rejected", err)
				}
				var event CodexItemNotification
				if method == "item/started" {
					event = message.Notification.ItemStarted.CodexItemNotification
				} else {
					event = message.Notification.ItemCompleted.CodexItemNotification
				}
				if event.Item.ID != "item-1" || event.ThreadID != "thread-1" || event.TurnID != "turn-1" {
					t.Fatal("correlation lost")
				}
				if item.name != "agentMessage" && event.Item.Text != "" {
					t.Fatal("lifecycle item became output")
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal([]byte(item.wire), &fields); err != nil {
					t.Fatal(err)
				}
				for _, key := range append([]string{"type", "id"}, item.required...) {
					for _, replacement := range []string{"", "null", "true", "{}"} {
						original := fields[key]
						if replacement == "" {
							delete(fields, key)
						} else {
							fields[key] = json.RawMessage(replacement)
						}
						bad, _ := json.Marshal(fields)
						if _, err := DecodeCodexMessage([]byte(officialItemEvent(method, string(bad))), nil); err == nil {
							t.Fatalf("accepted invalid required field %s=%s", key, replacement)
						}
						fields[key] = original
					}
				}
				fields["unknown"] = json.RawMessage(`true`)
				bad, _ := json.Marshal(fields)
				if _, err := DecodeCodexMessage([]byte(officialItemEvent(method, string(bad))), nil); err == nil {
					t.Fatal("unknown item field accepted")
				}
				if _, err := DecodeCodexMessage([]byte(strings.Replace(base, `"item":`, `"unknown":true,"item":`, 1)), nil); err == nil {
					t.Fatal("unknown params field accepted")
				}
			})
		}
	}
}

func TestCodexNotificationNestedStrictness(t *testing.T) {
	for i, input := range []string{
		strings.Replace(turnEvent("completed"), `"threadId":`, `"unknown":true,"threadId":`, 1),
		strings.Replace(turnEvent("completed"), `"status":`, `"unknown":true,"status":`, 1),
		strings.Replace(turnEvent("failed"), `"error":null`, `"error":{"message":"failure","unknown":true}`, 1),
		strings.Replace(turnDelta("x"), `"delta":`, `"unknown":true,"delta":`, 1),
		strings.Replace(officialItemEvent("item/started", normalCodexItems()[0].wire), `"start":0`, `"start":0,"unknown":true`, 1),
		strings.Replace(officialItemEvent("item/completed", normalCodexItems()[4].wire), `"name":"file"`, `"name":"file","unknown":true`, 1),
		strings.Replace(officialItemEvent("item/completed", normalCodexItems()[5].wire), `"move_path":null`, `"move_path":null,"unknown":true`, 1),
		turnEvent("completed") + ` {}`,
	} {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			if _, err := DecodeCodexMessage([]byte(input), nil); err == nil {
				t.Fatalf("accepted nested/trailing field: %s", input)
			}
		})
	}
}

func TestCodexUnsupportedThreadItemsFailClosed(t *testing.T) {
	// Feature-specific capabilities are never authorized by merely receiving data.
	for _, variant := range []string{"hookPrompt", "functionCallOutput", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageGeneration", "enteredReviewMode", "exitedReviewMode", "unknown"} {
		for _, method := range []string{"item/started", "item/completed"} {
			if _, err := DecodeCodexMessage([]byte(officialItemEvent(method, `{"type":"`+variant+`","id":"item-1"}`)), nil); err == nil {
				t.Fatalf("unsupported %s accepted", variant)
			}
		}
	}
}

func TestCodexOfficialDefaultsAndNestedTypes(t *testing.T) {
	for _, item := range []string{
		`{"type":"reasoning","id":"item-1"}`,
		`{"type":"userMessage","id":"item-1","content":[{"type":"text","text":""}]}`,
		`{"type":"commandExecution","id":"item-1","command":"","cwd":"","status":"inProgress","commandActions":[]}`,
		`{"type":"fileChange","id":"item-1","status":"inProgress","changes":[]}`,
	} {
		if _, err := DecodeCodexMessage([]byte(officialItemEvent("item/started", item)), nil); err != nil {
			t.Fatal("official default rejected", err)
		}
	}
	for _, item := range []string{
		`{"type":"reasoning","id":"item-1","summary":null}`,
		`{"type":"reasoning","id":"item-1","content":[null]}`,
		`{"type":"userMessage","id":"item-1","content":[null]}`,
		`{"type":"userMessage","id":"item-1","content":[{"type":"text","text":"","text_elements":null}]}`,
		`{"type":"userMessage","id":"item-1","content":[{"type":"text","text":"","text_elements":[{"byteRange":{"end":1}}]}]}`,
		`{"type":"userMessage","id":"item-1","content":[{"type":"skill","name":"skill","path":"/workspace/skill"}]}`,
		`{"type":"commandExecution","id":"item-1","command":"","cwd":"","status":"unknown","commandActions":[]}`,
		`{"type":"commandExecution","id":"item-1","command":"","cwd":"","status":"completed","source":null,"commandActions":[]}`,
		`{"type":"commandExecution","id":"item-1","command":"","cwd":"","status":"completed","commandActions":[null]}`,
		`{"type":"commandExecution","id":"item-1","command":"","cwd":"","status":"completed","commandActions":[{"type":"read","command":"x","name":"x"}]}`,
		`{"type":"commandExecution","id":"item-1","command":"","cwd":"","status":"completed","commandActions":[{"type":"unknown","command":"x","path":null}]}`,
		`{"type":"fileChange","id":"item-1","status":"completed","changes":[null]}`,
		`{"type":"fileChange","id":"item-1","status":"completed","changes":[{"path":"a","diff":"x","kind":{"type":"unknown"}}]}`,
		`{"type":"sleep","id":"item-1","durationMs":-1}`,
		`{"type":"sleep","id":"item-1","durationMs":"1"}`,
	} {
		if _, err := DecodeCodexMessage([]byte(officialItemEvent("item/started", item)), nil); err == nil {
			t.Fatalf("invalid nested/default type accepted: %s", item)
		}
	}
}

func TestCodexStrictTerminalSnapshotsAndErrors(t *testing.T) {
	for _, item := range normalCodexItems() {
		terminal := strings.Replace(turnEvent("completed"), `"items":[]`, `"items":[`+item.wire+`]`, 1)
		if _, err := DecodeCodexMessage([]byte(terminal), nil); err != nil {
			t.Fatal("official terminal snapshot rejected", err)
		}
		if _, err := DecodeCodexMessage([]byte(strings.Replace(terminal, `"type":`, `"unknown":true,"type":`, 1)), nil); err == nil {
			t.Fatal("unknown snapshot item field accepted")
		}
	}
	for _, change := range [][2]string{
		{`"items":[],`, ""}, {`"items":[]`, `"items":null`}, {`"items":[]`, `"items":[null]`},
		{`"items":[]`, `"items":[],"itemsView":null`}, {`"items":[]`, `"items":[],"itemsView":"unknown"`},
		{`"items":[]`, `"items":[],"durationMs":"1"`},
	} {
		if _, err := DecodeCodexMessage([]byte(strings.Replace(turnEvent("completed"), change[0], change[1], 1)), nil); err == nil {
			t.Fatal("invalid terminal fields accepted", change)
		}
	}
	for _, info := range []string{
		`"usageLimitExceeded"`, `{"httpConnectionFailed":{"httpStatusCode":401}}`,
		`{"responseStreamDisconnected":{"httpStatusCode":null}}`, `{"activeTurnNotSteerable":{"turnKind":"compact"}}`,
	} {
		errData := `{"message":"failure","codexErrorInfo":` + info + `,"additionalDetails":null,"misalignment":{"errorType":null,"detailedExplanation":null,"steer":{"message":"data only"}}}`
		if _, err := DecodeCodexMessage([]byte(strings.Replace(turnEvent("failed"), `"error":null`, `"error":`+errData, 1)), nil); err != nil {
			t.Fatal("official error rejected", err)
		}
	}
	for _, info := range []string{
		`"unknown"`, `{"httpConnectionFailed":{"unknown":true}}`,
		`{"httpConnectionFailed":{},"activeTurnNotSteerable":null}`,
		`{"activeTurnNotSteerable":{}}`, `{"activeTurnNotSteerable":{"turnKind":"unknown"}}`,
	} {
		errData := `{"message":"failure","codexErrorInfo":` + info + `}`
		if _, err := DecodeCodexMessage([]byte(strings.Replace(turnEvent("failed"), `"error":null`, `"error":`+errData, 1)), nil); err == nil {
			t.Fatal("invalid nested error accepted", info)
		}
	}
}
