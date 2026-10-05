package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCodexAccountInterleavedNotifications(t *testing.T) {
	account := `{"id":99,"result":{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}}`
	notification := `{"method":"account/updated","params":{"authMode":"chatgpt","SECRET_KEY":"SECRET_TOKEN"},"emittedAtMs":1234}`
	for _, tc := range []struct {
		name     string
		messages []string
		fail     bool
	}{
		{"notifications_before_response", []string{notification, notification, account}, false},
		{"immediate", []string{account}, false},
		{"inert_notification", []string{`{"method":"tool/run","params":{"command":"SECRET_COMMAND","auth":{"access_token":"SECRET_TOKEN"},"allowlist":["SECRET_HOST"]},"emittedAtMs":null}`, account}, false},
		{"wrong_id", []string{strings.Replace(account, `"id":99`, `"id":100`, 1), account}, true},
		{"wrong_id_type", []string{strings.Replace(account, `"id":99`, `"id":"99"`, 1)}, true},
		{"malformed", []string{`{"method":"account/updated","params":[],"emittedAtMs":1234}`, account}, true},
		{"request_not_notification", []string{`{"id":4,"method":"tool/run","params":{}}`, account}, true},
		{"invalid_timestamp", []string{`{"method":"account/updated","params":{},"emittedAtMs":"SECRET_TOKEN"}`, account}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &threadFake{messages: tc.messages}
			err := requireCodexChatGPTAccount(f)
			if (err != nil) != tc.fail {
				t.Fatalf("correlation mismatch: %v", err)
			}
			if len(f.writes) != 1 {
				t.Fatal("notification triggered additional operation")
			}
			if err != nil && strings.Contains(fmt.Sprintf("%+v", err), "SECRET") {
				t.Fatal("notification payload leaked")
			}
		})
	}
	f := &threadFake{}
	for i := 0; i < 129; i++ {
		f.messages = append(f.messages, notification)
	}
	f.messages = append(f.messages, account)
	if requireCodexChatGPTAccount(f) == nil || len(f.messages) != 1 {
		t.Fatal("unbounded notification wait")
	}
}

func TestCodexAccountSafeShape(t *testing.T) {
	response := `{"id":99,"result":{"account":{"type":"chatgpt","email":"EMAIL_SECRET","planType":"plus"},"requiresOpenaiAuth":true,"workspaceRouting":null,"KEY_SECRET":"VALUE_SECRET"}}`
	err := requireCodexChatGPTAccount(&threadFake{messages: []string{response}})
	var diagnostic *CodexAccountReadError
	if !errors.As(err, &diagnostic) || diagnostic.Kind != "protocol_decode" {
		t.Fatal("unknown fields accepted")
	}
	for _, expected := range []string{"top.id=number", "top.result=object", "result.account=object", "account.email=string", "account.type=string", "account.planType=string", "result.workspaceRouting=null", "result.unknown_fields=present", "top.jsonrpc=absent"} {
		if !strings.Contains(diagnostic.ResponseShape, expected) {
			t.Fatalf("missing safe structural field: %s", expected)
		}
	}
	for _, rendered := range []string{diagnostic.ResponseShape, err.Error(), fmt.Sprintf("%#v", err)} {
		for _, secret := range []string{"EMAIL_SECRET", "KEY_SECRET", "VALUE_SECRET", response} {
			if strings.Contains(rendered, secret) {
				t.Fatal("shape leaked value or arbitrary key")
			}
		}
	}
	for _, tc := range []struct{ response, expected string }{
		{`{"id":"SECRET_ID","jsonrpc":"SECRET_VERSION","result":{"account":[],"requiresOpenaiAuth":null}}`, "result.account=array"},
		{`{"id":99,"result":{"account":{"type":"chatgpt","email":false,"planType":"plus"},"requiresOpenaiAuth":true}}`, "account.email=boolean"},
		{`{"id":99,"result":{"account":{"type":"chatgpt","planType":"plus"},"requiresOpenaiAuth":true}}`, "account.email=absent"},
		{`{"id":99,"result":{"workspaceRouting":{"backendOrigin":"SECRET_URL","chatgptAccountId":"SECRET_ACCOUNT","accountRoutingOverride":"SECRET_ROUTING"}}}`, "workspaceRouting.chatgptAccountId=string"},
		{`malformed SECRET_TOKEN`, "shape=invalid_json"},
		{`{"auth.json":{"tokens":{"access_token":"SECRET_TOKEN"}},"SECRET_KEY":1}`, "top.unknown_fields=present"},
	} {
		shape := safeCodexAccountShape([]byte(tc.response))
		if !strings.Contains(shape, tc.expected) || strings.Contains(shape, "SECRET") || strings.Contains(shape, "access_token") || strings.Contains(shape, "auth.json") {
			t.Fatal("unsafe or incorrect projection")
		}
	}
}

func TestCodexAccountSafeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, response, kind, safe string
		code                       int64
	}{
		{"rpc", `{"id":99,"error":{"code":-32000,"message":"upstream failure"}}`, "rpc_error", "account RPC failed", -32000},
		{"routing", `{"id":99,"error":{"code":-32001,"message":"workspace routing discovery failed"}}`, "workspace_routing", "workspace routing discovery failed", -32001},
		{"decode", `broken TOKEN_SECRET`, "protocol_decode", "account response invalid", 0},
		{"absent", `{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`, "account_unavailable", "account unavailable", 0},
		{"type", `{"id":99,"result":{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}}`, "wrong_account_type", "ChatGPT account required", 0},
		{"secret", `{"id":99,"error":{"code":-1,"message":"workspace routing discovery failed TOKEN_SECRET","data":{"auth.json":{"access_token":"TOKEN_SECRET"}}}}`, "rpc_error", "account RPC failed", -1},
		{"auth", `{"auth_mode":"chatgpt","tokens":{"access_token":"TOKEN_SECRET"}}`, "protocol_decode", "account response invalid", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireCodexChatGPTAccount(&threadFake{messages: []string{tc.response}})
			var diagnostic *CodexAccountReadError
			if !errors.As(err, &diagnostic) || diagnostic.Kind != tc.kind || diagnostic.SafeMessage != tc.safe {
				t.Fatalf("diagnostic mismatch: %v", err)
			}
			if tc.code != 0 && (diagnostic.RPCCode == nil || *diagnostic.RPCCode != tc.code) {
				t.Fatal("RPC code lost")
			}
			for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
				if strings.Contains(rendered, "TOKEN_SECRET") || strings.Contains(rendered, "access_token") || strings.Contains(rendered, "auth.json") || strings.Contains(rendered, tc.response) {
					t.Fatal("raw payload leaked")
				}
			}
		})
	}
	for _, f := range []*threadFake{{writeFailure: 1}, {readErr: errors.New("TOKEN_SECRET")}} {
		var diagnostic *CodexAccountReadError
		if err := requireCodexChatGPTAccount(f); !errors.As(err, &diagnostic) || diagnostic.Kind != "transport" || strings.Contains(err.Error(), "TOKEN_SECRET") {
			t.Fatal("transport diagnosis failed")
		}
	}
}

func TestAuthenticatedExecutorRequiresAccountBeforeThread(t *testing.T) {
	for _, authenticated := range []bool{true, false} {
		account := `{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`
		if authenticated {
			account = `{"id":99,"result":{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}}`
		}
		f := executionFake(handshakeResponse, account, threadResponse, turnResponse, turnDelta("done"), turnEvent("completed"))
		runtime := NewCodexExecutorRuntime(func(context.Context, DockerEnvironmentConfig) (CodexExecutorSession, error) { return f, nil })
		runtime.authenticated = true
		result, err := runtime.Execute(context.Background(), executionRequest())
		if authenticated && (err != nil || result.Summary != "done") {
			t.Fatal("authenticated execution failed")
		}
		if !authenticated && (err == nil || len(f.writes) != 3) {
			t.Fatal("unauthenticated account started thread")
		}
		if f.closes != 1 {
			t.Fatal("account path skipped cleanup")
		}
	}
}

func TestCodexChatGPTAccount(t *testing.T) {
	for _, tc := range []struct {
		message  string
		accepted bool
	}{
		{`{"id":99,"result":{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}}`, true},
		{`{"id":99,"result":{"account":null,"requiresOpenaiAuth":true}}`, false},
		{`{"id":99,"result":{"account":{"type":"apiKey"},"requiresOpenaiAuth":true}}`, false},
		{`{"id":100,"result":{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}}`, false},
		{`{"id":99,"error":{"message":"TEST_SECRET_DO_NOT_LEAK"}}`, false},
		{`malformed TEST_SECRET_DO_NOT_LEAK`, false},
	} {
		f := &threadFake{messages: []string{tc.message}}
		err := requireCodexChatGPTAccount(f)
		if (err == nil) != tc.accepted {
			t.Fatal("account authorization mismatch")
		}
		if err != nil && strings.Contains(err.Error(), "TEST_SECRET") {
			t.Fatal("account error leaks")
		}
		if len(f.writes) != 1 || string(f.writes[0]) != `{"id":99,"method":"account/read","params":{"refreshToken":false}}` {
			t.Fatal("account RPC mismatch")
		}
	}
}
