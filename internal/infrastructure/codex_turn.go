package infrastructure

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Local byte bound, independent of the future executor-wide limits policy.
const codexTurnTextLimit = 64 * 1024

type CodexTurnID struct{ value string }

// CodexTurnResult is infrastructure data; model text is never executed.
type CodexTurnResult struct {
	TurnID CodexTurnID
	Status CodexTurnStatus
	Text   string
	Error  *CodexTurnError
}

type codexTurnCapability struct {
	mu        sync.Mutex
	transport codexHandshakeTransport
}

// StartTurn consumes a validated thread's single turn opportunity. The caller
// owns cleanup; failures cannot be retried through a copied capability.
func (thread CodexThreadID) StartTurn(text string) (CodexTurnResult, error) {
	fail := func(message string) (CodexTurnResult, error) { return CodexTurnResult{}, errors.New(message) }
	if thread.value == "" || thread.turn == nil {
		return fail("codex turn start requires validated thread")
	}
	capability := thread.turn
	capability.mu.Lock()
	defer capability.mu.Unlock()
	if capability.transport == nil {
		return fail("codex turn start requires unused thread")
	}
	transport := capability.transport
	capability.transport = nil
	id := CodexIntegerID(3)
	request, err := EncodeCodexTurnStart(id, thread.value, text)
	if err != nil {
		return CodexTurnResult{}, err
	}
	if err := transport.Write(request); err != nil {
		return CodexTurnResult{}, fmt.Errorf("codex turn start write failed: %w", err)
	}
	expected := &CodexResponseExpectation{ID: id, Method: CodexTurnStart}
	var turnID string
	var output strings.Builder
	// Bound asynchronous traffic as in the handshake/thread boundaries.
	for events := 0; events < 4096; events++ {
		data, err := transport.Read()
		if err != nil {
			return CodexTurnResult{}, fmt.Errorf("codex turn read before terminal failed: %w", err)
		}
		message, err := DecodeCodexMessage(data, expected)
		if err != nil {
			return CodexTurnResult{}, err
		}
		if message.Response != nil {
			if turnID != "" || message.Response.TurnStart == nil {
				return fail("codex turn unexpected response")
			}
			turnID = message.Response.TurnStart.Turn.ID
			expected = nil
			continue
		}
		notification := message.Notification
		if notification == nil {
			return fail("codex turn unexpected message")
		}
		if notification.AgentMessageDelta != nil {
			delta := notification.AgentMessageDelta
			if turnID == "" || delta.ThreadID != thread.value || delta.TurnID != turnID {
				return fail("codex turn delta correlation failed")
			}
			if len(delta.Delta) > codexTurnTextLimit-output.Len() {
				return fail("codex turn text limit exceeded")
			}
			output.WriteString(delta.Delta)
		}
		if notification.Turn != nil {
			event := notification.Turn
			if turnID == "" || event.ThreadID != thread.value || event.Turn.ID != turnID {
				return fail("codex turn event correlation failed")
			}
			if notification.Method == CodexTurnCompleted {
				return CodexTurnResult{TurnID: CodexTurnID{turnID}, Status: event.Turn.Status, Text: output.String(), Error: event.Turn.Error}, nil
			}
		}
	}
	return fail("codex turn event limit exceeded")
}
