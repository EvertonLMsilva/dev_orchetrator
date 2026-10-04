package infrastructure

import (
	"context"
	"errors"
)

type codexHandshakeTransport interface {
	Write([]byte) error
	Read() ([]byte, error)
}

// startCodexHandshake transfers container ownership to the existing runtime.
// Failure always closes it; success leaves the process alive for its owner.
func startCodexHandshake(ctx context.Context, docker codexProcessDocker, containerID string) (*CodexProcessTransport, *CodexInitializeResponse, error) {
	transport, err := startCodexProcessRuntime(ctx, docker, containerID)
	if err != nil {
		return nil, nil, err
	}
	response, err := codexHandshake(transport)
	if err != nil {
		return nil, nil, errors.Join(err, transport.Close())
	}
	return transport, response, nil
}

func codexHandshake(transport codexHandshakeTransport) (*CodexInitializeResponse, error) {
	title := "Dev Orchestrator"
	id := CodexIntegerID(1)
	request, err := EncodeCodexInitialize(id, CodexClientInfo{Name: "dev-orchestrator", Title: &title, Version: "0.1.0"})
	if err != nil {
		return nil, err
	}
	if err := transport.Write(request); err != nil {
		return nil, errors.New("codex handshake initialize write failed")
	}
	expected := CodexResponseExpectation{ID: id, Method: CodexInitialize}
	// Known codec notifications are asynchronous events, never acknowledgements or
	// authorization. Bound their count; unknown messages fail in the shared codec.
	for notifications := 0; notifications <= 128; notifications++ {
		data, err := transport.Read()
		if err != nil {
			return nil, errors.New("codex handshake initialize read failed")
		}
		message, err := DecodeCodexMessage(data, &expected)
		if err != nil {
			return nil, err
		}
		if message.Notification != nil {
			continue
		}
		if message.Response == nil || message.Response.Initialize == nil {
			return nil, errors.New("codex handshake unexpected response")
		}
		initialized, err := EncodeCodexInitialized()
		if err != nil {
			return nil, err
		}
		if err := transport.Write(initialized); err != nil {
			return nil, errors.New("codex handshake initialized write failed")
		}
		return message.Response.Initialize, nil
	}
	return nil, errors.New("codex handshake notification limit exceeded")
}
