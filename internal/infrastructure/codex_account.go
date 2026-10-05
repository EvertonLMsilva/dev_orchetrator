package infrastructure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// CodexAccountReadError contains only classifications, a numeric code and
// fixed allowlisted text. It never wraps an upstream error or retains JSON.
type CodexAccountReadError struct {
	Kind        string
	RPCCode     *int64
	SafeMessage string
}

func (e *CodexAccountReadError) Error() string {
	return "account/read " + e.Kind + ": " + e.SafeMessage
}
func (e *CodexAccountReadError) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, e.Error()) }
func accountReadFailure(kind, message string) *CodexAccountReadError {
	return &CodexAccountReadError{Kind: kind, SafeMessage: message}
}

func requireCodexChatGPTAccount(transport codexHandshakeTransport) error {
	failure := accountReadFailure("protocol_decode", "account response invalid")
	if transport == nil || transport.Write([]byte(`{"id":99,"method":"account/read","params":{"refreshToken":false}}`)) != nil {
		return accountReadFailure("transport", "account transport failed")
	}
	for notifications := 0; notifications <= 128; notifications++ {
		data, err := transport.Read()
		if err != nil {
			return accountReadFailure("transport", "account transport failed")
		}
		var message struct {
			ID     *int64          `json:"id"`
			Method *string         `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  json.RawMessage `json:"error"`
			Result *struct {
				Account *struct {
					Type  string          `json:"type"`
					Email json.RawMessage `json:"email"`
					Plan  string          `json:"planType"`
				} `json:"account"`
				RequiresAuth *bool `json:"requiresOpenaiAuth"`
			} `json:"result"`
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		err = d.Decode(&message)
		if err == nil {
			err = d.Decode(new(any))
			if err == io.EOF {
				err = nil
			} else {
				err = failure
			}
		}
		clear(data)
		if err != nil {
			return failure
		}
		if message.ID == nil && message.Method != nil && message.Result == nil && len(message.Error) == 0 {
			continue
		}
		if message.ID == nil || *message.ID != 99 || message.Method != nil {
			return failure
		}
		if len(message.Error) != 0 {
			if message.Result != nil {
				return failure
			}
			var rpc struct {
				Code    *int64 `json:"code"`
				Message string `json:"message"`
			}
			if json.Unmarshal(message.Error, &rpc) != nil || rpc.Code == nil {
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
		if message.Result == nil || message.Result.RequiresAuth == nil {
			return failure
		}
		account := message.Result.Account
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
