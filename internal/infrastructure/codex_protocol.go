package infrastructure

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Wire contracts verified against openai/codex rust-v0.159.2:
// codex-rs/app-server-protocol/schema/{json,typescript}.
// Responses/events are projections of the fields needed for correlation and
// execution status. Ancillary Thread/ThreadItem fields are deliberately omitted.
// This codec neither starts a provider nor normalizes an Executor outcome.
type CodexMethod string

const (
	CodexInitialize        CodexMethod = "initialize"
	CodexInitialized       CodexMethod = "initialized"
	CodexThreadStart       CodexMethod = "thread/start"
	CodexTurnStart         CodexMethod = "turn/start"
	CodexThreadStarted     CodexMethod = "thread/started"
	CodexTurnStarted       CodexMethod = "turn/started"
	CodexTurnCompleted     CodexMethod = "turn/completed"
	CodexAgentMessageDelta CodexMethod = "item/agentMessage/delta"
	codexProtocolWorkspace             = "/workspace"
)

// CodexRequestID preserves both int64 precision and the string/integer distinction.
// The zero value is invalid; numeric zero is constructed explicitly.
type CodexRequestID struct {
	kind   byte
	number int64
	text   string
}

func CodexIntegerID(id int64) CodexRequestID { return CodexRequestID{kind: 'n', number: id} }
func CodexStringID(id string) CodexRequestID { return CodexRequestID{kind: 's', text: id} }

func (id CodexRequestID) MarshalJSON() ([]byte, error) {
	switch id.kind {
	case 's':
		return json.Marshal(id.text)
	case 'n':
		return []byte(strconv.FormatInt(id.number, 10)), nil
	default:
		return nil, errors.New("codex protocol: missing request ID")
	}
}

func (id *CodexRequestID) UnmarshalJSON(data []byte) error {
	*id = CodexRequestID{}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		*id = CodexStringID(text)
		return nil
	}
	value, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return errors.New("codex protocol: ID must be string or int64")
	}
	*id = CodexIntegerID(value)
	return nil
}

type CodexClientInfo struct {
	Name    string  `json:"name"`
	Title   *string `json:"title,omitempty"`
	Version string  `json:"version"`
}

type CodexInitializeParams struct {
	ClientInfo CodexClientInfo `json:"clientInfo"`
}

// No exported cwd field: infrastructure owns the encoded workspace.
type CodexThreadStartParams struct{}

func (CodexThreadStartParams) MarshalJSON() ([]byte, error) {
	return []byte(`{"cwd":"/workspace"}`), nil
}

// Only plain text is needed; text_elements is always an empty array.
type CodexTextInput struct {
	Type         string      `json:"type"`
	Text         string      `json:"text"`
	TextElements [0]struct{} `json:"text_elements"`
}

type CodexTurnStartParams struct {
	ThreadID string           `json:"threadId"`
	Input    []CodexTextInput `json:"input"`
}

type codexRequestParams interface {
	CodexInitializeParams | CodexThreadStartParams | CodexTurnStartParams
}

// App-server uses JSON-RPC envelopes without the jsonrpc version field.
type CodexRPCRequest[P codexRequestParams] struct {
	ID     CodexRequestID `json:"id"`
	Method CodexMethod    `json:"method"`
	Params P              `json:"params"`
}

type CodexInitializedNotification struct {
	Method CodexMethod `json:"method"`
}

func EncodeCodexInitialize(id CodexRequestID, client CodexClientInfo) ([]byte, error) {
	if client.Name == "" || client.Version == "" {
		return nil, errors.New("codex protocol: client name and version required")
	}
	return json.Marshal(CodexRPCRequest[CodexInitializeParams]{id, CodexInitialize, CodexInitializeParams{client}})
}

func EncodeCodexInitialized() ([]byte, error) {
	return json.Marshal(CodexInitializedNotification{CodexInitialized})
}

func EncodeCodexThreadStart(id CodexRequestID) ([]byte, error) {
	return json.Marshal(CodexRPCRequest[CodexThreadStartParams]{id, CodexThreadStart, CodexThreadStartParams{}})
}

