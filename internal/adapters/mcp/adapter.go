// Package mcp maps official MCP protocol to the existing secure READ application.
// It owns no authorization policy, repositories, Planner or Executor.
package mcp

import (
	"bytes"
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/application/readcontracts"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const ProtocolVersion = "2025-11-25"
const MaxRequestBytes = readcontracts.MaxJSONBytes
const MaxResponseBytes = 256 * 1024

type secureQuery interface {
	Query(context.Context, ports.AuthenticationEvidence, string, any) (any, error)
}
type Adapter struct {
	ctx     context.Context
	cancel  context.CancelFunc
	ready   atomic.Bool
	handler http.Handler
	slots   chan struct{}
	timeout time.Duration
}

// New accepts only the MCP-4 runtime. Test doubles are confined to private tests.
func New(ctx context.Context, runtime *application.SecureReadRuntime, timeout time.Duration, concurrency int) (*Adapter, error) {
	if runtime == nil || timeout <= 0 || timeout > 120*time.Second || concurrency < 1 || concurrency > 16 {
		return nil, errors.New("invalid MCP adapter configuration")
	}
	return newAdapter(ctx, runtime, timeout, concurrency), nil
}
func newAdapter(parent context.Context, runtime secureQuery, timeout time.Duration, concurrency int) *Adapter {
	ctx, cancel := context.WithCancel(parent)
	a := &Adapter{ctx: ctx, cancel: cancel, timeout: timeout, slots: make(chan struct{}, concurrency)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := sdk.NewServer(&sdk.Implementation{Name: "dev-orchestrator", Version: "1"}, &sdk.ServerOptions{Logger: logger, SupportedProtocolVersions: []string{ProtocolVersion}})
	for _, operation := range []string{"project.status", "project.tasks", "git.status", "execution.status"} {
		schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"projectId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "correlationId": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}}, "required": []string{"projectId", "correlationId"}}
		if operation == "project.tasks" {
			props := schema["properties"].(map[string]any)
			props["limit"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 100}
			props["offset"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 10000}
			schema["required"] = []string{"projectId", "correlationId", "limit"}
		}
		server.AddTool(&sdk.Tool{Name: operation, Description: "Read approved project information", InputSchema: schema, Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}}, a.tool(runtime, operation))
	}
	a.handler = sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{Stateless: true, JSONResponse: true, Logger: logger, MaxRequestBodyBytes: MaxRequestBytes})
	a.ready.Store(true)
	return a
}
func (a *Adapter) Ready() bool { return a != nil && a.ready.Load() && a.ctx.Err() == nil }
func (a *Adapter) Stop() {
	if a != nil {
		a.ready.Store(false)
		a.cancel()
	}
}

