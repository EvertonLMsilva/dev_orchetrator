// Package provider owns authentication bootstrap, independently of workspace
// routing and execution readiness. Wire contracts are pinned to Codex 0.159.2.
package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dev-orchestrator/internal/infrastructure"
)

type AuthState string

const (
	Unauthenticated AuthState = "UNAUTHENTICATED"
	LoginPending    AuthState = "LOGIN_PENDING"
	Authenticated   AuthState = "AUTHENTICATED"
	AuthFailed      AuthState = "AUTH_FAILED"
)

// LoginAttempt requires deliberate accessor calls for administrative presentation.
// Formatting and JSON serialization never reveal its internal or sensitive data.
type LoginAttempt struct{ loginID, verificationURL, userCode string }

func (a LoginAttempt) LoginID() string          { return a.loginID }
func (a LoginAttempt) VerificationURL() string  { return a.verificationURL }
func (a LoginAttempt) UserCode() string         { return a.userCode }
func (LoginAttempt) Format(s fmt.State, _ rune) { io.WriteString(s, "LoginAttempt[redacted]") }

type LoginCompletion struct{ Success bool }
type LoginCancelResult string

const (
	LoginCanceled LoginCancelResult = "canceled"
	LoginNotFound LoginCancelResult = "notFound"
)

// DeviceCodeTransport must bound messages and honor context cancellation in both
// methods, unblocking all I/O before returning. It must never log wire payloads.
// This infrastructure contract does not start a process or acquire credentials.
type DeviceCodeTransport interface {
	Write(context.Context, []byte) error
	Read(context.Context) ([]byte, error)
}

// Operations are serialized. The timeout covers the whole attempt, including
// initialization; callers may also provide shorter per-operation deadlines.
type DeviceCodeBootstrap struct {
	mu          sync.Mutex
	transport   DeviceCodeTransport
	timeout     time.Duration
	deadline    time.Time
	state       AuthState
	attempt     LoginAttempt
	early       *completionWire
	diagnostic  BootstrapDiagnostics
	activeStage int
}

const (
	diagInitialize = iota
	diagInitialized
	diagLoginRPC
	diagDeviceRequest
	diagResponseDecode
	diagRPCError
	diagRemoteStatus
)

// Diagnostics retain only bounded classifications and integers, never messages,
// request/response payloads, account data or login presentation material.
type BootstrapDiagnostics struct {
	results        [7]string
	classification string
	rpcCode        int64
	hasRPCCode     bool
	httpStatus     int
	loginShapes    []LoginRPCShape
}

// LoginRPCShape retains presence/type/correlation facts only. Arbitrary keys,
// methods, IDs, URLs and values are never retained, even on malformed messages.
type LoginRPCShape struct {
	kind, resultKind, resultType, originValid                            string
	jsonValid, uniqueFields, hasID, idMatches, typePresent, errorPresent bool
	fields                                                               [4]bool
	topUnknown, resultUnknown                                            int
}

func diagnosticYes(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}
func (s LoginRPCShape) Lines() []string {
	kind := s.kind
	switch kind {
	case "response", "notification", "request":
	default:
		kind = "unknown"
	}
	result := s.resultKind
	switch result {
	case "object", "null", "other":
	default:
		result = "missing"
	}
	typ := s.resultType
	switch typ {
	case "apiKey", "chatgpt", "chatgptDeviceCode", "chatgptAuthTokens", "amazonBedrock":
	default:
		typ = "UNKNOWN"
	}
	origin := s.originValid
	if origin != "yes" && origin != "no" {
		origin = "unknown"
	}
	return []string{
		"message_kind=" + kind + " has_id=" + diagnosticYes(s.hasID) + " id_matches=" + diagnosticYes(s.idMatches),
		"json_valid=" + diagnosticYes(s.jsonValid) + " unique_fields=" + diagnosticYes(s.uniqueFields),
		"result_kind=" + result + " result_type_present=" + diagnosticYes(s.typePresent) + " result_type=" + typ + " error_present=" + diagnosticYes(s.errorPresent),
		"fields_present: type=" + diagnosticYes(s.fields[0]) + " loginId=" + diagnosticYes(s.fields[1]) + " verificationUrl=" + diagnosticYes(s.fields[2]) + " userCode=" + diagnosticYes(s.fields[3]),
		"verification_url_present=" + diagnosticYes(s.fields[2]) + " verification_url_origin_valid=" + origin,
		fmt.Sprintf("envelope_unknown_field_count=%d result_unknown_field_count=%d", max(0, s.topUnknown), max(0, s.resultUnknown)),
	}
}
func (s LoginRPCShape) Format(state fmt.State, _ rune) {
	io.WriteString(state, strings.Join(s.Lines(), "\n"))
}

