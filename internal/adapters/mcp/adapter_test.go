package mcp

import (
	"context"
	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type queryProbe struct {
	calls    atomic.Int32
	evidence string
	err      error
}

func (p *queryProbe) Query(_ context.Context, e ports.AuthenticationEvidence, op string, r any) (any, error) {
	p.calls.Add(1)
	p.evidence = string(e.Material)
	if p.err != nil {
		return nil, p.err
	}
	return readcontracts.ExecutionStatusResponse{Metadata: readcontracts.Metadata{SchemaVersion: "1", ProjectID: "p", CorrelationID: "c", ObservedAt: time.Now().UTC()}, ExecutionObservation: readcontracts.Unavailable}, nil
}

type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}
func connect(t *testing.T, url, token string) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "test-local", Version: "1"}, nil)
	s, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearerTransport{token: token}}}, &sdk.ClientSessionOptions{ProtocolVersion: ProtocolVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMCPProtocolBoundary(t *testing.T) {
	probe := &queryProbe{}
	a := newAdapter(context.Background(), probe, time.Second, 2)
	server := httptest.NewServer(a)
	defer server.Close()
	session := connect(t, server.URL, "opaque")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := session.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 4 {
		t.Fatal("READ tool list", err)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "execution.status", Arguments: map[string]any{"projectId": "p", "correlationId": "c"}})
	if err != nil || result.IsError || probe.calls.Load() != 1 || probe.evidence != "opaque" {
		t.Fatal("mapping failed", err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	if !strings.Contains(string(data), "UNAVAILABLE") {
		t.Fatal("execution data invented")
	}
	for _, args := range []map[string]any{{"projectId": "p", "correlationId": "c", "principalId": "admin"}, {"projectId": "p", "correlationId": "c", "workspace": "/private"}, {"projectId": "", "correlationId": "c"}} {
		if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "execution.status", Arguments: args}); err == nil {
			t.Fatal("invalid arguments accepted")
		}
	}
	if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "write", Arguments: map[string]any{}}); err == nil {
		t.Fatal("unknown operation accepted")
	}
	if probe.calls.Load() != 1 {
		t.Fatal("invalid input reached application")
	}
	probe.err = errors.New("/private/hash-secret-stack")
	result, err = session.CallTool(ctx, &sdk.CallToolParams{Name: "execution.status", Arguments: map[string]any{"projectId": "p", "correlationId": "c"}})
	data, _ = json.Marshal(result)
	if err != nil || !result.IsError || strings.Contains(string(data), "hash-secret") {
		t.Fatal("raw failure exposed", err)
	}
	a.Stop()
	response, err := http.Post(server.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("work accepted after shutdown")
	}
}

func TestMCPHTTPProtection(t *testing.T) {
	a := newAdapter(context.Background(), &queryProbe{}, time.Second, 1)
	for _, scenario := range []string{"invalid", "origin", "host", "large"} {
		body := `broken`
		if scenario == "large" {
			body = strings.Repeat("x", MaxRequestBytes+1)
		}
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if scenario == "origin" {
			req.Header.Set("Origin", "https://attacker.invalid")
		}
		if scenario == "host" {
			req.Host = "attacker.invalid"
		}
		out := httptest.NewRecorder()
		a.ServeHTTP(out, req)
		if out.Code < 400 {
			t.Fatal("unsafe HTTP request accepted", scenario, out.Code)
		}
		data, _ := io.ReadAll(out.Result().Body)
		if strings.Contains(string(data), "broken") || strings.Contains(string(data), "attacker.invalid") {
			t.Fatal("unsafe error detail")
		}
	}
}

type stalledQuery struct {
	entered chan struct{}
	once    sync.Once
}

func (p *stalledQuery) Query(ctx context.Context, _ ports.AuthenticationEvidence, _ string, _ any) (any, error) {
	p.once.Do(func() { close(p.entered) })
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestMCPBoundedWork(t *testing.T) {
	probe := &stalledQuery{entered: make(chan struct{})}
	a := newAdapter(context.Background(), probe, 100*time.Millisecond, 1)
	server := httptest.NewServer(a)
	defer server.Close()
	session := connect(t, server.URL, "opaque")
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "execution.status", Arguments: map[string]any{"projectId": "p", "correlationId": "c"}})
		if err == nil && !result.IsError {
			err = errors.New("timeout returned success")
		}
		done <- err
	}()
	select {
	case <-probe.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("query did not enter")
	}
	response, err := http.Post(server.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("concurrency limit bypass")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unbounded work")
	}
}
