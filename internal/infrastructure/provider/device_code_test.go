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

func (canceledIO) Write(ctx context.Context, _ []byte) error { return ctx.Err() }
func (canceledIO) Read(context.Context) ([]byte, error)      { return nil, errors.New("TOKEN_RAW") }
func TestDeviceCodeTransportFailure(t *testing.T) {
	b := NewDeviceCodeBootstrap(canceledIO{}, time.Minute)
	if _, err := b.Start(context.Background()); err == nil || strings.Contains(err.Error(), "TOKEN_RAW") || b.State() != AuthFailed {
		t.Fatal("transport failure unsafe")
	}
}