func safeLoginRPCShape(data []byte, expectedID int) LoginRPCShape {
	s := LoginRPCShape{jsonValid: json.Valid(data)}
	if !s.jsonValid {
		return s
	}
	s.uniqueFields = uniqueValue(json.NewDecoder(bytes.NewReader(data))) == nil
	var top map[string]json.RawMessage
	if json.Unmarshal(data, &top) != nil || top == nil {
		return s
	}
	_, s.hasID = top["id"]
	_, s.errorPresent = top["error"]
	var id int64
	s.idMatches = s.hasID && !bytes.Equal(bytes.TrimSpace(top["id"]), []byte("null")) && json.Unmarshal(top["id"], &id) == nil && id == int64(expectedID)
	var method string
	methodPresent := json.Unmarshal(top["method"], &method) == nil && method != ""
	_, resultPresent := top["result"]
	if methodPresent {
		if s.hasID {
			s.kind = "request"
		} else {
			s.kind = "notification"
		}
	} else if s.hasID && (resultPresent != s.errorPresent) {
		s.kind = "response"
	}
	s.topUnknown = len(top)
	for _, name := range []string{"id", "method", "params", "result", "error", "emittedAtMs"} {
		if _, ok := top[name]; ok {
			s.topUnknown--
		}
	}
	raw := bytes.TrimSpace(top["result"])
	if !resultPresent {
		return s
	}
	s.resultKind = "other"
	if bytes.Equal(raw, []byte("null")) {
		s.resultKind = "null"
		return s
	}
	var result map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &result) != nil {
		return s
	}
	s.resultKind = "object"
	s.resultUnknown = len(result)
	for i, name := range []string{"type", "loginId", "verificationUrl", "userCode"} {
		_, s.fields[i] = result[name]
		if s.fields[i] {
			s.resultUnknown--
		}
	}
	s.typePresent = s.fields[0]
	var typ string
	if json.Unmarshal(result["type"], &typ) == nil {
		switch typ {
		case "apiKey", "chatgpt", "chatgptDeviceCode", "chatgptAuthTokens", "amazonBedrock":
			s.resultType = typ
		}
	}
	var verificationURL string
	if json.Unmarshal(result["verificationUrl"], &verificationURL) == nil && !bytes.Equal(bytes.TrimSpace(result["verificationUrl"]), []byte("null")) {
		s.originValid = "no"
		parsed, err := url.Parse(verificationURL)
		if err == nil && parsed.Scheme == "https" && parsed.Host == "auth.openai.com" && parsed.User == nil {
			s.originValid = "yes"
		}
	}
	return s
}

func (d BootstrapDiagnostics) Lines() []string {
	var lines []string
	for i, stage := range []string{"INITIALIZE", "INITIALIZED", "LOGIN_RPC_START", "DEVICE_CODE_REQUEST", "RESPONSE_DECODE", "RPC_ERROR", "REMOTE_STATUS"} {
		result := d.results[i]
		if result != "PASS" && result != "FAIL" {
			result = "UNKNOWN"
		}
		lines = append(lines, "stage="+stage+" result="+result)
	}
	class := d.classification
	switch class {
	case "RPC_ERROR", "REMOTE_STATUS", "DEVICE_CODE_NOT_ENABLED", "RESPONSE_DECODE", "TRANSPORT", "TIMEOUT_OR_CANCELLATION":
	default:
		class = "UNKNOWN"
	}
	lines = append(lines, "classification="+class)
	if d.hasRPCCode {
		lines = append(lines, fmt.Sprintf("rpc_code=%d", d.rpcCode))
	}
	if d.httpStatus >= 100 && d.httpStatus <= 599 {
		lines = append(lines, fmt.Sprintf("http_status=%d", d.httpStatus))
	}
	for i, shape := range d.loginShapes {
		lines = append(lines, fmt.Sprintf("login_rpc_message_index=%d", i))
		lines = append(lines, shape.Lines()...)
	}
	return lines
}

