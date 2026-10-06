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
// Thread responses validate the full contract before identity projection. Turn/item notifications validate
// the pinned wire contract before retaining only execution/correlation data.
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
	CodexItemStarted       CodexMethod = "item/started"
	CodexItemCompleted     CodexMethod = "item/completed"
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

// Validate provider metadata as data before discarding it. None of the policy,
// source or path fields below grants authority to the client.
func (thread *CodexThread) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID             string  `json:"id"`
		SessionID      *string `json:"sessionId"`
		ForkedFromID   *string `json:"forkedFromId"`
		ParentThreadID *string `json:"parentThreadId"`
		Preview        *string `json:"preview"`
		Ephemeral      *bool   `json:"ephemeral"`
		Section        *struct {
			ID         *string `json:"id"`
			Name       *string `json:"name"`
			Appearance *struct {
				Icon  *string `json:"icon"`
				Color *string `json:"color"`
			} `json:"appearance"`
		} `json:"section"`
		SectionEnteredAt *int64                      `json:"sectionEnteredAt"`
		ProjectID        codexRequiredNullableString `json:"projectId"`
		HistoryMode      codexJSONString             `json:"historyMode"`
		ModelProvider    *string                     `json:"modelProvider"`
		Model            *string                     `json:"model"`
		ReasoningEffort  *string                     `json:"reasoningEffort"`
		CreatedAt        *int64                      `json:"createdAt"`
		UpdatedAt        *int64                      `json:"updatedAt"`
		RecencyAt        *int64                      `json:"recencyAt"`
		Status           *codexThreadStatus          `json:"status"`
		Path             *string                     `json:"path"`
		Cwd              string                      `json:"cwd"`
		CLIVersion       *string                     `json:"cliVersion"`
		Originator       *string                     `json:"originator"`
		Source           *codexSessionSource         `json:"source"`
		ThreadSource     *string                     `json:"threadSource"`
		AgentNickname    *string                     `json:"agentNickname"`
		AgentRole        *string                     `json:"agentRole"`
		GitInfo          *struct {
			SHA       *string `json:"sha"`
			Branch    *string `json:"branch"`
			OriginURL *string `json:"originUrl"`
		} `json:"gitInfo"`
		Name  *string                `json:"name"`
		Turns *codexArray[CodexTurn] `json:"turns"`
	}
	wire.HistoryMode = "legacy"
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	decoded := CodexThread{ID: wire.ID, Cwd: wire.Cwd}
	if !validCodexThread(decoded) || wire.SessionID == nil || wire.Preview == nil || wire.Ephemeral == nil ||
		!wire.ProjectID.present || wire.ModelProvider == nil || wire.CreatedAt == nil || wire.UpdatedAt == nil ||
		wire.Status == nil || wire.CLIVersion == nil || wire.Source == nil || wire.Turns == nil ||
		!codexOneOf(string(wire.HistoryMode), "legacy", "paginated") ||
		(wire.Section != nil && (wire.Section.ID == nil || wire.Section.Name == nil)) {
		return errors.New("codex protocol: invalid thread")
	}
	*thread = decoded
	return nil
}

// projectId is required but nullable in the pinned JSON schema.
type codexRequiredNullableString struct{ present bool }

func (value *codexRequiredNullableString) UnmarshalJSON(data []byte) error {
	var text *string
	if err := decodeCodexStrictValue(data, &text); err != nil {
		return err
	}
	value.present = true
	return nil
}

type codexThreadStatus struct{}

