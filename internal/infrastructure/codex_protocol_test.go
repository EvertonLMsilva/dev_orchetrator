package infrastructure

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Full required fields and optional metadata from the pinned 0.159.2 schema.
const officialThreadJSON = `{"id":"thread-1","sessionId":"session-1","projectId":null,"preview":"","ephemeral":false,"modelProvider":"openai","createdAt":1,"updatedAt":2,"status":{"type":"idle"},"cwd":"/workspace","cliVersion":"0.159.2","source":"appServer","turns":[]}`
const officialThreadStartJSON = `{"thread":` + officialThreadJSON + `,"model":"fake","modelProvider":"openai","cwd":"/workspace","approvalPolicy":"never","approvalsReviewer":"user","sandbox":{"type":"dangerFullAccess"}}`

func TestCodexRemainingStrictDecoding(t *testing.T) {
	initialize := `{"id":1,"result":{"userAgent":"fake","codexHome":"/run/codex-process","platformFamily":"unix","platformOs":"linux"}}`
	threadStart := `{"id":2,"result":` + officialThreadStartJSON + `}`
	threadStarted := `{"method":"thread/started","params":{"thread":` + officialThreadJSON + `}}`
	turnStart := `{"id":3,"result":{"turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}}`
	rpc := `{"id":1,"error":{"code":-1,"message":"failure"}}`
	for _, tc := range []struct {
		name, input string
		method      CodexMethod
		id          int64
	}{
		{"initialize_unknown", strings.Replace(initialize, `"userAgent":`, `"unknown":true,"userAgent":`, 1), CodexInitialize, 1},
		{"thread_start_unknown", strings.Replace(threadStart, `"result":{`, `"result":{"unknown":true,`, 1), CodexThreadStart, 2},
		{"thread_start_nested_unknown", strings.Replace(threadStart, `"sessionId":`, `"unknown":true,"sessionId":`, 1), CodexThreadStart, 2},
		{"turn_start_wrapper_unknown", strings.Replace(turnStart, `"result":{`, `"result":{"unknown":true,`, 1), CodexTurnStart, 3},
		{"turn_start_nested_unknown", strings.Replace(turnStart, `"items":`, `"unknown":true,"items":`, 1), CodexTurnStart, 3},
		{"thread_started_params_unknown", strings.Replace(threadStarted, `"params":{`, `"params":{"unknown":true,`, 1), "", 0},
		{"thread_started_thread_unknown", strings.Replace(threadStarted, `"sessionId":`, `"unknown":true,"sessionId":`, 1), "", 0},
		{"rpc_unknown", strings.Replace(rpc, `"code":`, `"unknown":"credential-secret","code":`, 1), CodexInitialize, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var expected *CodexResponseExpectation
			if tc.method != "" {
				expected = &CodexResponseExpectation{ID: CodexIntegerID(tc.id), Method: tc.method}
			}
			if _, err := DecodeCodexMessage([]byte(tc.input), expected); err == nil {
				t.Fatal("unknown field accepted")
			} else {
				var rpc *CodexRPCErrorResponse
				if tc.name == "rpc_unknown" && errors.As(err, &rpc) {
					t.Fatal("unknown field accepted as RPC error")
				}
				if strings.Contains(err.Error(), "credential-secret") {
					t.Fatal("error data leaked")
				}
			}
		})
	}
}