func (d BootstrapDiagnostics) Format(s fmt.State, _ rune) {
	io.WriteString(s, strings.Join(d.Lines(), "\n"))
}
func (b *DeviceCodeBootstrap) Diagnostics() BootstrapDiagnostics {
	b.mu.Lock()
	defer b.mu.Unlock()
	diagnostic := b.diagnostic
	diagnostic.loginShapes = append([]LoginRPCShape(nil), b.diagnostic.loginShapes...)
	return diagnostic
}

func (b *DeviceCodeBootstrap) diagnoseRPC(raw []byte, id int) {
	b.diagnostic.classification = "RPC_ERROR"
	b.diagnostic.results[diagRPCError] = "FAIL"
	var remote struct {
		Code    *int64  `json:"code"`
		Message *string `json:"message"`
	}
	if json.Unmarshal(raw, &remote) != nil || remote.Code == nil {
		return
	}
	b.diagnostic.rpcCode = *remote.Code
	b.diagnostic.hasRPCCode = true
	if id != 2 || remote.Message == nil {
		return
	}
	message := *remote.Message
	// Exact Codex 0.159.2 templates only. No substring searches or body parsing.
	if message == "device code login is not enabled for this Codex server. Use the browser login or verify the server URL." {
		b.diagnostic.classification = "DEVICE_CODE_NOT_ENABLED"
		b.diagnostic.httpStatus = 404
	} else {
		message = strings.TrimPrefix(message, "failed to request device code: ")
		for status := 400; status <= 599; status++ {
			reason := http.StatusText(status)
			if reason != "" && message == fmt.Sprintf("device code request failed with status %d %s", status, reason) {
				b.diagnostic.classification = "REMOTE_STATUS"
				b.diagnostic.httpStatus = status
				break
			}
		}
	}
	if b.diagnostic.httpStatus != 0 {
		b.diagnostic.results[diagDeviceRequest] = "FAIL"
		b.diagnostic.results[diagRemoteStatus] = "FAIL"
	}
}

func NewDeviceCodeBootstrap(t DeviceCodeTransport, timeout time.Duration) *DeviceCodeBootstrap {
	return &DeviceCodeBootstrap{transport: t, timeout: timeout, state: Unauthenticated, activeStage: -1}
}
func (*DeviceCodeBootstrap) Format(s fmt.State, _ rune) {
	io.WriteString(s, "DeviceCodeBootstrap[redacted]")
}
func (b *DeviceCodeBootstrap) State() AuthState { b.mu.Lock(); defer b.mu.Unlock(); return b.state }
func (b *DeviceCodeBootstrap) fail(kind string) error {
	if b.activeStage >= 0 {
		b.diagnostic.results[b.activeStage] = "FAIL"
	}
	switch kind {
	case "protocol", "correlation", "message limit":
		b.diagnostic.results[diagResponseDecode] = "FAIL"
		b.diagnostic.classification = "RESPONSE_DECODE"
	case "transport":
		b.diagnostic.classification = "TRANSPORT"
	case "timeout or cancellation":
		b.diagnostic.classification = "TIMEOUT_OR_CANCELLATION"
	}
	b.state = AuthFailed
	b.attempt = LoginAttempt{}
	b.early = nil
	return errors.New("device code bootstrap: " + kind)
}
func (b *DeviceCodeBootstrap) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, b.deadline)
}
func (b *DeviceCodeBootstrap) write(ctx context.Context, v any) error {
	if ctx.Err() != nil {
		return b.fail("timeout or cancellation")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return b.fail("protocol")
	}
	defer clear(data)
	if b.transport.Write(ctx, data) != nil {
		return b.fail("transport")
	}
	return nil
}