func (*codexThreadStatus) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type        codexJSONString             `json:"type"`
		ActiveFlags codexArray[codexJSONString] `json:"activeFlags"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Type == "active" {
		if wire.ActiveFlags == nil {
			return errors.New("codex protocol: missing active flags")
		}
		for _, flag := range wire.ActiveFlags {
			if !codexOneOf(string(flag), "waitingOnApproval", "waitingOnUserInput") {
				return errors.New("codex protocol: invalid active flag")
			}
		}
	} else if !codexOneOf(string(wire.Type), "notLoaded", "idle", "systemError") || wire.ActiveFlags != nil {
		return errors.New("codex protocol: invalid thread status")
	}
	return nil
}

type codexSessionSource struct{}

func (*codexSessionSource) UnmarshalJSON(data []byte) error {
	if !codexObject(data) {
		var name codexJSONString
		if decodeCodexStrictValue(data, &name) != nil || !codexOneOf(string(name), "cli", "vscode", "exec", "appServer", "unknown") {
			return errors.New("codex protocol: invalid session source")
		}
		return nil
	}
	var wire struct {
		Custom   codexJSONString     `json:"custom"`
		SubAgent codexSubAgentSource `json:"subAgent"`
	}
	// Presence metadata rejects multiple tags, even if one value is null.
	var tags struct {
		Custom   json.RawMessage `json:"custom"`
		SubAgent json.RawMessage `json:"subAgent"`
	}
	if decodeCodexStrictObject(data, &tags) != nil || (len(tags.Custom) == 0) == (len(tags.SubAgent) == 0) {
		return errors.New("codex protocol: invalid session source tags")
	}
	return decodeCodexStrictObject(data, &wire)
}

type codexSubAgentSource struct{}

func (*codexSubAgentSource) UnmarshalJSON(data []byte) error {
	if !codexObject(data) {
		var name codexJSONString
		if decodeCodexStrictValue(data, &name) != nil || !codexOneOf(string(name), "review", "compact", "memory_consolidation") {
			return errors.New("codex protocol: invalid subagent source")
		}
		return nil
	}
	var tags struct {
		Spawn json.RawMessage `json:"thread_spawn"`
		Other json.RawMessage `json:"other"`
	}
	if decodeCodexStrictObject(data, &tags) != nil || (len(tags.Spawn) == 0) == (len(tags.Other) == 0) {
		return errors.New("codex protocol: invalid subagent source tags")
	}
	var wire struct {
		Spawn *struct {
			ParentThreadID *string `json:"parent_thread_id"`
			Depth          *int32  `json:"depth"`
			AgentPath      *string `json:"agent_path"`
			AgentNickname  *string `json:"agent_nickname"`
			AgentRole      *string `json:"agent_role"`
		} `json:"thread_spawn"`
		Other codexJSONString `json:"other"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if len(tags.Spawn) != 0 && (wire.Spawn == nil || wire.Spawn.ParentThreadID == nil || wire.Spawn.Depth == nil) {
		return errors.New("codex protocol: invalid thread spawn source")
	}
	return nil
}

func (response *CodexThreadStartResponse) UnmarshalJSON(data []byte) error {
	var wire struct {
		Thread             *CodexThread                `json:"thread"`
		Model              *string                     `json:"model"`
		ModelProvider      *string                     `json:"modelProvider"`
		ServiceTier        *string                     `json:"serviceTier"`
		DisabledPluginIDs  codexArray[codexJSONString] `json:"disabledPluginIds"`
		Cwd                string                      `json:"cwd"`
		InstructionSources codexArray[codexJSONString] `json:"instructionSources"`
		ApprovalPolicy     *codexApprovalPolicy        `json:"approvalPolicy"`
		ApprovalsReviewer  codexJSONString             `json:"approvalsReviewer"`
		Sandbox            *codexSandboxPolicy         `json:"sandbox"`
		ReasoningEffort    *string                     `json:"reasoningEffort"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Thread == nil || wire.Model == nil || wire.ModelProvider == nil || wire.Cwd != codexProtocolWorkspace ||
		wire.ApprovalPolicy == nil || wire.Sandbox == nil || !codexOneOf(string(wire.ApprovalsReviewer), "user", "auto_review", "guardian_subagent") {
		return errors.New("codex protocol: invalid thread start result")
	}
	*response = CodexThreadStartResponse{Thread: *wire.Thread, Cwd: wire.Cwd}
	return nil
}

type codexApprovalPolicy struct{}

func (*codexApprovalPolicy) UnmarshalJSON(data []byte) error {
	if !codexObject(data) {
		var name codexJSONString
		if decodeCodexStrictValue(data, &name) != nil || !codexOneOf(string(name), "untrusted", "on-request", "never") {
			return errors.New("codex protocol: invalid approval policy")
		}
		return nil
	}
	var wire struct {
		Granular *struct {
			SandboxApproval    *bool         `json:"sandbox_approval"`
			Rules              *bool         `json:"rules"`
			SkillApproval      codexJSONBool `json:"skill_approval"`
			RequestPermissions codexJSONBool `json:"request_permissions"`
			MCPElicitations    *bool         `json:"mcp_elicitations"`
		} `json:"granular"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Granular == nil || wire.Granular.SandboxApproval == nil || wire.Granular.Rules == nil || wire.Granular.MCPElicitations == nil {
		return errors.New("codex protocol: invalid granular approval policy")
	}
	return nil
}

type codexJSONBool bool

func (value *codexJSONBool) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if !bytes.Equal(data, []byte("true")) && !bytes.Equal(data, []byte("false")) {
		return errors.New("codex protocol: boolean required")
	}
	*value = codexJSONBool(bytes.Equal(data, []byte("true")))
	return nil
}

