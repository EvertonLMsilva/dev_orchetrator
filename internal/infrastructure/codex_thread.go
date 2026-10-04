package infrastructure

import (
	"errors"
	"sync"
)

// CodexThreadID is provider-specific and stays inside infrastructure. Its zero
// value is untrusted; only a correlated, validated response produces a value.
type CodexThreadID struct {
	value string
	turn  *codexTurnCapability
}

// codexReadySession owns one thread-start opportunity after initialize and
// initialized succeed. Keep it by pointer; concurrent calls are serialized.
type codexReadySession struct {
	mu        sync.Mutex
	transport codexHandshakeTransport
}

// completeCodexHandshake composes the existing handshake with a typed capability.
// The caller retains transport cleanup ownership on both success and failure.
func completeCodexHandshake(transport codexHandshakeTransport) (*codexReadySession, error) {
	if transport == nil {
		return nil, errors.New("codex handshake transport required")
	}
	if _, err := codexHandshake(transport); err != nil {
		return nil, err
	}
	return &codexReadySession{transport: transport}, nil
}

func (s *codexReadySession) StartThread() (CodexThreadID, error) {
	if s == nil {
		return CodexThreadID{}, errors.New("codex thread start requires completed handshake")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.transport == nil {
		return CodexThreadID{}, errors.New("codex thread start requires unused completed handshake")
	}
	transport := s.transport
	// Consume before I/O: a failed or ambiguous attempt must never be retried on
	// this capability, nor may a second call create another thread for the task.
	s.transport = nil
	id := CodexIntegerID(2)
	request, err := EncodeCodexThreadStart(id)
	if err != nil {
		return CodexThreadID{}, err
	}
	if err := transport.Write(request); err != nil {
		return CodexThreadID{}, errors.New("codex thread start write failed")
	}
	expected := CodexResponseExpectation{ID: id, Method: CodexThreadStart}
	for notifications := 0; notifications <= 128; notifications++ {
		data, err := transport.Read()
		if err != nil {
			return CodexThreadID{}, errors.New("codex thread start read failed")
		}
		message, err := DecodeCodexMessage(data, &expected)
		if err != nil {
			return CodexThreadID{}, err
		}
		if message.Notification != nil {
			continue
		}
		if message.Response == nil || message.Response.ThreadStart == nil {
			return CodexThreadID{}, errors.New("codex thread start unexpected response")
		}
		return CodexThreadID{value: message.Response.ThreadStart.Thread.ID, turn: &codexTurnCapability{transport: transport}}, nil
	}
	return CodexThreadID{}, errors.New("codex thread start notification limit exceeded")
}
