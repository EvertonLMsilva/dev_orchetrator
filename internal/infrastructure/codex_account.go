package infrastructure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// CodexAccountReadError contains only classifications, a numeric code and
// fixed allowlisted text. It never wraps an upstream error or retains JSON.
type CodexAccountReadError struct {
	Kind        string
	RPCCode     *int64
	SafeMessage string
	// ResponseShape contains only fixed protocol paths and JSON type names.
	ResponseShape string
}

func (e *CodexAccountReadError) Error() string {
	return "account/read " + e.Kind + ": " + e.SafeMessage
}
func (e *CodexAccountReadError) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, e.Error()) }
func accountReadFailure(kind, message string) *CodexAccountReadError {
	return &CodexAccountReadError{Kind: kind, SafeMessage: message}
}

// Never emit arbitrary keys: even a JSON field name can carry a secret.
// This diagnostic projection does not participate in account authorization.
func safeCodexAccountShape(data []byte) string {
	if !json.Valid(data) {
		return "shape=invalid_json"
	}
	var fields []string
	valueType := func(raw json.RawMessage) string {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			return "absent"
		}
		switch raw[0] {
		case '{':
			return "object"
		case '[':
			return "array"
		case '"':
			return "string"
		case 'n':
			return "null"
		case 't', 'f':
			return "boolean"
		default:
			return "number"
		}
	}
	project := func(prefix string, raw json.RawMessage, known []string) map[string]json.RawMessage {
		fields = append(fields, prefix+"="+valueType(raw))
		var object map[string]json.RawMessage
		if valueType(raw) != "object" || json.Unmarshal(raw, &object) != nil {
			return nil
		}
		unknown := len(object)
		for _, name := range known {
			child, present := object[name]
			if present {
				unknown--
			}
			fields = append(fields, prefix+"."+name+"="+valueType(child))
		}
		presence := "absent"
		if unknown > 0 {
			presence = "present"
		}
		fields = append(fields, prefix+".unknown_fields="+presence)
		return object
	}
	top := project("top", data, []string{"id", "jsonrpc", "method", "params", "result", "error", "emittedAtMs"})
	result := project("result", top["result"], []string{"account", "requiresOpenaiAuth", "workspaceRouting"})
	project("account", result["account"], []string{"type", "email", "planType", "usesCodexManagedCredentials"})
	project("workspaceRouting", result["workspaceRouting"], []string{"accountRoutingOverride", "backendOrigin", "chatgptAccountId"})
	project("error", top["error"], []string{"code", "message", "data"})
	return strings.Join(fields, " ")
}

func requireCodexChatGPTAccount(transport codexHandshakeTransport) error {
	failure := accountReadFailure("protocol_decode", "account response invalid")
	id := CodexIntegerID(99)
	request, err := json.Marshal(struct {
		ID     CodexRequestID  `json:"id"`
		Method string          `json:"method"`
		Params map[string]bool `json:"params"`
	}{id, "account/read", map[string]bool{"refreshToken": false}})
	if err != nil {
		return failure
	}
	if transport == nil || transport.Write(request) != nil {
		return accountReadFailure("transport", "account transport failed")
	}
	for notifications := 0; notifications <= 128; notifications++ {
		data, err := transport.Read()
		if err != nil {
			return accountReadFailure("transport", "account transport failed")
		}
		envelope, notification, envelopeErr := decodeCodexCorrelatedEnvelope(data, &id)
		// Compute shape before clearing bytes; no scalar values or raw JSON
		// survive in the returned diagnostic.
		failure.ResponseShape = safeCodexAccountShape(data)
		clear(data)
		if envelopeErr != nil {
			return failure
		}
		if notification {
			// During account verification events are discarded, never executed
			// or interpreted as authorization. Payloads are not logged.
			continue
		}
		var result struct {
			Account *struct {
				Type  string          `json:"type"`
				Email json.RawMessage `json:"email"`
				Plan  string          `json:"planType"`
			} `json:"account"`
			RequiresAuth *bool `json:"requiresOpenaiAuth"`
		}
		if len(envelope.Error) != 0 {
			var rpc struct {
				Code    *int64 `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(envelope.Error, &rpc) != nil || rpc.Code == nil {
				return failure
			}
			diagnostic := accountReadFailure("rpc_error", "account RPC failed")
			diagnostic.RPCCode = rpc.Code
			// Exact equality only: appended identifiers, tokens and arbitrary
			// upstream messages are never copied into the public diagnostic.
			if rpc.Message == "workspace routing discovery failed" {
				diagnostic.Kind = "workspace_routing"
				diagnostic.SafeMessage = "workspace routing discovery failed"
			}
			return diagnostic
		}
		if decodeCodexStrictObject(envelope.Result, &result) != nil || result.RequiresAuth == nil {
			return failure
		}
		account := result.Account
		if account == nil {
			return accountReadFailure("account_unavailable", "account unavailable")
		}
		if account.Type != "chatgpt" {
			return accountReadFailure("wrong_account_type", "ChatGPT account required")
		}
		if account.Plan == "" || len(account.Email) == 0 {
			return failure
		}
		var email *string
		if json.Unmarshal(account.Email, &email) != nil {
			return failure
		}
		return nil
	}
	return failure
}
