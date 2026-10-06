package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type fakeSession struct {
	messages [][]byte
	writes   [][]byte
}

func (f *fakeSession) Write(_ context.Context, b []byte) error {
	f.writes = append(f.writes, append([]byte(nil), b...))
	return nil
}
func (f *fakeSession) Read(ctx context.Context) ([]byte, error) {
	if len(f.messages) == 0 {
		<-ctx.Done()
		return nil, errors.New("TOKEN_RAW")
	}
	b := f.messages[0]
	f.messages = f.messages[1:]
	return b, nil
}
func session(messages ...string) *fakeSession {
	f := &fakeSession{}
	for _, s := range append([]string{`{"id":1,"result":{"userAgent":"codex","codexHome":"/runtime","platformFamily":"unix","platformOs":"linux"}}`}, messages...) {
		f.messages = append(f.messages, []byte(s))
	}
	return f
}

const attemptResponse = `{"id":2,"result":{"type":"chatgptDeviceCode","loginId":"LOGIN_SECRET","verificationUrl":"https://auth.openai.com/codex/device","userCode":"USER_SECRET"}}`
const completed = `{"method":"account/login/completed","params":{"loginId":"LOGIN_SECRET","success":true,"error":null,"onboardingEntrypoint":null}}`

func TestDeviceCodeStartAndCompletion(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprint(early), func(t *testing.T) {
			messages := []string{attemptResponse, completed}
			if early {
				messages = []string{completed, attemptResponse}
			}
			f := session(messages...)
			b := NewDeviceCodeBootstrap(f, time.Minute)
			a, err := b.Start(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if a.LoginID() != "LOGIN_SECRET" || a.VerificationURL() != "https://auth.openai.com/codex/device" || a.UserCode() != "USER_SECRET" || b.State() != LoginPending {
				t.Fatal("invalid typed attempt/state")
			}
			var request struct {
				Method string
				Params map[string]string
			}
			if json.Unmarshal(f.writes[2], &request) != nil || request.Method != "account/login/start" || request.Params["type"] != "chatgptDeviceCode" || len(request.Params) != 1 {
				t.Fatal("wrong start request")
			}
			if string(f.writes[1]) != `{"method":"initialized"}` {
				t.Fatal("missing initialized")
			}
			c, err := b.Wait(context.Background())
			if err != nil || !c.Success || b.State() != Authenticated {
				t.Fatal("completion failed")
			}
			for _, v := range []any{a, b, c} {
				for _, format := range []string{"%v", "%+v", "%#v"} {
					s := fmt.Sprintf(format, v)
					if strings.Contains(s, "SECRET") {
						t.Fatal("diagnostic leak")
					}
				}
				raw, _ := json.Marshal(v)
				if strings.Contains(string(raw), "SECRET") {
					t.Fatal("JSON leak")
				}
			}
		})
	}
}

