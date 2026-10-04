package infrastructure

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

const threadResponse = `{"id":2,"result":{"thread":{"id":"thread-1","cwd":"/workspace"},"cwd":"/workspace"}}`
const threadNotification = `{"method":"thread/started","params":{"thread":{"id":"existing","cwd":"/workspace"}}}`

type threadFake struct {
	messages     []string
	writes       [][]byte
	writeFailure int
	readErr      error
}

func (f *threadFake) Write(b []byte) error {
	f.writes = append(f.writes, append([]byte(nil), b...))
	if len(f.writes) == f.writeFailure {
		return errors.New("transport failure")
	}
	return nil
}
func (f *threadFake) Read() ([]byte, error) {
	if len(f.messages) == 0 {
		if f.readErr != nil {
			return nil, f.readErr
		}
		return nil, io.EOF
	}
	m := f.messages[0]
	f.messages = f.messages[1:]
	return []byte(m), nil
}

func TestCodexThreadStart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []string
		fail     bool
	}{
		{"valid_response", []string{threadResponse}, false},
		{"notification_before_response", []string{threadNotification, threadNotification, threadResponse}, false},
		{"wrong_id", []string{strings.Replace(threadResponse, `"id":2`, `"id":1`, 1)}, true},
		{"wrong_id_type", []string{strings.Replace(threadResponse, `"id":2`, `"id":"2"`, 1)}, true},
		{"rpc_error", []string{`{"id":2,"error":{"code":-32600,"message":"rejected"}}`}, true},
		{"malformed_response", []string{`{"id":2,"result":{}}`}, true},
		{"malformed_json", []string{`{`}, true},
		{"missing_thread_id", []string{`{"id":2,"result":{"thread":{"cwd":"/workspace"},"cwd":"/workspace"}}`}, true},
		{"empty_thread_id", []string{strings.Replace(threadResponse, `"thread-1"`, `""`, 1)}, true},
		{"wrong_cwd", []string{strings.ReplaceAll(threadResponse, "/workspace", "/host")}, true},
		{"unknown_notification", []string{`{"method":"unknown","params":{}}`, threadResponse}, true},
		{"malformed_notification", []string{`{"method":"thread/started","params":{}}`, threadResponse}, true},
		{"eof", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &threadFake{messages: append([]string{handshakeResponse}, tc.messages...)}
			session, err := completeCodexHandshake(f)
			if err != nil {
				t.Fatal(err)
			}
			id, err := session.StartThread()
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.fail && id != (CodexThreadID{}) {
				t.Fatal("trusted ID on failure")
			}
			if !tc.fail && id.value != "thread-1" {
				t.Fatal("missing trusted ID")
			}
			if tc.name == "rpc_error" {
				var rpc *CodexRPCErrorResponse
				if !errors.As(err, &rpc) {
					t.Fatal("lost RPC failure")
				}
			}
			if len(f.writes) != 3 || string(f.writes[1]) != `{"method":"initialized"}` || string(f.writes[2]) != `{"id":2,"method":"thread/start","params":{"cwd":"/workspace"}}` {
				t.Fatalf("encode/order: %s", f.writes)
			}
			if _, err := session.StartThread(); err == nil || len(f.writes) != 3 {
				t.Fatal("session reused")
			}
		})
	}
}

func TestCodexThreadCallerCannotControlCwd(t *testing.T) {
	if reflect.TypeOf(CodexThreadStartParams{}).NumField() != 0 {
		t.Fatal("caller configuration exposed")
	}
	method, ok := reflect.TypeOf((*codexReadySession)(nil)).MethodByName("StartThread")
	if !ok || method.Type.NumIn() != 1 {
		t.Fatal("StartThread accepts caller configuration")
	}
}

func TestCodexThreadHandshakeRequired(t *testing.T) {
	for _, session := range []*codexReadySession{nil, {}} {
		if id, err := session.StartThread(); err == nil || id != (CodexThreadID{}) {
			t.Fatal("uninitialized session accepted")
		}
	}
	for _, f := range []*threadFake{
		{messages: []string{`{"id":1,"result":{}}`}},
		{messages: []string{handshakeResponse}, writeFailure: 2},
	} {
		if session, err := completeCodexHandshake(f); err == nil || session != nil {
			t.Fatal("failed handshake granted session")
		}
		for _, write := range f.writes {
			if strings.Contains(string(write), "thread/start") {
				t.Fatal("thread before handshake")
			}
		}
	}
}

func TestCodexThreadTransportFailureAndNotificationLimit(t *testing.T) {
	for _, f := range []*threadFake{
		{messages: []string{handshakeResponse}, writeFailure: 3},
		{messages: []string{handshakeResponse}, readErr: errors.New("read failure")},
		{messages: append([]string{handshakeResponse}, append(makeNotifications(129), threadResponse)...)},
	} {
		session, err := completeCodexHandshake(f)
		if err != nil {
			t.Fatal(err)
		}
		if id, err := session.StartThread(); err == nil || id != (CodexThreadID{}) {
			t.Fatal("failure accepted")
		}
	}
}
func makeNotifications(n int) []string {
	result := make([]string, n)
	for i := range result {
		result[i] = threadNotification
	}
	return result
}
