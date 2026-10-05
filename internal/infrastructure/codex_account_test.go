package infrastructure

import (
	"context"
	"strings"
	"testing"
)

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