func TestDeviceCodeFailures(t *testing.T) {
	for _, tc := range []struct{ name, start, event string }{
		{"id", strings.Replace(attemptResponse, `"id":2`, `"id":3`, 1), completed},
		{"malformed", `{"id":2,"result":{"type":"chatgptDeviceCode","loginId":"LOGIN_SECRET"}}`, completed},
		{"wrong-type", strings.Replace(attemptResponse, "chatgptDeviceCode", "chatgptAuthTokens", 1), completed},
		{"wrong-login", attemptResponse, strings.Replace(completed, "LOGIN_SECRET", "WRONG", 1)},
		{"failure", attemptResponse, strings.Replace(completed, `"success":true`, `"success":false`, 1)},
		{"remote", `{"id":2,"error":{"code":1,"message":"TOKEN_RAW USER_SECRET LOGIN_SECRET"}}`, completed},
		{"missing-success", attemptResponse, `{"method":"account/login/completed","params":{"loginId":"LOGIN_SECRET","error":"TOKEN_RAW"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewDeviceCodeBootstrap(session(tc.start, tc.event), time.Minute)
			_, err := b.Start(context.Background())
			if err == nil {
				_, err = b.Wait(context.Background())
			}
			if err == nil || b.State() != AuthFailed {
				t.Fatal("did not fail closed")
			}
			if strings.Contains(fmt.Sprintf("%+v", err), "SECRET") || strings.Contains(err.Error(), "TOKEN_RAW") {
				t.Fatal("error leaked")
			}
		})
	}
}

func TestDeviceCodeTimeout(t *testing.T) {
	b := NewDeviceCodeBootstrap(session(attemptResponse), time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Start(ctx); err == nil || b.State() != AuthFailed {
		t.Fatal("start cancellation failed")
	}
	b = NewDeviceCodeBootstrap(session(attemptResponse), time.Minute)
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Inject an expired deadline without waiting for wall clock time.
	b.deadline = time.Now().Add(-time.Second)
	if _, err := b.Wait(context.Background()); err == nil || b.State() != AuthFailed {
		t.Fatal("timeout failed")
	}
}

func TestDeviceCodeCancel(t *testing.T) {
	for _, status := range []string{"canceled", "notFound", "invalid"} {
		t.Run(status, func(t *testing.T) {
			f := session(attemptResponse, `{"id":3,"result":{"status":"`+status+`"}}`)
			b := NewDeviceCodeBootstrap(f, time.Minute)
			if _, err := b.Start(context.Background()); err != nil {
				t.Fatal(err)
			}
			result, err := b.Cancel(context.Background())
			if status == "invalid" {
				if err == nil || b.State() != AuthFailed {
					t.Fatal("invalid cancel accepted")
				}
				return
			}
			if err != nil || string(result) != status || b.State() != Unauthenticated {
				t.Fatal("cancel failed")
			}
			var req struct {
				Method string
				Params map[string]string
			}
			json.Unmarshal(f.writes[3], &req)
			if req.Method != "account/login/cancel" || req.Params["loginId"] != "LOGIN_SECRET" || len(req.Params) != 1 {
				t.Fatal("cancel wire mismatch")
			}
		})
	}
}

func TestDeviceCodeMalformedAndEarlyCorrelation(t *testing.T) {
	for _, messages := range [][]string{
		{"   "},
		{strings.Replace(attemptResponse, `"id":2`, `"id":2,"method":null`, 1)},
		{attemptResponse, strings.Replace(completed, `"method":`, `"id":null,"method":`, 1)},
		{strings.Replace(attemptResponse, `"id":2`, `"id":2,"id":2`, 1)},
		{`{"id":"2","result":{}}`},
		{strings.Replace(completed, "LOGIN_SECRET", "WRONG", 1), attemptResponse},
		{completed, completed, attemptResponse},
		{attemptResponse, strings.Replace(completed, `"onboardingEntrypoint":null`, `"onboardingEntrypoint":"TOKEN_RAW"`, 1)},
		{attemptResponse, `{"method":"account/login/completed","params":{"loginId":null,"success":true,"error":null,"onboardingEntrypoint":null}}`},
	} {
		b := NewDeviceCodeBootstrap(session(messages...), time.Minute)
		_, err := b.Start(context.Background())
		if err == nil {
			_, err = b.Wait(context.Background())
		}
		if err == nil || b.State() != AuthFailed {
			t.Fatal("malformed or uncorrelated payload accepted")
		}
	}
}

func TestDeviceCodeInitializeFailure(t *testing.T) {
	for _, init := range []string{
		`{"id":9,"result":{}}`,
		`{"id":1,"error":{"code":1,"message":"TOKEN_RAW"}}`,
		`{"id":1,"result":{}}`,
	} {
		f := &fakeSession{messages: [][]byte{[]byte(init)}}
		b := NewDeviceCodeBootstrap(f, time.Minute)
		if _, err := b.Start(context.Background()); err == nil || len(f.writes) != 1 {
			t.Fatal("login started before valid initialization")
		}
	}
}

type canceledIO struct{}

func TestDeviceCodeSafeDiagnosticsStages(t *testing.T) {
	for _, tc := range []struct{ response, stage, class string }{
		{`{"id":1,"error":{"code":-32600,"message":"SECRET_RAW"}}`, "INITIALIZE", "RPC_ERROR"},
		{`{"id":1,"result":{}}`, "INITIALIZE", "RESPONSE_DECODE"},
		{`{"id":2,"error":{"code":-32602,"message":"SECRET_RAW","data":{"token":"SECRET_BODY"}}}`, "LOGIN_RPC_START", "RPC_ERROR"},
		{`{"id":2,"result":{}}`, "LOGIN_RPC_START", "RESPONSE_DECODE"},
	} {
		f := session(tc.response)
		if tc.stage == "INITIALIZE" {
			f = &fakeSession{messages: [][]byte{[]byte(tc.response)}}
		}
		b := NewDeviceCodeBootstrap(f, time.Minute)
		if _, err := b.Start(context.Background()); err == nil {
			t.Fatal("failure accepted")
		}
		lines := strings.Join(b.Diagnostics().Lines(), "\n")
		if !strings.Contains(lines, "stage="+tc.stage+" result=FAIL") || !strings.Contains(lines, "classification="+tc.class) {
			t.Fatal("incorrect sanitized diagnostic")
		}
		// The new structural labels contain "message" but no remote message.
		withoutMetadataLabels := strings.ReplaceAll(strings.ReplaceAll(lines, "message_kind=", ""), "login_rpc_message_index=", "")
		if strings.Contains(lines, "SECRET") || strings.Contains(withoutMetadataLabels, "message") || strings.Contains(lines, "token") {
			t.Fatal("diagnostic leak")
		}
		if tc.stage == "INITIALIZE" && strings.Contains(lines, "stage=LOGIN_RPC_START result=PASS") {
			t.Fatal("unobserved login stage passed")
		}
	}
}

func TestDeviceCodeSafeRemoteStatus(t *testing.T) {
	for _, tc := range []struct {
		message, class string
		status         int
	}{
		{"device code request failed with status 403 Forbidden", "REMOTE_STATUS", 403},
		{"failed to request device code: device code request failed with status 401 Unauthorized", "REMOTE_STATUS", 401},
		{"device code login is not enabled for this Codex server. Use the browser login or verify the server URL.", "DEVICE_CODE_NOT_ENABLED", 404},
		{"SECRET 404 body Unauthorized", "RPC_ERROR", 0},
		{"device code request failed with status 999 SECRET", "RPC_ERROR", 0},
		{"device code request failed with status 403 Forbidden SECRET_BODY", "RPC_ERROR", 0},
	} {
		code := -32603
		if tc.class == "DEVICE_CODE_NOT_ENABLED" {
			code = -32600
		}
		raw, _ := json.Marshal(map[string]any{"id": 2, "error": map[string]any{"code": code, "message": tc.message}})
		b := NewDeviceCodeBootstrap(session(string(raw)), time.Minute)
		b.Start(context.Background())
		lines := strings.Join(b.Diagnostics().Lines(), "\n")
		if !strings.Contains(lines, "classification="+tc.class) || strings.Contains(lines, "SECRET") || strings.Contains(lines, "body") {
			t.Fatal("unsafe remote classification")
		}
		if tc.status != 0 && !strings.Contains(lines, fmt.Sprintf("http_status=%d", tc.status)) {
			t.Fatal("safe status missing")
		}
		if tc.status == 0 && strings.Contains(lines, "http_status=") {
			t.Fatal("status invented")
		}
		if !strings.Contains(lines, fmt.Sprintf("rpc_code=%d", code)) {
			t.Fatal("RPC code not preserved")
		}
	}
}

func TestDeviceCodeSafeDiagnosticsUnknownAndSuccess(t *testing.T) {
	b := NewDeviceCodeBootstrap(session(attemptResponse), time.Minute)
	before := strings.Join(b.Diagnostics().Lines(), "\n")
	if strings.Contains(before, "result=PASS") || !strings.Contains(before, "classification=UNKNOWN") {
		t.Fatal("unobserved success")
	}
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatal("fake start failed")
	}
	lines := strings.Join(b.Diagnostics().Lines(), "\n")
	for _, stage := range []string{"INITIALIZE", "INITIALIZED", "LOGIN_RPC_START", "DEVICE_CODE_REQUEST", "RESPONSE_DECODE"} {
		if !strings.Contains(lines, "stage="+stage+" result=PASS") {
			t.Fatal("observed stage missing")
		}
	}
	if strings.Contains(lines, "SECRET") || strings.Contains(fmt.Sprintf("%#v", b.Diagnostics()), "SECRET") {
		t.Fatal("diagnostic leak")
	}
}

type diagnosticWriteFailure struct {
	*fakeSession
	failAt, writesSeen int
}

func (f *diagnosticWriteFailure) Write(ctx context.Context, data []byte) error {
	f.writesSeen++
	if f.writesSeen == f.failAt {
		return errors.New("SECRET_RAW_BODY")
	}
	return f.fakeSession.Write(ctx, data)
}
func TestDeviceCodeSafeWriteStages(t *testing.T) {
	for i, stage := range []string{"INITIALIZE", "INITIALIZED", "LOGIN_RPC_START"} {
		f := &diagnosticWriteFailure{fakeSession: session(attemptResponse), failAt: i + 1}
		b := NewDeviceCodeBootstrap(f, time.Minute)
		if _, err := b.Start(context.Background()); err == nil {
			t.Fatal("transport failure ignored")
		}
		lines := strings.Join(b.Diagnostics().Lines(), "\n")
		if !strings.Contains(lines, "stage="+stage+" result=FAIL") || !strings.Contains(lines, "classification=TRANSPORT") || strings.Contains(lines, "SECRET") {
			t.Fatal("unsafe write-stage diagnostic")
		}
		if !strings.Contains(lines, "stage=DEVICE_CODE_REQUEST result=UNKNOWN") {
			t.Fatal("device request inferred from write failure")
		}
	}
}
func TestDeviceCodeSafeDiagnosticsMalformedRemoteAndUnknown(t *testing.T) {
	b := NewDeviceCodeBootstrap(session(`{"id":2,"error":{"code":"SECRET","message":"SECRET_BODY"}}`), time.Minute)
	b.Start(context.Background())
	lines := strings.Join(b.Diagnostics().Lines(), "\n")
	if strings.Contains(lines, "SECRET") || strings.Contains(lines, "rpc_code=") || strings.Contains(lines, "http_status=") {
		t.Fatal("unsafe malformed RPC metadata")
	}
	d := BootstrapDiagnostics{classification: "SECRET", httpStatus: 999}
	d.results[diagInitialize] = "SECRET"
	lines = strings.Join(d.Lines(), "\n")
	if strings.Contains(lines, "SECRET") || strings.Contains(lines, "http_status=") || !strings.Contains(lines, "classification=UNKNOWN") {
		t.Fatal("unknown diagnostics not closed")
	}
	raw, _ := json.Marshal(b.Diagnostics())
	if strings.Contains(string(raw), "SECRET") {
		t.Fatal("JSON diagnostics leaked")
	}
}

func TestDeviceCodeSafeDiagnosticsDoNotReusePriorSuccess(t *testing.T) {
	f := session(attemptResponse, `{"id":3,"result":{"status":"canceled"}}`, `{"id":1,"result":{}}`)
	b := NewDeviceCodeBootstrap(f, time.Minute)
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatal("fake start failed")
	}
	if _, err := b.Cancel(context.Background()); err != nil {
		t.Fatal("fake cancel failed")
	}
	if _, err := b.Start(context.Background()); err == nil {
		t.Fatal("invalid initialization accepted")
	}
	lines := strings.Join(b.Diagnostics().Lines(), "\n")
	if !strings.Contains(lines, "stage=INITIALIZE result=FAIL") || !strings.Contains(lines, "stage=LOGIN_RPC_START result=UNKNOWN") || !strings.Contains(lines, "stage=DEVICE_CODE_REQUEST result=UNKNOWN") {
		t.Fatal("prior success reused")
	}
}

func TestDeviceCodeLoginRPCShape(t *testing.T) {
	for _, tc := range []struct{ wire, kind, result, match, typ string }{
		{attemptResponse, "response", "object", "yes", "chatgptDeviceCode"},
		{`{"id":9,"result":{"type":"chatgptDeviceCode","loginId":"SECRET","verificationUrl":"https://auth.openai.com/codex/device?SECRET","userCode":"SECRET"}}`, "response", "object", "no", "chatgptDeviceCode"},
		{`{"id":2,"result":null}`, "response", "null", "yes", "UNKNOWN"},
		{`{"id":2}`, "unknown", "missing", "yes", "UNKNOWN"},
		{`{"id":2,"result":{"type":"SECRET_TYPE","SECRET_KEY":"SECRET_BODY"}}`, "response", "object", "yes", "UNKNOWN"},
		{`{"method":"configWarning","params":{"summary":"SECRET_BODY"}}`, "notification", "missing", "no", "UNKNOWN"},
		{`{"id":"SECRET_ID","method":"server/request","params":{"token":"SECRET_BODY"}}`, "request", "missing", "no", "UNKNOWN"},
		{`{"id":2,"error":{"code":-32603,"message":"SECRET_BODY"}}`, "response", "missing", "yes", "UNKNOWN"},
		{`not-json-SECRET`, "unknown", "missing", "no", "UNKNOWN"},
	} {
		shape := safeLoginRPCShape([]byte(tc.wire), 2)
		lines := strings.Join(shape.Lines(), "\n")
		for _, want := range []string{"message_kind=" + tc.kind, "result_kind=" + tc.result, "id_matches=" + tc.match, "result_type=" + tc.typ} {
			if !strings.Contains(lines, want) {
				t.Fatal("incorrect structural classification")
			}
		}
		if strings.Contains(lines, "SECRET") || strings.Contains(lines, "https://") || strings.Contains(lines, "summary") || strings.Contains(lines, "server/request") {
			t.Fatal("structural diagnostic exposed values")
		}
		if strings.Contains(fmt.Sprintf("%#v", shape), "SECRET") {
			t.Fatal("shape formatting exposed values")
		}
		raw, _ := json.Marshal(shape)
		if strings.Contains(string(raw), "SECRET") {
			t.Fatal("shape JSON exposed values")
		}
	}
}

func TestDeviceCodeShapeBeforeResponse(t *testing.T) {
	f := session(`{"method":"configWarning","params":{"summary":"SECRET"}}`, attemptResponse)
	b := NewDeviceCodeBootstrap(f, time.Minute)
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatal("valid notification prevented correlated response")
	}
	lines := strings.Join(b.Diagnostics().Lines(), "\n")
	if !strings.Contains(lines, "message_kind=notification") || !strings.Contains(lines, "message_kind=response") || strings.Contains(lines, "SECRET") || len(f.messages) != 0 {
		t.Fatal("sanitized interleaved shape missing")
	}
	b = NewDeviceCodeBootstrap(session(completed, attemptResponse), time.Minute)
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatal("existing early completion failed")
	}
	lines = strings.Join(b.Diagnostics().Lines(), "\n")
	if !strings.Contains(lines, "message_kind=notification") || !strings.Contains(lines, "message_kind=response") || strings.Contains(lines, "SECRET") {
		t.Fatal("interleaved shape not preserved")
	}
}

func TestDeviceCodeInterleavedNotifications(t *testing.T) {
	notification := `{"method":"configWarning","params":{"summary":"SECRET_BODY"},"emittedAtMs":123}`
	for _, tc := range []struct {
		name     string
		messages []string
	}{
		{"before", []string{notification, attemptResponse, completed}},
		{"multiple", []string{notification, notification, attemptResponse, completed}},
		{"early", []string{notification, completed, notification, attemptResponse}},
		{"after", []string{attemptResponse, notification, completed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewDeviceCodeBootstrap(session(tc.messages...), time.Minute)
			if _, err := b.Start(context.Background()); err != nil || b.State() != LoginPending {
				t.Fatal("interleaving prevented pending login")
			}
			if c, err := b.Wait(context.Background()); err != nil || !c.Success || b.State() != Authenticated {
				t.Fatal("interleaving lost completion")
			}
			if strings.Contains(fmt.Sprintf("%#v", b.Diagnostics()), "SECRET") {
				t.Fatal("ignored notification leaked")
			}
		})
	}
	// Generic notifications never establish authentication or completion.
	b := NewDeviceCodeBootstrap(session(attemptResponse, notification), time.Minute)
	b.Start(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := b.Wait(ctx); err == nil || b.State() != AuthFailed {
		t.Fatal("generic notification treated as login completion")
	}
}

func TestDeviceCodeInterleavingFailClosed(t *testing.T) {
	for _, wire := range []string{
		`{"method":"event","id":2,"params":{}}`,
		`{"method":"event","id":null,"params":{}}`,
		`{"method":"event","result":null,"params":{}}`,
		`{"method":"event","error":null,"params":{}}`,
		`{"method":"event"}`,
		`{"method":"event","params":null}`,
		`{"method":"event","params":[]}`,
		`{"method":"","params":{}}`,
		`{"method":null,"params":{}}`,
		`{"method":"event","params":{"key":1,"key":2}}`,
		`{"method":"event","params":{},"unknown":"SECRET"}`,
		`{"method":"event","params":{},"emittedAtMs":"SECRET"}`,
		strings.Replace(attemptResponse, `"id":2`, `"id":9`, 1),
	} {
		b := NewDeviceCodeBootstrap(session(wire, attemptResponse), time.Minute)
		if _, err := b.Start(context.Background()); err == nil || b.State() != AuthFailed {
			t.Fatal("invalid or uncorrelated envelope accepted")
		}
		if strings.Contains(fmt.Sprintf("%#v", b.Diagnostics()), "SECRET") {
			t.Fatal("rejected envelope leaked")
		}
	}
	messages := make([]string, 128)
	for i := range messages {
		messages[i] = `{"method":"event","params":{}}`
	}
	f := session(append(messages, attemptResponse)...)
	b := NewDeviceCodeBootstrap(f, time.Minute)
	if _, err := b.Start(context.Background()); err == nil || err.Error() != "device code bootstrap: message limit" || len(f.messages) != 1 {
		t.Fatal("notification flood bypassed message limit")
	}
}

func TestDeviceCodeShapeSensitiveFieldPresenceAndOrigin(t *testing.T) {
	for _, origin := range []struct{ value, want string }{
		{"https://auth.openai.com/codex/device?SECRET", "yes"},
		{"https://SECRET@auth.openai.com/codex/device", "no"},
		{"https://auth.openai.com.SECRET/codex/device", "no"},
		{"http://auth.openai.com/codex/device", "no"},
	} {
		raw, _ := json.Marshal(map[string]any{"id": 2, "result": map[string]any{"type": "chatgptDeviceCode", "loginId": "SECRET_ID", "verificationUrl": origin.value, "userCode": "SECRET_CODE", "SECRET_KEY": "SECRET_VALUE"}})
		lines := strings.Join(safeLoginRPCShape(raw, 2).Lines(), "\n")
		for _, want := range []string{"type=yes", "loginId=yes", "verificationUrl=yes", "userCode=yes", "verification_url_origin_valid=" + origin.want, "result_unknown_field_count=1"} {
			if !strings.Contains(lines, want) {
				t.Fatal("sensitive field projection incorrect")
			}
		}
		if strings.Contains(lines, "SECRET") || strings.Contains(lines, "https://") {
			t.Fatal("sensitive values exposed")
		}
	}
}

func (canceledIO) Write(ctx context.Context, _ []byte) error { return ctx.Err() }
func (canceledIO) Read(context.Context) ([]byte, error)      { return nil, errors.New("TOKEN_RAW") }
func TestDeviceCodeTransportFailure(t *testing.T) {
	b := NewDeviceCodeBootstrap(canceledIO{}, time.Minute)
	if _, err := b.Start(context.Background()); err == nil || strings.Contains(err.Error(), "TOKEN_RAW") || b.State() != AuthFailed {
		t.Fatal("transport failure unsafe")
	}
}
