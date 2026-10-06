package infrastructure

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCodexCorrelatedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		message            string
		id                 CodexRequestID
		notification, fail bool
	}{
		{`{"id":7,"result":{}}`, CodexIntegerID(7), false, false},
		{`{"id":"request-N","result":{}}`, CodexStringID("request-N"), false, false},
		{`{"id":8,"result":{}}`, CodexIntegerID(7), false, true},
		{`{"id":"7","result":{}}`, CodexIntegerID(7), false, true},
		{`{"method":"account/updated","params":{"SECRET_KEY":"SECRET_TOKEN"},"emittedAtMs":null}`, CodexIntegerID(7), true, false},
		{`{"id":null,"method":"account/updated","params":{}}`, CodexIntegerID(7), false, true},
		{`{"id":7,"result":{},"emittedAtMs":null}`, CodexIntegerID(7), false, true},
		{`{"id":7,"result":{},"error":{}}`, CodexIntegerID(7), false, true},
		{`{"id":7,"result":{},"SECRET_KEY":"SECRET_TOKEN"}`, CodexIntegerID(7), false, true},
		{`{"method":"account/updated","params":{},"SECRET_KEY":"SECRET_TOKEN"}`, CodexIntegerID(7), false, true},
		{`{"method":"","params":{}}`, CodexIntegerID(7), false, true},
		{`malformed SECRET_TOKEN`, CodexIntegerID(7), false, true},
	} {
		_, notification, err := decodeCodexCorrelatedEnvelope([]byte(tc.message), &tc.id)
		if (err != nil) != tc.fail || err == nil && notification != tc.notification {
			t.Fatal("correlation policy mismatch")
		}
		if err != nil && strings.Contains(err.Error(), "SECRET") {
			t.Fatal("raw protocol error leaked")
		}
	}
}

type accountCancelSession struct {
	threadFake
	cancel context.CancelFunc
}

func (s *accountCancelSession) Read() ([]byte, error) {
	data, err := s.threadFake.Read()
	s.cancel()
	return data, err
}
func (*accountCancelSession) Close() error { return nil }

func TestCodexAccountWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &accountCancelSession{threadFake: threadFake{messages: []string{`{"method":"account/updated","params":{},"emittedAtMs":1}`, `{"id":99,"result":{"account":{"type":"chatgpt","email":null,"planType":"plus"},"requiresOpenaiAuth":true}}`}}, cancel: cancel}
	err := requireCodexChatGPTAccount(codexContextTransport{ctx: ctx, session: f})
	var diagnostic *CodexAccountReadError
	if !errors.As(err, &diagnostic) || diagnostic.Kind != "transport" || len(f.messages) != 1 || len(f.writes) != 1 {
		t.Fatal("cancellation consumed correlated response")
	}
}