func EncodeCodexTurnStart(id CodexRequestID, threadID, text string) ([]byte, error) {
	if threadID == "" {
		return nil, errors.New("codex protocol: thread ID required")
	}
	params := CodexTurnStartParams{threadID, []CodexTextInput{{Type: "text", Text: text}}}
	return json.Marshal(CodexRPCRequest[CodexTurnStartParams]{id, CodexTurnStart, params})
}

type CodexInitializeResponse struct {
	UserAgent      string `json:"userAgent"`
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

type CodexThread struct {
	ID  string `json:"id"`
	Cwd string `json:"cwd"`
}

type CodexThreadStartResponse struct {
	Thread CodexThread `json:"thread"`
	Cwd    string      `json:"cwd"`
}

type CodexTurnStatus string

const (
	CodexTurnInProgress      CodexTurnStatus = "inProgress"
	CodexTurnCompletedStatus CodexTurnStatus = "completed"
	CodexTurnFailed          CodexTurnStatus = "failed"
	CodexTurnInterrupted     CodexTurnStatus = "interrupted"
)

type CodexTurnError struct {
	Message string `json:"message"`
}

type CodexTurn struct {
	ID     string          `json:"id"`
	Status CodexTurnStatus `json:"status"`
	Error  *CodexTurnError `json:"error"`
}

type CodexTurnStartResponse struct {
	Turn CodexTurn `json:"turn"`
}

// Method is local correlation metadata, not a field in a success wire response.
// Decode requires an outstanding request expectation because responses have no method.
type CodexResponseExpectation struct {
	ID     CodexRequestID
	Method CodexMethod
}

type CodexRPCSuccessResponse struct {
	ID          CodexRequestID
	Method      CodexMethod
	Initialize  *CodexInitializeResponse
	ThreadStart *CodexThreadStartResponse
	TurnStart   *CodexTurnStartResponse
}

type CodexRPCErrorDetail struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type CodexRPCErrorResponse struct {
	ID          CodexRequestID      `json:"id"`
	ErrorDetail CodexRPCErrorDetail `json:"error"`
}

func (e *CodexRPCErrorResponse) Error() string {
	return fmt.Sprintf("codex provider RPC error (%d): %s", e.ErrorDetail.Code, e.ErrorDetail.Message)
}

type CodexThreadStartedNotification struct {
	Thread CodexThread `json:"thread"`
}
type CodexTurnNotification struct {
	ThreadID string    `json:"threadId"`
	Turn     CodexTurn `json:"turn"`
}

type CodexAgentMessageDeltaNotification struct {
	ThreadID string `json:"threadId"`
	TurnID   string `json:"turnId"`
	ItemID   string `json:"itemId"`
	Delta    string `json:"delta"`
}

type CodexRPCNotification struct {
	Method            CodexMethod
	ThreadStarted     *CodexThreadStartedNotification
	Turn              *CodexTurnNotification
	AgentMessageDelta *CodexAgentMessageDeltaNotification
}

type CodexMessage struct {
	Response     *CodexRPCSuccessResponse
	Notification *CodexRPCNotification
}

// RawMessage is confined to framing/discrimination; provider payloads are typed.
type codexEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method json.RawMessage `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func codexObject(data []byte) bool {
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '{'
}

// DecodeCodexMessage decodes one message, never a stream or a server request.
// Unknown notifications fail closed; none are silently treated as completion.
// The caller owns outstanding-request lifecycle and supplies correlation metadata.
func DecodeCodexMessage(data []byte, expected *CodexResponseExpectation) (CodexMessage, error) {
	invalid := errors.New("codex protocol: malformed or unsupported message")
	if !codexObject(data) {
		return CodexMessage{}, invalid
	}
	var envelope codexEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return CodexMessage{}, fmt.Errorf("codex protocol envelope: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return CodexMessage{}, invalid
	}
	if len(envelope.Method) > 0 {
		if len(envelope.ID) > 0 || len(envelope.Result) > 0 || len(envelope.Error) > 0 || !codexObject(envelope.Params) {
			return CodexMessage{}, invalid
		}
		var method CodexMethod
		if err := json.Unmarshal(envelope.Method, &method); err != nil {
			return CodexMessage{}, invalid
		}
		return decodeCodexNotification(method, envelope.Params)
	}
	if expected == nil || expected.ID.kind == 0 || !codexResponseMethod(expected.Method) || len(envelope.Params) > 0 || (len(envelope.Result) > 0) == (len(envelope.Error) > 0) {
		return CodexMessage{}, invalid
	}
	var id CodexRequestID
	if err := json.Unmarshal(envelope.ID, &id); err != nil || id != expected.ID {
		return CodexMessage{}, invalid
	}
	if len(envelope.Error) > 0 {
		var detail struct {
			Code    *int64  `json:"code"`
			Message *string `json:"message"`
		}
		if !codexObject(envelope.Error) || json.Unmarshal(envelope.Error, &detail) != nil || detail.Code == nil || detail.Message == nil {
			return CodexMessage{}, invalid
		}
		return CodexMessage{}, &CodexRPCErrorResponse{id, CodexRPCErrorDetail{*detail.Code, *detail.Message}}
	}
	if !codexObject(envelope.Result) {
		return CodexMessage{}, invalid
	}
	response := &CodexRPCSuccessResponse{ID: id, Method: expected.Method}
	switch expected.Method {
	case CodexInitialize:
		var result CodexInitializeResponse
		if json.Unmarshal(envelope.Result, &result) != nil || result.UserAgent == "" || result.CodexHome == "" || result.PlatformFamily == "" || result.PlatformOS == "" {
			return CodexMessage{}, invalid
		}
		response.Initialize = &result
	case CodexThreadStart:
		var result CodexThreadStartResponse
		if json.Unmarshal(envelope.Result, &result) != nil || !validCodexThread(result.Thread) || result.Cwd != codexProtocolWorkspace {
			return CodexMessage{}, invalid
		}
		response.ThreadStart = &result
	case CodexTurnStart:
		var result CodexTurnStartResponse
		if json.Unmarshal(envelope.Result, &result) != nil || !validCodexTurn(result.Turn) || result.Turn.Status != CodexTurnInProgress {
			return CodexMessage{}, invalid
		}
		response.TurnStart = &result
	}
	return CodexMessage{Response: response}, nil
}

func codexResponseMethod(method CodexMethod) bool {
	return method == CodexInitialize || method == CodexThreadStart || method == CodexTurnStart
}

func validCodexThread(thread CodexThread) bool {
	return thread.ID != "" && thread.Cwd == codexProtocolWorkspace
}

func validCodexTurn(turn CodexTurn) bool {
	if turn.ID == "" || (turn.Error != nil && turn.Error.Message == "") {
		return false
	}
	switch turn.Status {
	case CodexTurnInProgress, CodexTurnCompletedStatus, CodexTurnFailed, CodexTurnInterrupted:
		return true
	default:
		return false
	}
}

func decodeCodexNotification(method CodexMethod, params []byte) (CodexMessage, error) {
	invalid := errors.New("codex protocol: malformed or unsupported notification")
	notification := &CodexRPCNotification{Method: method}
	switch method {
	case CodexThreadStarted:
		var event CodexThreadStartedNotification
		if json.Unmarshal(params, &event) != nil || !validCodexThread(event.Thread) {
			return CodexMessage{}, invalid
		}
		notification.ThreadStarted = &event
	case CodexTurnStarted, CodexTurnCompleted:
		var event CodexTurnNotification
		if json.Unmarshal(params, &event) != nil || event.ThreadID == "" || !validCodexTurn(event.Turn) {
			return CodexMessage{}, invalid
		}
		if (method == CodexTurnStarted) != (event.Turn.Status == CodexTurnInProgress) {
			return CodexMessage{}, invalid
		}
		notification.Turn = &event
	case CodexAgentMessageDelta:
		var event struct {
			ThreadID string  `json:"threadId"`
			TurnID   string  `json:"turnId"`
			ItemID   string  `json:"itemId"`
			Delta    *string `json:"delta"`
		}
		if json.Unmarshal(params, &event) != nil || event.ThreadID == "" || event.TurnID == "" || event.ItemID == "" || event.Delta == nil {
			return CodexMessage{}, invalid
		}
		notification.AgentMessageDelta = &CodexAgentMessageDeltaNotification{event.ThreadID, event.TurnID, event.ItemID, *event.Delta}
	default:
		return CodexMessage{}, invalid
	}
	return CodexMessage{Notification: notification}, nil
}