func TestCodexStrictRequiredResponseFields(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		method        CodexMethod
		keys          []string
	}{
		{"initialize", `{"userAgent":"fake","codexHome":"/run/codex-process","platformFamily":"unix","platformOs":"linux"}`, CodexInitialize, []string{"userAgent", "codexHome", "platformFamily", "platformOs"}},
		{"thread_start", officialThreadStartJSON, CodexThreadStart, []string{"thread", "model", "modelProvider", "cwd", "approvalPolicy", "approvalsReviewer", "sandbox"}},
		{"turn_start", `{"turn":{"id":"turn-1","items":[],"status":"inProgress","error":null}}`, CodexTurnStart, []string{"turn"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := &CodexResponseExpectation{ID: CodexIntegerID(7), Method: tc.method}
			wrap := func(payload string) []byte { return []byte(`{"id":7,"result":` + payload + `}`) }
			if _, err := DecodeCodexMessage(wrap(tc.payload), expected); err != nil {
				t.Fatal("valid official response rejected", err)
			}
			var fields map[string]json.RawMessage
			if err := decodeCodexStrictObject([]byte(tc.payload), &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.keys {
				original := fields[key]
				for _, replacement := range []string{"", "null", "123", "[]"} {
					if replacement == "" {
						delete(fields, key)
					} else {
						fields[key] = json.RawMessage(replacement)
					}
					payload, _ := json.Marshal(fields)
					if _, err := DecodeCodexMessage(wrap(string(payload)), expected); err == nil {
						t.Fatalf("accepted invalid required field %s=%s", key, replacement)
					}
				}
				fields[key] = original
			}
			for _, bad := range []string{`null`, `[]`, `{`, tc.payload + ` {}`} {
				if _, err := DecodeCodexMessage(wrap(bad), expected); err == nil {
					t.Fatal("malformed/trailing result accepted")
				}
			}
		})
	}
	for _, notification := range []bool{false, true} {
		var fields map[string]json.RawMessage
		if err := decodeCodexStrictObject([]byte(officialThreadJSON), &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"id", "sessionId", "projectId", "preview", "ephemeral", "modelProvider", "createdAt", "updatedAt", "status", "cwd", "cliVersion", "source", "turns"} {
			original := fields[key]
			for _, replacement := range []string{"", "null", "true", "{}"} {
				if (key == "projectId" && replacement == "null") || (key == "ephemeral" && replacement == "true") {
					continue
				}
				if replacement == "" {
					delete(fields, key)
				} else {
					fields[key] = json.RawMessage(replacement)
				}
				thread, _ := json.Marshal(fields)
				input := strings.Replace(officialThreadStartJSON, officialThreadJSON, string(thread), 1)
				expected := &CodexResponseExpectation{ID: CodexIntegerID(2), Method: CodexThreadStart}
				input = `{"id":2,"result":` + input + `}`
				if notification {
					input = `{"method":"thread/started","params":{"thread":` + string(thread) + `}}`
					expected = nil
				}
				if _, err := DecodeCodexMessage([]byte(input), expected); err == nil {
					t.Fatalf("notification=%v accepted invalid thread %s=%s", notification, key, replacement)
				}
			}
			fields[key] = original
		}
	}
}