type codexSandboxPolicy struct{}

func (*codexSandboxPolicy) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type codexJSONString `json:"type"`
	}
	// Discrimination only; the selected variant is strictly decoded below.
	if !codexObject(data) || json.Unmarshal(data, &tag) != nil {
		return errors.New("codex protocol: invalid sandbox policy")
	}
	switch tag.Type {
	case "dangerFullAccess":
		return decodeCodexStrictObject(data, &tag)
	case "readOnly":
		var wire struct {
			Type          codexJSONString `json:"type"`
			NetworkAccess codexJSONBool   `json:"networkAccess"`
		}
		return decodeCodexStrictObject(data, &wire)
	case "externalSandbox":
		var wire struct {
			Type          codexJSONString `json:"type"`
			NetworkAccess codexJSONString `json:"networkAccess"`
		}
		wire.NetworkAccess = "restricted"
		if err := decodeCodexStrictObject(data, &wire); err != nil {
			return err
		}
		if !codexOneOf(string(wire.NetworkAccess), "restricted", "enabled") {
			return errors.New("codex protocol: invalid network access")
		}
		return nil
	case "workspaceWrite":
		var wire struct {
			Type                codexJSONString             `json:"type"`
			WritableRoots       codexArray[codexJSONString] `json:"writableRoots"`
			NetworkAccess       codexJSONBool               `json:"networkAccess"`
			ExcludeTmpdirEnvVar codexJSONBool               `json:"excludeTmpdirEnvVar"`
			ExcludeSlashTmp     codexJSONBool               `json:"excludeSlashTmp"`
		}
		return decodeCodexStrictObject(data, &wire)
	default:
		return errors.New("codex protocol: invalid sandbox policy type")
	}
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
	ID     string            `json:"id"`
	Status CodexTurnStatus   `json:"status"`
	Error  *CodexTurnError   `json:"error"`
	Items  []CodexThreadItem `json:"items"`
}

func (turn *CodexTurn) UnmarshalJSON(data []byte) error {
	var wire struct {
		ID          string             `json:"id"`
		Status      CodexTurnStatus    `json:"status"`
		Items       *[]CodexThreadItem `json:"items"`
		ItemsView   codexJSONString    `json:"itemsView"`
		Error       *CodexTurnError    `json:"error"`
		StartedAt   *int64             `json:"startedAt"`
		CompletedAt *int64             `json:"completedAt"`
		DurationMs  *int64             `json:"durationMs"`
	}
	wire.ItemsView = "full" // Official serde default; items has no default.
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Items == nil || !codexOneOf(string(wire.ItemsView), "notLoaded", "summary", "full") {
		return errors.New("codex protocol: invalid turn")
	}
	decoded := CodexTurn{ID: wire.ID, Status: wire.Status, Error: wire.Error, Items: *wire.Items}
	if !validCodexTurn(decoded) {
		return errors.New("codex protocol: invalid turn")
	}
	*turn = decoded
	return nil
}