func (a *Adapter) tool(runtime secureQuery, operation string) sdk.ToolHandler {
	return func(parent context.Context, request *sdk.CallToolRequest) (result *sdk.CallToolResult, err error) {
		defer func() {
			if recover() != nil {
				result = toolFailure("READ internal failure")
				err = nil
			}
		}()
		if !a.Ready() {
			return toolFailure("READ unavailable during shutdown"), nil
		}
		var value any
		switch operation {
		case "project.status":
			value = &readcontracts.ProjectStatusRequest{}
		case "project.tasks":
			value = &readcontracts.ProjectTasksRequest{}
		case "git.status":
			value = &readcontracts.GitStatusRequest{}
		case "execution.status":
			value = &readcontracts.ExecutionStatusRequest{}
		}
		if request == nil || request.Params == nil || readcontracts.Decode(request.Params.Arguments, value) != nil {
			return nil, &jsonrpc.Error{Code: -32602, Message: "Invalid READ arguments"}
		}
		var internal any
		var correlation string
		switch r := value.(type) {
		case *readcontracts.ProjectStatusRequest:
			internal = *r
			correlation = r.CorrelationID
		case *readcontracts.ProjectTasksRequest:
			internal = *r
			correlation = r.CorrelationID
		case *readcontracts.GitStatusRequest:
			internal = *r
			correlation = r.CorrelationID
		case *readcontracts.ExecutionStatusRequest:
			internal = *r
			correlation = r.CorrelationID
		}
		evidence := ports.AuthenticationEvidence{}
		if request.Extra != nil {
			headers := request.Extra.Header.Values("Authorization")
			if len(headers) == 1 {
				kind, material, ok := strings.Cut(headers[0], " ")
				if ok && strings.EqualFold(kind, "Bearer") && material != "" && len(material) <= 4096 && !strings.ContainsAny(material, " \t\r\n") {
					evidence.Material = []byte(material)
				}
			}
		}
		ctx, cancel := context.WithTimeout(parent, a.timeout)
		defer cancel()
		stop := context.AfterFunc(a.ctx, cancel)
		defer stop()
		out, queryErr := runtime.Query(ctx, evidence, operation, internal)
		clear(evidence.Material)
		if queryErr != nil || ctx.Err() != nil {
			return toolFailure("READ denied or unavailable. CorrelationID: " + correlation), nil
		}
		data, marshalErr := json.Marshal(out)
		if marshalErr != nil || len(data) > readcontracts.MaxJSONBytes {
			return toolFailure("READ result unavailable"), nil
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: string(data)}}, StructuredContent: out}, nil
	}
}
func toolFailure(message string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: message}}}
}

func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !a.Ready() {
		http.Error(w, "Unavailable", 503)
		return
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		http.Error(w, "Forbidden", 403)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
		http.Error(w, "Forbidden", 403)
		return
	}
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	default:
		http.Error(w, "Busy", 503)
		return
	}
	if r.Method == http.MethodPost {
		data, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
		r.Body.Close()
		if err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		if len(data) > MaxRequestBytes {
			http.Error(w, "Request too large", 413)
			return
		}
		if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
			http.Error(w, "Invalid request", 400)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
	}
	bounded := &responseBuffer{header: make(http.Header)}
	defer func() {
		if recover() != nil {
			http.Error(w, "Internal error", 500)
		}
	}()
	a.handler.ServeHTTP(bounded, r)
	if bounded.overflow {
		http.Error(w, "Response unavailable", 500)
		return
	}
	data := safeProtocolErrors(bounded.body.Bytes())
	for key, values := range bounded.header {
		if key != "Cache-Control" {
			w.Header()[key] = values
		}
	}
	status := bounded.status
	if status == 0 {
		status = 200
	}
	w.WriteHeader(status)
	w.Write(data)
}

type responseBuffer struct {
	header   http.Header
	body     bytes.Buffer
	status   int
	overflow bool
}

func (b *responseBuffer) Header() http.Header { return b.header }
func (b *responseBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *responseBuffer) Write(data []byte) (int, error) {
	if b.body.Len()+len(data) > MaxResponseBytes {
		b.overflow = true
		return 0, errors.New("response limit")
	}
	return b.body.Write(data)
}
func (b *responseBuffer) Flush() {}
func safeProtocolErrors(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(data, &envelope) != nil {
		return []byte("Protocol request rejected\n")
	}
	if raw, ok := envelope["error"]; ok {
		var failure struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		json.Unmarshal(raw, &failure)
		message := "Internal protocol error"
		switch failure.Code {
		case -32700:
			message = "Parse error"
		case -32600:
			message = "Invalid request"
		case -32601:
			message = "Unsupported operation"
		case -32602:
			message = "Invalid arguments"
			if strings.HasPrefix(failure.Message, "unknown tool ") {
				message = "Unsupported operation"
			}
		case -32022:
			message = "Unsupported protocol version"
		}
		envelope["error"], _ = json.Marshal(map[string]any{"code": failure.Code, "message": message})
		encoded, err := json.Marshal(envelope)
		if err == nil {
			return encoded
		}
	}
	return data
}