type rpcRequest struct {
	ID     int    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}
type envelope struct {
	ID          *int            `json:"id"`
	Method      string          `json:"method"`
	Params      json.RawMessage `json:"params"`
	Result      json.RawMessage `json:"result"`
	Error       json.RawMessage `json:"error"`
	EmittedAtMs *int64          `json:"emittedAtMs"`
}
type completionWire struct {
	LoginID    *string         `json:"loginId"`
	Success    *bool           `json:"success"`
	Error      json.RawMessage `json:"error"`
	Onboarding json.RawMessage `json:"onboardingEntrypoint"`
}

func strict(data []byte, v any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("protocol")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("protocol")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("protocol")
	}
	// Reject duplicate fields at every depth, including nullable values.
	d = json.NewDecoder(bytes.NewReader(data))
	return uniqueValue(d)
}
func uniqueValue(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("protocol")
			}
			seen[s] = true
		}
		if err := uniqueValue(d); err != nil {
			return err
		}
	}
	_, err = d.Token()
	return err
}
func (b *DeviceCodeBootstrap) read(ctx context.Context, id int) (json.RawMessage, error) {
	for count := 0; count < 128; count++ {
		if ctx.Err() != nil {
			return nil, b.fail("timeout or cancellation")
		}
		data, err := b.transport.Read(ctx)
		if err != nil {
			return nil, b.fail("transport")
		}
		if id == 2 && len(b.diagnostic.loginShapes) < 128 {
			b.diagnostic.loginShapes = append(b.diagnostic.loginShapes, safeLoginRPCShape(data, id))
		}
		var e envelope
		err = strict(data, &e)
		var fields map[string]json.RawMessage
		if err == nil {
			err = json.Unmarshal(data, &fields)
		}
		clear(data)
		if err != nil {
			return nil, b.fail("protocol")
		}
		if fields["method"] != nil {
			if fields["id"] != nil || e.Result != nil || e.Error != nil || e.Method != "account/login/completed" {
				return nil, b.fail("protocol")
			}
			var c completionWire
			if strict(e.Params, &c) != nil || c.LoginID == nil || *c.LoginID == "" || c.Success == nil || c.Error == nil || c.Onboarding == nil {
				return nil, b.fail("protocol")
			}
			var remote *string
			if json.Unmarshal(c.Error, &remote) != nil {
				return nil, b.fail("protocol")
			}
			var onboarding *string
			if json.Unmarshal(c.Onboarding, &onboarding) != nil || onboarding != nil && *onboarding != "life_sciences" {
				return nil, b.fail("protocol")
			}
			c.Error = nil
			c.Onboarding = nil
			if b.early != nil || b.attempt.loginID != "" && *c.LoginID != b.attempt.loginID {
				return nil, b.fail("correlation")
			}
			b.early = &c
			if id == 0 {
				return nil, nil
			}
			continue
		}
		if id == 0 || e.ID == nil || *e.ID != id || e.Params != nil || fields["emittedAtMs"] != nil || (e.Result == nil) == (e.Error == nil) {
			return nil, b.fail("correlation")
		}
		if e.Error != nil {
			b.diagnoseRPC(e.Error, id)
			return nil, b.fail("remote failure")
		}
		return e.Result, nil
	}
	return nil, b.fail("message limit")
}