func TestCodexStrictOfficialThreadMetadata(t *testing.T) {
	// All official optional fields remain accepted and discarded. Runtime identity
	// and cwd remain the only retained fields, regardless of provider metadata.
	metadata := `,"forkedFromId":null,"parentThreadId":null,"section":{"id":"s","name":"section","appearance":{"icon":"icon","color":"color"}},"sectionEnteredAt":3,"historyMode":"paginated","model":"m","reasoningEffort":"future-effort","recencyAt":4,"path":"/provider/path","originator":"provider","threadSource":"source","agentNickname":"agent","agentRole":"role","gitInfo":{"sha":"sha","branch":"branch","originUrl":"url"},"name":"name"`
	thread := strings.TrimSuffix(officialThreadJSON, "}") + metadata + "}"
	result := strings.Replace(officialThreadStartJSON, officialThreadJSON, thread, 1)
	result = strings.TrimSuffix(result, "}") + `,"serviceTier":"tier","disabledPluginIds":["plugin"],"instructionSources":["/workspace/AGENTS.md"],"reasoningEffort":"effort"}`
	expected := &CodexResponseExpectation{ID: CodexIntegerID(2), Method: CodexThreadStart}
	wrap := func(result string) []byte { return []byte(`{"id":2,"result":` + result + `}`) }
	m, err := DecodeCodexMessage(wrap(result), expected)
	if err != nil || m.Response.ThreadStart.Thread != (CodexThread{ID: "thread-1", Cwd: "/workspace"}) {
		t.Fatal("official metadata/projection", err)
	}
	for _, change := range [][2]string{
		{`"icon":"icon"`, `"icon":"icon","unknown":true`},
		{`"appearance":{`, `"unknown":true,"appearance":{`},
		{`"sha":"sha"`, `"sha":"sha","unknown":true`},
		{`"type":"idle"`, `"type":"idle","unknown":true`},
		{`"type":"idle"`, `"type":"idle","activeFlags":[]`},
		{`"type":"idle"`, `"type":"active"`},
		{`"type":"idle"`, `"type":"active","activeFlags":[null]`},
		{`"type":"idle"`, `"type":"active","activeFlags":["unknown"]`},
		{`"createdAt":1`, `"createdAt":1.5`},
		{`"ephemeral":false`, `"ephemeral":0`},
		{`"historyMode":"paginated"`, `"historyMode":null`},
		{`"turns":[]`, `"turns":[null]`},
		{`"turns":[]`, `"turns":[{"id":"u","status":"completed","items":[],"unknown":true}]`},
		{`"instructionSources":["/workspace/AGENTS.md"]`, `"instructionSources":[null]`},
		{`"disabledPluginIds":["plugin"]`, `"disabledPluginIds":null`},
		{`"name":"section"`, `"name":null`},
	} {
		if _, err := DecodeCodexMessage(wrap(strings.Replace(result, change[0], change[1], 1)), expected); err == nil {
			t.Fatal("invalid nested metadata accepted", change[0])
		}
	}
	for _, source := range []string{`"cli"`, `"vscode"`, `"exec"`, `"unknown"`, `{"custom":"client"}`, `{"subAgent":"review"}`, `{"subAgent":"compact"}`, `{"subAgent":"memory_consolidation"}`, `{"subAgent":{"other":"source"}}`, `{"subAgent":{"thread_spawn":{"parent_thread_id":"parent","depth":1,"agent_path":"/agent","agent_nickname":null,"agent_role":null}}}`} {
		if _, err := DecodeCodexMessage(wrap(strings.Replace(result, `"source":"appServer"`, `"source":`+source, 1)), expected); err != nil {
			t.Fatal("official source rejected", err)
		}
	}
	for _, source := range []string{`{"custom":null}`, `{"custom":"client","unknown":true}`, `{"custom":"client","subAgent":null}`, `{"subAgent":null}`, `{"subAgent":{"other":null}}`, `{"subAgent":{"thread_spawn":{"parent_thread_id":"p","depth":1,"unknown":true}}}`, `{"subAgent":{"thread_spawn":{"depth":1}}}`, `{"subAgent":{"thread_spawn":null}}`} {
		if _, err := DecodeCodexMessage(wrap(strings.Replace(result, `"source":"appServer"`, `"source":`+source, 1)), expected); err == nil {
			t.Fatal("invalid source accepted")
		}
	}
	for _, status := range []string{`{"type":"notLoaded"}`, `{"type":"systemError"}`, `{"type":"active","activeFlags":["waitingOnApproval","waitingOnUserInput"]}`} {
		if _, err := DecodeCodexMessage(wrap(strings.Replace(result, `{"type":"idle"}`, status, 1)), expected); err != nil {
			t.Fatal("official status rejected", err)
		}
	}
}