func (detail *CodexTurnError) UnmarshalJSON(data []byte) error {
	var wire struct {
		Message           *string         `json:"message"`
		CodexErrorInfo    *codexErrorInfo `json:"codexErrorInfo"`
		AdditionalDetails *string         `json:"additionalDetails"`
		Misalignment      *struct {
			ErrorType           *string `json:"errorType"`
			DetailedExplanation *string `json:"detailedExplanation"`
			Steer               *struct {
				Message *string `json:"message"`
			} `json:"steer"`
		} `json:"misalignment"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Message == nil || *wire.Message == "" || (wire.Misalignment != nil && wire.Misalignment.Steer != nil && wire.Misalignment.Steer.Message == nil) {
		return errors.New("codex protocol: invalid turn error")
	}
	*detail = CodexTurnError{Message: *wire.Message}
	return nil
}

// CodexErrorInfo is a pinned string/object union. Details are validated and
// discarded; they cannot change the result or initiate a continuation.
type codexErrorInfo struct{}

func (*codexErrorInfo) UnmarshalJSON(data []byte) error {
	invalid := errors.New("codex protocol: invalid error info")
	if !codexObject(data) {
		var name codexJSONString
		if json.Unmarshal(data, &name) != nil || !codexOneOf(string(name),
			"contextWindowExceeded", "sessionBudgetExceeded", "usageLimitExceeded", "rateLimitExceeded",
			"flexUnavailable", "serverOverloaded", "cyberPolicy", "misalignmentPolicyViolation", "tooManyDenials",
			"internalServerError", "unauthorized", "badRequest", "threadRollbackFailed", "sandboxError", "other") {
			return invalid
		}
		return nil
	}
	// Externally tagged objects have exactly one variant key, including when a
	// sibling value is null. Raw fields are only discriminator presence metadata;
	// the selected object is immediately decoded below into the typed contract.
	var tags struct {
		HTTPConnectionFailed           json.RawMessage `json:"httpConnectionFailed"`
		ResponseStreamConnectionFailed json.RawMessage `json:"responseStreamConnectionFailed"`
		ResponseStreamDisconnected     json.RawMessage `json:"responseStreamDisconnected"`
		ResponseTooManyFailedAttempts  json.RawMessage `json:"responseTooManyFailedAttempts"`
		ActiveTurnNotSteerable         json.RawMessage `json:"activeTurnNotSteerable"`
	}
	if err := decodeCodexStrictObject(data, &tags); err != nil {
		return err
	}
	keys := 0
	for _, tag := range []json.RawMessage{tags.HTTPConnectionFailed, tags.ResponseStreamConnectionFailed, tags.ResponseStreamDisconnected, tags.ResponseTooManyFailedAttempts, tags.ActiveTurnNotSteerable} {
		if len(tag) != 0 {
			keys++
		}
	}
	if keys != 1 {
		return invalid
	}
	type httpFailure struct {
		HTTPStatusCode *uint16 `json:"httpStatusCode"`
	}
	var wire struct {
		HTTPConnectionFailed           *httpFailure `json:"httpConnectionFailed"`
		ResponseStreamConnectionFailed *httpFailure `json:"responseStreamConnectionFailed"`
		ResponseStreamDisconnected     *httpFailure `json:"responseStreamDisconnected"`
		ResponseTooManyFailedAttempts  *httpFailure `json:"responseTooManyFailedAttempts"`
		ActiveTurnNotSteerable         *struct {
			TurnKind codexJSONString `json:"turnKind"`
		} `json:"activeTurnNotSteerable"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	count := 0
	for _, failure := range []*httpFailure{wire.HTTPConnectionFailed, wire.ResponseStreamConnectionFailed, wire.ResponseStreamDisconnected, wire.ResponseTooManyFailedAttempts} {
		if failure != nil {
			count++
		}
	}
	if wire.ActiveTurnNotSteerable != nil {
		count++
		if !codexOneOf(string(wire.ActiveTurnNotSteerable.TurnKind), "review", "compact") {
			return invalid
		}
	}
	if count != 1 {
		return invalid
	}
	return nil
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
	EmittedAtMs       *int64
	ThreadStarted     *CodexThreadStartedNotification
	Turn              *CodexTurnNotification
	AgentMessageDelta *CodexAgentMessageDeltaNotification
	ItemStarted       *CodexItemStartedNotification
	ItemCompleted     *CodexItemCompletedNotification
}

// Only the agentMessage variant is needed by this text-output boundary.
// Ancillary fields are decoded strictly, but only identity/text are retained.
type CodexAgentMessageItem struct {
	ID   string
	Text string
}

type CodexItemType string

const (
	CodexAgentMessageItemType      CodexItemType = "agentMessage"
	CodexUserMessageItemType       CodexItemType = "userMessage"
	CodexPlanItemType              CodexItemType = "plan"
	CodexReasoningItemType         CodexItemType = "reasoning"
	CodexCommandExecutionItemType  CodexItemType = "commandExecution"
	CodexFileChangeItemType        CodexItemType = "fileChange"
	CodexImageViewItemType         CodexItemType = "imageView"
	CodexSleepItemType             CodexItemType = "sleep"
	CodexContextCompactionItemType CodexItemType = "contextCompaction"
)

// Pinned ThreadItem inventory: agentMessage is OUTPUT. userMessage, plan,
// reasoning, commandExecution, fileChange, imageView, sleep, contextCompaction
// are LIFECYCLE_ONLY. These are provider data, never instructions for this client.
// hookPrompt, functionCallOutput, mcpToolCall, dynamicToolCall, collabAgentToolCall,
// subAgentActivity, webSearch, imageGeneration, enteredReviewMode, exitedReviewMode
// require unused feature/capability contracts and remain UNSUPPORTED.
type CodexThreadItem struct {
	Type CodexItemType
	ID   string
	Text string // Only agentMessage snapshots; deltas remain the output source.
}

type codexItemIdentity struct {
	Type codexJSONString `json:"type"`
	ID   codexJSONString `json:"id"`
}

func (item *CodexThreadItem) UnmarshalJSON(data []byte) error {
	invalid := errors.New("codex protocol: invalid or unsupported thread item")
	// This first-stage discriminator is immediately followed by a strict typed
	// decode of the whole object; no free-form payload is retained.
	var identity codexItemIdentity
	if !codexObject(data) || json.Unmarshal(data, &identity) != nil || identity.ID == "" {
		return invalid
	}
	typ := CodexItemType(identity.Type)
	decoded := CodexThreadItem{Type: typ, ID: string(identity.ID)}
	switch typ {
	case CodexAgentMessageItemType:
		var agent CodexAgentMessageItem
		if err := json.Unmarshal(data, &agent); err != nil {
			return err
		}
		decoded.Text = agent.Text
	case CodexPlanItemType:
		var wire struct {
			codexItemIdentity
			Text *string `json:"text"`
		}
		if decodeCodexStrictObject(data, &wire) != nil || wire.Text == nil {
			return invalid
		}
	case CodexUserMessageItemType:
		var wire struct {
			codexItemIdentity
			ClientID *string                    `json:"clientId"`
			Content  *codexArray[codexUserText] `json:"content"`
		}
		if decodeCodexStrictObject(data, &wire) != nil || wire.Content == nil {
			return invalid
		}
	case CodexReasoningItemType:
		var wire struct {
			codexItemIdentity
			Summary codexArray[codexJSONString] `json:"summary"`
			Content codexArray[codexJSONString] `json:"content"`
		}
		if err := decodeCodexStrictObject(data, &wire); err != nil {
			return err
		}
	case CodexCommandExecutionItemType:
		var wire struct {
			codexItemIdentity
			Command          *string                         `json:"command"`
			Cwd              *string                         `json:"cwd"`
			Source           codexJSONString                 `json:"source"`
			Status           codexJSONString                 `json:"status"`
			CommandActions   *codexArray[codexCommandAction] `json:"commandActions"`
			PluginID         *string                         `json:"pluginId"`
			ScriptPath       *string                         `json:"scriptPath"`
			ProcessID        *string                         `json:"processId"`
			AggregatedOutput *string                         `json:"aggregatedOutput"`
			ExitCode         *int32                          `json:"exitCode"`
			DurationMs       *int64                          `json:"durationMs"`
		}
		wire.Source = "agent"
		if decodeCodexStrictObject(data, &wire) != nil || wire.Command == nil || wire.Cwd == nil || wire.CommandActions == nil ||
			!codexOneOf(string(wire.Source), "agent", "userShell", "unifiedExecStartup", "unifiedExecInteraction") || !validCodexItemStatus(string(wire.Status)) {
			return invalid
		}
	case CodexFileChangeItemType:
		var wire struct {
			codexItemIdentity
			Status  codexJSONString              `json:"status"`
			Changes *codexArray[codexFileUpdate] `json:"changes"`
		}
		if decodeCodexStrictObject(data, &wire) != nil || wire.Changes == nil || !validCodexItemStatus(string(wire.Status)) {
			return invalid
		}
	case CodexImageViewItemType:
		var wire struct {
			codexItemIdentity
			Path *string `json:"path"`
		}
		if decodeCodexStrictObject(data, &wire) != nil || wire.Path == nil {
			return invalid
		}
	case CodexSleepItemType:
		var wire struct {
			codexItemIdentity
			DurationMs *uint64 `json:"durationMs"`
		}
		if decodeCodexStrictObject(data, &wire) != nil || wire.DurationMs == nil {
			return invalid
		}
	case CodexContextCompactionItemType:
		if err := decodeCodexStrictObject(data, &identity); err != nil {
			return err
		}
	default:
		return invalid
	}
	*item = decoded
	return nil
}

func codexOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validCodexItemStatus(status string) bool {
	return codexOneOf(status, "inProgress", "completed", "failed", "declined")
}

// Typed array fields may have official absent/default semantics, but null is
// never an array. Element custom decoders enforce their own nested contracts.
type codexArray[T any] []T

func (array *codexArray[T]) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '[' {
		return errors.New("codex protocol: array required")
	}
	return decodeCodexStrictValue(data, (*[]T)(array))
}

