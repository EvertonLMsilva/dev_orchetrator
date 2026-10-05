package infrastructure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const handshakeResponse = `{"id":1,"result":{"userAgent":"codex/0.159.2","codexHome":"/run/codex-process","platformFamily":"unix","platformOs":"linux"}}`

type handshakeFake struct {
	messages     []string
	writes       [][]byte
	readErr      error
	writeFailure int
}

func (f *handshakeFake) Write(b []byte) error {
	f.writes = append(f.writes, append([]byte(nil), b...))
	if f.writeFailure == len(f.writes) {
		return errors.New("transport failure")
	}
	return nil
}
func (f *handshakeFake) Read() ([]byte, error) {
	if len(f.writes) != 1 {
		return nil, errors.New("initialized sent before response")
	}
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
func TestCodexHandshake(t *testing.T) {
	notification := threadNotification
	for _, tc := range []struct {
		name     string
		messages []string
		fail     bool
	}{
		{"response_decode", []string{handshakeResponse}, false},
		{"notification_before_response", []string{notification, handshakeResponse}, false},
		{"wrong_id", []string{strings.Replace(handshakeResponse, `"id":1`, `"id":2`, 1)}, true},
		{"rpc_error", []string{`{"id":1,"error":{"code":-32600,"message":"rejected"}}`}, true},
		{"malformed_json", []string{`{`}, true},
		{"malformed_response", []string{`{"id":1,"result":{}}`}, true},
		{"eof", nil, true},
		{"unknown_notification", []string{`{"method":"unknown","params":{}}`}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &handshakeFake{messages: tc.messages}
			result, err := codexHandshake(f)
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected error: %v", err)
			}
			var request struct {
				ID     int
				Method string
				Params CodexInitializeParams
			}
			if len(f.writes) == 0 || json.Unmarshal(f.writes[0], &request) != nil || request.ID != 1 || request.Method != "initialize" || request.Params.ClientInfo.Name != "dev-orchestrator" || request.Params.ClientInfo.Title == nil || *request.Params.ClientInfo.Title != "Dev Orchestrator" || request.Params.ClientInfo.Version != "0.1.0" {
				t.Fatal("initialize_send contract")
			}
			if tc.fail {
				if len(f.writes) != 1 {
					t.Fatal("initialized sent on failure")
				}
				return
			}
			if result.UserAgent != "codex/0.159.2" || len(f.writes) != 2 || string(f.writes[1]) != `{"method":"initialized"}` {
				t.Fatal("response/order/notification contract")
			}
		})
	}
}
func TestCodexHandshakeTransportFailure(t *testing.T) {
	for _, n := range []int{1, 2} {
		f := &handshakeFake{messages: []string{handshakeResponse}, writeFailure: n}
		if _, err := codexHandshake(f); err == nil {
			t.Fatal("write failure accepted")
		}
	}
	f := &handshakeFake{readErr: errors.New("read failure")}
	if _, err := codexHandshake(f); err == nil {
		t.Fatal("read failure accepted")
	}
}
func TestCodexHandshakeRuntime(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	d := &fakeCodexProcessDocker{output: reader}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { writer.Write(codexDockerFrame(1, handshakeResponse+"\n")) }()
	tr, result, err := startCodexHandshake(ctx, d, "container")
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	if result.UserAgent != "codex/0.159.2" {
		t.Fatal("response missing")
	}
	d.mu.Lock()
	input := d.input.String()
	d.mu.Unlock()
	if !strings.HasSuffix(input, "{\"method\":\"initialized\"}\n") {
		t.Fatal("runtime framing missing")
	}
}

func TestCodexHandshakeFailureClosesRuntime(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	d := &fakeCodexProcessDocker{output: reader}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { writer.Write(codexDockerFrame(1, "{\"id\":2,\"result\":{}}\n")) }()
	tr, response, err := startCodexHandshake(ctx, d, "container")
	if err == nil || tr != nil || response != nil {
		t.Fatal("invalid response accepted")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed || d.calls[len(d.calls)-1] != "remove" || strings.Contains(d.input.String(), `"initialized"`) {
		t.Fatal("failure cleanup/order violated")
	}
}

func TestCodexHandshakeNotificationLimit(t *testing.T) {
	f := &handshakeFake{}
	for i := 0; i < 129; i++ {
		f.messages = append(f.messages, threadNotification)
	}
	if _, err := codexHandshake(f); err == nil || len(f.writes) != 1 {
		t.Fatal("notification flood accepted")
	}
}