func TestCodexStrictThreadStartPolicyMetadata(t *testing.T) {
	expected := &CodexResponseExpectation{ID: CodexIntegerID(2), Method: CodexThreadStart}
	for _, sandbox := range []string{`{"type":"dangerFullAccess"}`, `{"type":"readOnly"}`, `{"type":"readOnly","networkAccess":false}`, `{"type":"externalSandbox"}`, `{"type":"externalSandbox","networkAccess":"enabled"}`, `{"type":"workspaceWrite"}`, `{"type":"workspaceWrite","writableRoots":["/workspace"],"networkAccess":false,"excludeTmpdirEnvVar":true,"excludeSlashTmp":false}`} {
		for _, approval := range []string{`"never"`, `"untrusted"`, `"on-request"`, `{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true}}`, `{"granular":{"sandbox_approval":false,"rules":true,"mcp_elicitations":false,"skill_approval":true,"request_permissions":false}}`} {
			input := strings.Replace(officialThreadStartJSON, `{"type":"dangerFullAccess"}`, sandbox, 1)
			input = strings.Replace(input, `"approvalPolicy":"never"`, `"approvalPolicy":`+approval, 1)
			if _, err := DecodeCodexMessage([]byte(`{"id":2,"result":`+input+`}`), expected); err != nil {
				t.Fatal("official policy metadata rejected", err)
			}
		}
	}
	for _, change := range [][2]string{
		{`{"type":"dangerFullAccess"}`, `{"type":"dangerFullAccess","unknown":true}`},
		{`{"type":"dangerFullAccess"}`, `{"type":"dangerFullAccess","networkAccess":false}`},
		{`{"type":"dangerFullAccess"}`, `{"type":"readOnly","networkAccess":null}`},
		{`{"type":"dangerFullAccess"}`, `{"type":"externalSandbox","networkAccess":false}`},
		{`{"type":"dangerFullAccess"}`, `{"type":"workspaceWrite","writableRoots":[null]}`},
		{`"approvalPolicy":"never"`, `"approvalPolicy":{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true,"unknown":true}}`},
		{`"approvalPolicy":"never"`, `"approvalPolicy":{"granular":{"sandbox_approval":true,"rules":false,"mcp_elicitations":true,"skill_approval":null}}`},
		{`"approvalPolicy":"never"`, `"approvalPolicy":{"granular":{"rules":false}}`},
		{`"approvalsReviewer":"user"`, `"approvalsReviewer":"unknown"`},
	} {
		if _, err := DecodeCodexMessage([]byte(`{"id":2,"result":`+strings.Replace(officialThreadStartJSON, change[0], change[1], 1)+`}`), expected); err == nil {
			t.Fatal("invalid policy metadata accepted")
		}
	}
}

func TestCodexStrictRPCErrorProjection(t *testing.T) {
	expected := &CodexResponseExpectation{ID: CodexIntegerID(1), Method: CodexInitialize}
	for _, data := range []string{"", `,"data":null`, `,"data":{"detail":"credential-secret","nested":{"opaque":true}}`, `,"data":[1,"credential-secret"]`} {
		_, err := DecodeCodexMessage([]byte(`{"id":1,"error":{"code":-1,"message":"failure"`+data+`}}`), expected)
		var rpc *CodexRPCErrorResponse
		if !errors.As(err, &rpc) || rpc.ErrorDetail != (CodexRPCErrorDetail{Code: -1, Message: "failure"}) || strings.Contains(err.Error(), "credential-secret") {
			t.Fatal("invalid RPC projection or data leak")
		}
	}
	for _, detail := range []string{`null`, `[]`, `{}`, `{"code":null,"message":"failure"}`, `{"code":1.5,"message":"failure"}`, `{"code":-1,"message":null}`, `{"code":-1,"message":"failure","unknown":true}`, `{"code":-1,"message":"failure"} {}`} {
		_, err := DecodeCodexMessage([]byte(`{"id":1,"error":`+detail+`}`), expected)
		var rpc *CodexRPCErrorResponse
		if err == nil || errors.As(err, &rpc) {
			t.Fatal("malformed detail accepted as RPC error")
		}
	}
}

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
		{CodexThreadStart, officialThreadStartJSON},
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
		{CodexThreadStarted, `{"thread":` + officialThreadJSON + `}`},
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