// The current runtime submits plain text only. Other UserInput capabilities
// (skills, mentions, media) require separate contracts and fail closed.
type codexUserText struct{}

func (*codexUserText) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type         codexJSONString `json:"type"`
		Text         *string         `json:"text"`
		TextElements codexArray[struct {
			ByteRange *struct {
				Start *uint64 `json:"start"`
				End   *uint64 `json:"end"`
			} `json:"byteRange"`
			Placeholder *string `json:"placeholder"`
		}] `json:"text_elements"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Type != "text" || wire.Text == nil {
		return errors.New("codex protocol: invalid user text")
	}
	for _, element := range wire.TextElements {
		if element.ByteRange == nil || element.ByteRange.Start == nil || element.ByteRange.End == nil {
			return errors.New("codex protocol: invalid text element")
		}
	}
	return nil
}

type codexCommandAction struct{}

func (*codexCommandAction) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type codexJSONString `json:"type"`
	}
	if !codexObject(data) || json.Unmarshal(data, &tag) != nil {
		return errors.New("codex protocol: invalid command action")
	}
	// Each selected variant owns its allowed fields, including optional paths.
	var command *string
	switch tag.Type {
	case "read":
		var wire struct {
			Type    codexJSONString `json:"type"`
			Command *string         `json:"command"`
			Name    *string         `json:"name"`
			Path    *string         `json:"path"`
		}
		if decodeCodexStrictObject(data, &wire) != nil || wire.Name == nil || wire.Path == nil {
			return errors.New("codex protocol: invalid read action")
		}
		command = wire.Command
	case "listFiles":
		var wire struct {
			Type    codexJSONString `json:"type"`
			Command *string         `json:"command"`
			Path    *string         `json:"path"`
		}
		if err := decodeCodexStrictObject(data, &wire); err != nil {
			return err
		}
		command = wire.Command
	case "search":
		var wire struct {
			Type    codexJSONString `json:"type"`
			Command *string         `json:"command"`
			Path    *string         `json:"path"`
			Query   *string         `json:"query"`
		}
		if err := decodeCodexStrictObject(data, &wire); err != nil {
			return err
		}
		command = wire.Command
	case "unknown": // Official action variant, not an unknown ThreadItem type.
		var wire struct {
			Type    codexJSONString `json:"type"`
			Command *string         `json:"command"`
		}
		if err := decodeCodexStrictObject(data, &wire); err != nil {
			return err
		}
		command = wire.Command
	default:
		return errors.New("codex protocol: unsupported command action")
	}
	if command == nil {
		return errors.New("codex protocol: command data required")
	}
	return nil
}

type codexFileUpdate struct{}

func (*codexFileUpdate) UnmarshalJSON(data []byte) error {
	var wire struct {
		Path *string         `json:"path"`
		Kind *codexPatchKind `json:"kind"`
		Diff *string         `json:"diff"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	if wire.Path == nil || wire.Kind == nil || wire.Diff == nil {
		return errors.New("codex protocol: invalid file update")
	}
	return nil
}

