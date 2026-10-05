package application

import (
	"bytes"
	"encoding/json"
	"io"

	"dev-orchestrator/internal/domain"
)

// EncodeCodexResult serializes the shared envelope without generating metadata.
// Summary remains untrusted text and is never interpreted.
func EncodeCodexResult(e domain.Envelope) ([]byte, error) {
	if err := ValidateCodexResultEnvelope(e); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// DecodeCodexResult returns only a fully validated, typed envelope. The wire
// fields and opaque version follow domain.Envelope; unknown fields fail closed.
func DecodeCodexResult(data []byte) (domain.Envelope, error) {
	var wire struct {
		domain.Envelope
		Payload json.RawMessage
	}
	if err := decodeCodexResultJSON(data, &wire); err != nil {
		return domain.Envelope{}, err
	}
	var payload CodexResult
	if err := decodeCodexResultJSON(wire.Payload, &payload); err != nil {
		return domain.Envelope{}, err
	}
	wire.Envelope.Payload = payload
	if err := ValidateCodexResultEnvelope(wire.Envelope); err != nil {
		return domain.Envelope{}, err
	}
	return wire.Envelope, nil
}

func decodeCodexResultJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return ErrInvalidCodexResult
	}
	return nil
}
