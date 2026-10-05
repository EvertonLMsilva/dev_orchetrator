package infrastructure

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func requireCodexChatGPTAccount(transport codexHandshakeTransport) error {
	failure := errors.New("authenticated chatgpt account unavailable")
	if transport == nil || transport.Write([]byte(`{"id":99,"method":"account/read","params":{"refreshToken":false}}`)) != nil {
		return failure
	}
	for notifications := 0; notifications <= 128; notifications++ {
		data, err := transport.Read()
		if err != nil {
			return failure
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
		if message.ID == nil || *message.ID != 99 || message.Method != nil || len(message.Error) != 0 || message.Result == nil || message.Result.RequiresAuth == nil {
			return failure
		}
		account := message.Result.Account
		if account == nil || account.Type != "chatgpt" || account.Plan == "" || len(account.Email) == 0 {
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