type codexPatchKind struct{}

func (*codexPatchKind) UnmarshalJSON(data []byte) error {
	var tag struct {
		Type codexJSONString `json:"type"`
	}
	if !codexObject(data) || json.Unmarshal(data, &tag) != nil {
		return errors.New("codex protocol: invalid patch kind")
	}
	switch tag.Type {
	case "add", "delete":
		return decodeCodexStrictObject(data, &tag)
	case "update":
		var wire struct {
			Type     codexJSONString `json:"type"`
			MovePath *string         `json:"move_path"`
		}
		return decodeCodexStrictObject(data, &wire)
	default:
		return errors.New("codex protocol: unsupported patch kind")
	}
}

func (item *CodexAgentMessageItem) UnmarshalJSON(data []byte) error {
	var wire struct {
		Type           string  `json:"type"`
		ID             string  `json:"id"`
		Text           *string `json:"text"`
		Phase          *string `json:"phase"`
		Delivery       *string `json:"delivery"`
		MemoryCitation *struct {
			Entries []struct {
				Path      *string `json:"path"`
				LineStart *uint32 `json:"lineStart"`
				LineEnd   *uint32 `json:"lineEnd"`
				Note      *string `json:"note"`
			} `json:"entries"`
			ThreadIDs []codexJSONString `json:"threadIds"`
		} `json:"memoryCitation"`
		Questions *[]struct {
			Title   *string            `json:"title"`
			Options *[]codexJSONString `json:"options"`
		} `json:"questions"`
	}
	if err := decodeCodexStrictObject(data, &wire); err != nil {
		return err
	}
	invalid := errors.New("codex protocol: invalid agent message item")
	if wire.Type != "agentMessage" || wire.ID == "" || wire.Text == nil ||
		(wire.Phase != nil && *wire.Phase != "commentary" && *wire.Phase != "final_answer") ||
		(wire.Delivery != nil && *wire.Delivery != "async") {
		return invalid
	}
	if wire.MemoryCitation != nil {
		if wire.MemoryCitation.Entries == nil || wire.MemoryCitation.ThreadIDs == nil {
			return invalid
		}
		for _, entry := range wire.MemoryCitation.Entries {
			if entry.Path == nil || entry.LineStart == nil || entry.LineEnd == nil || entry.Note == nil {
				return invalid
			}
		}
	}
	if wire.Questions != nil {
		for _, question := range *wire.Questions {
			if question.Title == nil {
				return invalid
			}
		}
	}
	*item = CodexAgentMessageItem{ID: wire.ID, Text: *wire.Text}
	return nil
}