func (b *DeviceCodeBootstrap) Start(ctx context.Context) (LoginAttempt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.diagnostic = BootstrapDiagnostics{}
	b.activeStage = -1
	if b.state != Unauthenticated || b.transport == nil || b.timeout <= 0 {
		return LoginAttempt{}, b.fail("invalid state or configuration")
	}
	b.deadline = time.Now().Add(b.timeout)
	b.activeStage = diagInitialize
	ctx, cancel := b.operationContext(ctx)
	defer cancel()
	request, _ := infrastructure.EncodeCodexInitialize(infrastructure.CodexIntegerID(1), infrastructure.CodexClientInfo{Name: "dev-orchestrator", Version: "0.1.0"})
	// Reuse the existing typed initialize codec; never propagate its remote errors.
	if ctx.Err() != nil {
		return LoginAttempt{}, b.fail("timeout or cancellation")
	}
	if b.transport.Write(ctx, request) != nil {
		return LoginAttempt{}, b.fail("transport")
	}
	raw, err := b.read(ctx, 1)
	if err != nil {
		return LoginAttempt{}, err
	}
	response, _ := json.Marshal(struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}{1, raw})
	_, err = infrastructure.DecodeCodexMessage(response, &infrastructure.CodexResponseExpectation{ID: infrastructure.CodexIntegerID(1), Method: infrastructure.CodexInitialize})
	clear(response)
	clear(raw)
	if err != nil || b.early != nil {
		return LoginAttempt{}, b.fail("protocol")
	}
	b.diagnostic.results[diagInitialize] = "PASS"
	b.activeStage = diagInitialized
	if err = b.write(ctx, struct {
		Method string `json:"method"`
	}{"initialized"}); err != nil {
		return LoginAttempt{}, err
	}
	b.diagnostic.results[diagInitialized] = "PASS"
	b.activeStage = diagLoginRPC
	if err = b.write(ctx, rpcRequest{2, "account/login/start", struct {
		Type string `json:"type"`
	}{"chatgptDeviceCode"}}); err != nil {
		return LoginAttempt{}, err
	}
	raw, err = b.read(ctx, 2)
	if err != nil {
		return LoginAttempt{}, err
	}
	defer clear(raw)
	var a struct {
		Type            string `json:"type"`
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if strict(raw, &a) != nil || a.Type != "chatgptDeviceCode" || a.LoginID == "" || a.VerificationURL == "" || a.UserCode == "" {
		return LoginAttempt{}, b.fail("protocol")
	}
	if b.early != nil && *b.early.LoginID != a.LoginID {
		return LoginAttempt{}, b.fail("correlation")
	}
	b.diagnostic.results[diagLoginRPC] = "PASS"
	b.diagnostic.results[diagDeviceRequest] = "PASS"
	b.diagnostic.results[diagResponseDecode] = "PASS"
	b.activeStage = -1
	b.attempt = LoginAttempt{a.LoginID, a.VerificationURL, a.UserCode}
	b.state = LoginPending
	return b.attempt, nil
}
func (b *DeviceCodeBootstrap) Wait(ctx context.Context) (LoginCompletion, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != LoginPending {
		return LoginCompletion{}, b.fail("invalid state")
	}
	ctx, cancel := b.operationContext(ctx)
	defer cancel()
	if ctx.Err() != nil {
		return LoginCompletion{}, b.fail("timeout or cancellation")
	}
	if b.early == nil {
		if _, err := b.read(ctx, 0); err != nil {
			return LoginCompletion{}, err
		}
	}
	if !*b.early.Success {
		return LoginCompletion{}, b.fail("login failure")
	}
	b.state = Authenticated
	b.attempt = LoginAttempt{}
	b.early = nil
	return LoginCompletion{Success: true}, nil
}
func (b *DeviceCodeBootstrap) Cancel(ctx context.Context) (LoginCancelResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != LoginPending {
		return "", b.fail("invalid state")
	}
	ctx, cancel := b.operationContext(ctx)
	defer cancel()
	if err := b.write(ctx, rpcRequest{3, "account/login/cancel", struct {
		LoginID string `json:"loginId"`
	}{b.attempt.loginID}}); err != nil {
		return "", err
	}
	raw, err := b.read(ctx, 3)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	var result struct {
		Status LoginCancelResult `json:"status"`
	}
	if strict(raw, &result) != nil || result.Status != LoginCanceled && result.Status != LoginNotFound {
		return "", b.fail("protocol")
	}
	b.state = Unauthenticated
	b.attempt = LoginAttempt{}
	b.early = nil
	return result.Status, nil
}