// encoding/json otherwise accepts null as a string slice element.
type codexJSONString string

func (value *codexJSONString) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '"' {
		return errors.New("codex protocol: string required")
	}
	return json.Unmarshal(data, (*string)(value))
}

type CodexItemNotification struct {
	ThreadID string           `json:"threadId"`
	TurnID   string           `json:"turnId"`
	Item     *CodexThreadItem `json:"item"`
}

type CodexItemStartedNotification struct {
	CodexItemNotification
	StartedAtMs *int64 `json:"startedAtMs"`
}

type CodexItemCompletedNotification struct {
	CodexItemNotification
	CompletedAtMs *int64 `json:"completedAtMs"`
}

func validCodexItemNotification(event CodexItemNotification) bool {
	return event.ThreadID != "" && event.TurnID != "" && event.Item != nil
}

func decodeCodexStrictObject(data []byte, target any) error {
	if !codexObject(data) {
		return errors.New("codex protocol: object required")
	}
	return decodeCodexStrictValue(data, target)
}

func decodeCodexStrictValue(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("codex protocol: extra JSON")
	}
	return nil
}

// rust-v0.159.2 permits an absent/null optional i64 notification timestamp.
// Presence is tracked separately to reject this field on RPC responses, even null.
type codexNotificationTimestamp struct {
	present bool
	value   *int64
}

func (timestamp *codexNotificationTimestamp) UnmarshalJSON(data []byte) error {
	timestamp.present = true
	return json.Unmarshal(data, &timestamp.value)
}

type CodexMessage struct {
	Response     *CodexRPCSuccessResponse
	Notification *CodexRPCNotification
}

// RawMessage is confined to framing/discrimination; provider payloads are typed.
type codexEnvelope struct {
	ID          json.RawMessage            `json:"id"`
	Method      json.RawMessage            `json:"method"`
	Params      json.RawMessage            `json:"params"`
	Result      json.RawMessage            `json:"result"`
	Error       json.RawMessage            `json:"error"`
	EmittedAtMs codexNotificationTimestamp `json:"emittedAtMs"`
}

func codexObject(data []byte) bool {
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '{'
}

// Correlation is shared by every RPC consumer, independently of its result
// schema. A server request is never a notification. Unexpected response IDs
// fail closed because this client has only one outstanding request at a time.
func decodeCodexCorrelatedEnvelope(data []byte, expectedID *CodexRequestID) (codexEnvelope, bool, error) {
	invalid := errors.New("codex protocol: malformed or uncorrelated envelope")
	var envelope codexEnvelope
	if decodeCodexStrictObject(data, &envelope) != nil {
		return codexEnvelope{}, false, invalid
	}
	if len(envelope.Method) > 0 {
		var method string
		if len(envelope.ID) > 0 || len(envelope.Result) > 0 || len(envelope.Error) > 0 || !codexObject(envelope.Params) || json.Unmarshal(envelope.Method, &method) != nil || method == "" {
			return codexEnvelope{}, false, invalid
		}
		return envelope, true, nil
	}
	if envelope.EmittedAtMs.present || expectedID == nil || expectedID.kind == 0 || len(envelope.Params) > 0 || (len(envelope.Result) > 0) == (len(envelope.Error) > 0) {
		return codexEnvelope{}, false, invalid
	}
	var id CodexRequestID
	if json.Unmarshal(envelope.ID, &id) != nil || id != *expectedID || len(envelope.Result) > 0 && !codexObject(envelope.Result) {
		return codexEnvelope{}, false, invalid
	}
	return envelope, false, nil
}

// DecodeCodexMessage decodes one message, never a stream or a server request.
// Unknown notifications fail closed; none are silently treated as completion.
// The caller owns outstanding-request lifecycle and supplies correlation metadata.
func DecodeCodexMessage(data []byte, expected *CodexResponseExpectation) (CodexMessage, error) {
	invalid := errors.New("codex protocol: malformed or unsupported message")
	var expectedID *CodexRequestID
	if expected != nil {
		expectedID = &expected.ID
	}
	envelope, notification, err := decodeCodexCorrelatedEnvelope(data, expectedID)
	if err != nil {
		return CodexMessage{}, err
	}
	if notification {
		var method CodexMethod
		if err := json.Unmarshal(envelope.Method, &method); err != nil {
			return CodexMessage{}, invalid
		}
		message, err := decodeCodexNotification(method, envelope.Params)
		if err == nil {
			message.Notification.EmittedAtMs = envelope.EmittedAtMs.value
		}
		return message, err
	}
	if expected == nil || !codexResponseMethod(expected.Method) {
		return CodexMessage{}, invalid
	}
	id := expected.ID
	if len(envelope.Error) > 0 {
		var detail struct {
			Code    *int64  `json:"code"`
			Message *string `json:"message"`
			// The official schema explicitly permits any JSON in data. Validate
			// framing, then discard it; never expose it in the projected error.
			Data json.RawMessage `json:"data"`
		}
		if decodeCodexStrictObject(envelope.Error, &detail) != nil || detail.Code == nil || detail.Message == nil {
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
		if decodeCodexStrictObject(envelope.Result, &result) != nil || result.UserAgent == "" || result.CodexHome == "" || result.PlatformFamily == "" || result.PlatformOS == "" {
			return CodexMessage{}, invalid
		}
		response.Initialize = &result
	case CodexThreadStart:
		var result CodexThreadStartResponse
		if decodeCodexStrictObject(envelope.Result, &result) != nil || !validCodexThread(result.Thread) || result.Cwd != codexProtocolWorkspace {
			return CodexMessage{}, invalid
		}
		response.ThreadStart = &result
	case CodexTurnStart:
		var result CodexTurnStartResponse
		if decodeCodexStrictObject(envelope.Result, &result) != nil || !validCodexTurn(result.Turn) || result.Turn.Status != CodexTurnInProgress {
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
	case CodexItemStarted:
		var event CodexItemStartedNotification
		if decodeCodexStrictObject(params, &event) != nil || !validCodexItemNotification(event.CodexItemNotification) || event.StartedAtMs == nil {
			return CodexMessage{}, invalid
		}
		notification.ItemStarted = &event
	case CodexItemCompleted:
		var event CodexItemCompletedNotification
		if decodeCodexStrictObject(params, &event) != nil || !validCodexItemNotification(event.CodexItemNotification) || event.CompletedAtMs == nil {
			return CodexMessage{}, invalid
		}
		notification.ItemCompleted = &event
	case CodexThreadStarted:
		var event CodexThreadStartedNotification
		if decodeCodexStrictObject(params, &event) != nil || !validCodexThread(event.Thread) {
			return CodexMessage{}, invalid
		}
		notification.ThreadStarted = &event
	case CodexTurnStarted, CodexTurnCompleted:
		var event CodexTurnNotification
		if decodeCodexStrictObject(params, &event) != nil || event.ThreadID == "" || !validCodexTurn(event.Turn) {
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
		if decodeCodexStrictObject(params, &event) != nil || event.ThreadID == "" || event.TurnID == "" || event.ItemID == "" || event.Delta == nil {
			return CodexMessage{}, invalid
		}
		notification.AgentMessageDelta = &CodexAgentMessageDeltaNotification{event.ThreadID, event.TurnID, event.ItemID, *event.Delta}
	default:
		return CodexMessage{}, invalid
	}
	return CodexMessage{Notification: notification}, nil
}
