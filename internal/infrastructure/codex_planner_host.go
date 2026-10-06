package infrastructure

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/moby/moby/client"
)

const plannerHostImage = "dev-orchestrator-codex-planner-runtime:0.159.2"

// PlannerHostContainer exposes inference and RuntimeHome lifecycle only.
// Image, argv, egress, mounts and tool policy are runtime-owned constants.
type PlannerHostContainer struct {
	base               *RuntimeHomeDockerContainer
	evidenceVerified   bool
	failureClass       string
	lastConfirmedStage string
	httpDiagnostic     bool
	responseDiagnostic bool
}

// These observations contain only host-owned enum values, never payload text.
func (c *PlannerHostContainer) SanitizedFailureClass() string {
	if c == nil || c.failureClass == "" {
		return "unknown"
	}
	return c.failureClass
}
func (c *PlannerHostContainer) LastConfirmedStage() string {
	if c == nil || c.lastConfirmedStage == "" {
		return "none"
	}
	return c.lastConfirmedStage
}
func (c *PlannerHostContainer) observeHostResponse(data []byte) {
	c.failureClass = "protocol_invalid"
	if len(data) > 128*1024 || !utf8.Valid(data) || plannerUniqueJSON(data) != nil {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || !bytes.Equal(bytes.TrimSpace(fields["version"]), []byte("1")) {
		return
	}
	var class string
	if json.Unmarshal(fields["error"], &class) != nil {
		return
	}
	switch class {
	case "auth_failure", "tool_observed", "output_missing", "output_invalid", "session_start_failure", "inference_failure", "provider_failure", "timeout", "cleanup_failure", "runtime_failure", "invalid_request", "input_limit", "output_limit", "cancelled", "connect_failure", "proxy_failure", "provider_http_failure", "provider_stream_failure", "provider_rejected", "provider_unavailable", "provider_rate_limited", "provider_attempts_exhausted", "provider_response_failed", "provider_schema_rejected", "provider_model_rejected", "provider_incomplete", "provider_protocol_failure", "diagnostic_completed":
		c.failureClass = class
		// Receipt of a closed, versioned host error proves host startup only.
		c.lastConfirmedStage = "HOST_START"
		switch class {
		case "session_start_failure":
			c.lastConfirmedStage = "AUTH"
		case "inference_failure":
			c.lastConfirmedStage = "SESSION_START"
		case "provider_failure", "connect_failure", "proxy_failure", "provider_http_failure", "provider_stream_failure", "provider_rejected", "provider_unavailable", "provider_rate_limited", "provider_attempts_exhausted":
			// These enums originate only in the event loop after start_thread and
			// local turn submission succeeded. They do not prove provider receipt.
			c.lastConfirmedStage = "SESSION_START"
		case "output_missing", "output_invalid", "output_limit":
			c.lastConfirmedStage = "PROVIDER_RESPONSE"
		case "provider_response_failed", "provider_schema_rejected", "provider_model_rejected", "provider_incomplete", "provider_protocol_failure", "diagnostic_completed":
			c.lastConfirmedStage = "PROVIDER_RESPONSE"
		}
	}
}

// SanitizedEvidenceVerified is true only after the closed host attestation,
// successful process exit and exact EOF have all been checked.
func (c *PlannerHostContainer) SanitizedEvidenceVerified() bool {
	return c != nil && c.evidenceVerified
}

func NewPlannerHostContainer() (*PlannerHostContainer, error) {
	base, err := NewRuntimeHomeDockerContainer("auth.openai.com,chatgpt.com")
	if err != nil {
		return nil, ErrPlannerRuntimeFailure
	}
	return &PlannerHostContainer{base: base}, nil
}

// NewPlannerHostHTTPDiagnosticContainer isolates transport selection only.
// Production construction retains the dependency's configured transport.
func NewPlannerHostHTTPDiagnosticContainer() (*PlannerHostContainer, error) {
	c, err := NewPlannerHostContainer()
	if err != nil {
		return nil, err
	}
	c.httpDiagnostic = true
	return c, nil
}

func NewPlannerHostResponseDiagnosticContainer() (*PlannerHostContainer, error) {
	c, err := NewPlannerHostContainer()
	if err == nil {
		c.responseDiagnostic = true
	}
	return c, err
}
func (c *PlannerHostContainer) Prepare(ctx context.Context, archive io.Reader) error {
	if c == nil || c.base == nil {
		return ErrPlannerRuntimeFailure
	}
	return c.base.prepare(ctx, archive, plannerHostCreateOptions)
}
func (c *PlannerHostContainer) Capture(ctx context.Context) (io.ReadCloser, error) {
	return c.base.Capture(ctx)
}
func (c *PlannerHostContainer) Destroy(ctx context.Context) error {
	if c == nil || c.base == nil {
		return nil
	}
	return c.base.Destroy(ctx)
}
func plannerHostCreateOptions(private string) client.ContainerCreateOptions {
	opts := runtimeHomeCreateOptions(private)
	opts.Config.Image = plannerHostImage
	opts.Config.WorkingDir = "/planner"
	return opts
}
func plannerHostProcessOptions() client.ExecCreateOptions {
	opts := authenticatedCodexProcessOptions()
	opts.Cmd = []string{"/usr/local/bin/codex-planner-host"}
	opts.WorkingDir = "/planner"
	return opts
}

type plannerHostDocker struct {
	*ownedCodexEgress
	probe              bool
	httpDiagnostic     bool
	responseDiagnostic bool
}

// Reuse the shared bounded Write/Read/Close and lease-owned termination, with
// a single-shot stdout pump: a clean frame boundary is successful EOF.
func startPlannerHostProcessRuntime(ctx context.Context, driver leaseCodexProcessDocker, id string) (*CodexProcessTransport, error) {
	attachment, err := driver.startCodexProcess(ctx, id)
	if err != nil || attachment.execID == "" || attachment.input == nil || attachment.output == nil || attachment.close == nil || attachment.closeWrite == nil {
		if attachment.close != nil {
			attachment.close()
		}
		return nil, ErrPlannerRuntimeFailure
	}
	output, writer := io.Pipe()
	p := &CodexProcessTransport{docker: driver, containerID: id, attachment: attachment, output: output, reader: bufio.NewReader(output), pumpDone: make(chan struct{}), closed: make(chan struct{}), leaseOwned: true}
	go func() {
		defer close(p.pumpDone)
		err := copyPlannerHostStdout(writer, attachment.output)
		if err != nil {
			writer.CloseWithError(ErrPlannerRuntimeFailure)
		} else {
			writer.Close()
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = p.Close()
		case <-p.closed:
		}
	}()
	return p, nil
}

func copyPlannerHostStdout(stdout io.Writer, multiplexed io.Reader) error {
	var header [8]byte
	for {
		n, err := io.ReadFull(multiplexed, header[:])
		if err != nil {
			if n == 0 && errors.Is(err, io.EOF) {
				return nil
			}
			return ErrPlannerRuntimeFailure
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return ErrPlannerRuntimeFailure
		}
		var destination io.Writer
		switch header[0] {
		case 1:
			destination = stdout
		case 2:
			destination = io.Discard
		default:
			return ErrPlannerRuntimeFailure
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size > codexProcessMessageLimit {
			return ErrPlannerRuntimeFailure
		}
		if _, err = io.CopyN(destination, multiplexed, size); err != nil {
			return ErrPlannerRuntimeFailure
		}
	}
}

func (d plannerHostDocker) startCodexProcess(ctx context.Context, id string) (codexProcessAttachment, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerOperationTimeout)
	defer cancel()
	if !d.proxyHealthy(ctx) {
		return codexProcessAttachment{}, ErrPlannerRuntimeFailure
	}
	opts := plannerHostProcessOptions()
	if d.probe {
		opts.Cmd = append(opts.Cmd, "--probe-handshake")
	}
	if d.httpDiagnostic {
		opts.Cmd = append(opts.Cmd, "--http-sse-diagnostic")
	}
	if d.responseDiagnostic {
		opts.Cmd = append(opts.Cmd, "--response-diagnostic")
	}
	created, err := d.client.ExecCreate(ctx, id, opts)
	if err != nil {
		return codexProcessAttachment{}, ErrPlannerRuntimeFailure
	}
	attached, err := d.client.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return codexProcessAttachment{}, ErrPlannerRuntimeFailure
	}
	return codexProcessAttachment{execID: created.ID, input: codexProcessStdin{attached.Conn}, output: attached.Reader, close: attached.Close, closeWrite: attached.CloseWrite}, nil
}
func encodePlannerHostRequest(r PlannerRuntimeRequest) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Version      int             `json:"version"`
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		OutputSchema json.RawMessage `json:"outputSchema"`
		Limits       struct {
			MaxOutputBytes int   `json:"maxOutputBytes"`
			TimeoutMs      int64 `json:"timeoutMs"`
		} `json:"limits"`
	}{1, r.Instructions, r.Input, r.OutputSchema, struct {
		MaxOutputBytes int   `json:"maxOutputBytes"`
		TimeoutMs      int64 `json:"timeoutMs"`
	}{r.Limits.MaxOutputBytes, max(1, r.Limits.Timeout.Milliseconds())}})
}
func decodePlannerHostResponse(data []byte, maxOutput int) (PlannerRuntimeResult, error) {
	if len(data) > 128*1024 || !utf8.Valid(data) {
		return PlannerRuntimeResult{}, ErrPlannerRuntimeFailure
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 3 || fields["version"] == nil || fields["structuredOutput"] == nil || !validPlannerHostEvidence(fields["evidence"]) {
		return PlannerRuntimeResult{}, ErrPlannerRuntimeFailure
	}
	var response struct {
		Version          int             `json:"version"`
		StructuredOutput json.RawMessage `json:"structuredOutput"`
	}
	if plannerUniqueJSON(data) != nil || json.Unmarshal(data, &response) != nil || response.Version != 1 || len(response.StructuredOutput) == 0 || len(response.StructuredOutput) > maxOutput || bytes.Equal(bytes.TrimSpace(response.StructuredOutput), []byte("null")) || !json.Valid(response.StructuredOutput) {
		return PlannerRuntimeResult{}, ErrPlannerRuntimeFailure
	}
	return PlannerRuntimeResult{StructuredOutput: response.StructuredOutput}, nil
}

func validPlannerHostEvidence(data []byte) bool {
	const expected = `{"auth":"runtime_owned","inference_attempt":1,"request_max_retries":0,"stream_max_retries":0,"tools_policy":"empty","tools_observed":0,"cleanup":"pass"}`
	var got, want map[string]json.RawMessage
	if json.Unmarshal(data, &got) != nil || json.Unmarshal([]byte(expected), &want) != nil || len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if !bytes.Equal(bytes.TrimSpace(got[key]), value) {
			return false
		}
	}
	return true
}

func plannerUniqueJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return ErrPlannerRuntimeFailure
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return ErrPlannerRuntimeFailure
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return ErrPlannerRuntimeFailure
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return ErrPlannerRuntimeFailure
	}
	return nil
}
func (c *PlannerHostContainer) Infer(ctx context.Context, r PlannerRuntimeRequest) (PlannerRuntimeResult, error) {
	if err := ctx.Err(); err != nil {
		return PlannerRuntimeResult{}, err
	}
	data, err := encodePlannerHostRequest(r)
	if err != nil {
		return PlannerRuntimeResult{}, err
	}
	defer clear(data)
	if c == nil || c.base == nil || c.base.destroyed || c.base.id == "" || c.base.process != nil {
		return PlannerRuntimeResult{}, ErrPlannerRuntimeFailure
	}
	ctx, cancel := context.WithTimeout(ctx, r.Limits.Timeout)
	defer cancel()
	c.base.process, err = startPlannerHostProcessRuntime(ctx, plannerHostDocker{ownedCodexEgress: c.base.egress, httpDiagnostic: c.httpDiagnostic, responseDiagnostic: c.responseDiagnostic}, c.base.id)
	if err != nil {
		c.failureClass = "host_start_failure"
		return PlannerRuntimeResult{}, ErrPlannerRuntimeFailure
	}
	p := c.base.process
	c.lastConfirmedStage = "HOST_START"
	if p.Write(data) != nil {
		c.failureClass = "host_transport_failure"
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	response, err := p.Read()
	if err != nil {
		c.failureClass = "host_transport_failure"
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	defer clear(response)
	result, err := decodePlannerHostResponse(response, r.Limits.MaxOutputBytes)
	if err != nil {
		c.observeHostResponse(response)
		return PlannerRuntimeResult{}, err
	}
	c.lastConfirmedStage = "RUNTIME_DECODE"
	// One response only; await stdout EOF and successful process exit before capture.
	p.readMu.Lock()
	endErr := plannerHostEOF(p.reader)
	p.readMu.Unlock()
	if endErr != nil || p.Close() != nil {
		c.failureClass = "protocol_invalid"
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	inspect, err := c.base.driver.client.ExecInspect(ctx, p.attachment.execID, client.ExecInspectOptions{})
	if err != nil || inspect.Running || inspect.ExitCode != 0 {
		c.failureClass = "host_exit_failure"
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return PlannerRuntimeResult{}, err
	}
	c.evidenceVerified = true
	return result, nil
}

type PlannerProbeEvidence struct {
	AuthAvailable bool   `json:"auth_available"`
	WSHandshake   string `json:"ws_handshake"`
	HTTPClass     string `json:"http_class"`
	WSCloseClass  string `json:"ws_close_class"`
	InferenceSent bool   `json:"inference_sent"`
}

func decodePlannerProbe(data []byte) (PlannerProbeEvidence, error) {
	var empty PlannerProbeEvidence
	if len(data) > 4096 || !utf8.Valid(data) || plannerUniqueJSON(data) != nil {
		return empty, ErrPlannerRuntimeFailure
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || len(fields) != 5 {
		return empty, ErrPlannerRuntimeFailure
	}
	for _, key := range []string{"auth_available", "ws_handshake", "http_class", "ws_close_class", "inference_sent"} {
		if fields[key] == nil || bytes.Equal(bytes.TrimSpace(fields[key]), []byte("null")) {
			return empty, ErrPlannerRuntimeFailure
		}
	}
	var result PlannerProbeEvidence
	if json.Unmarshal(data, &result) != nil || result.InferenceSent {
		return empty, ErrPlannerRuntimeFailure
	}
	allowed := func(value string, choices ...string) bool {
		for _, choice := range choices {
			if value == choice {
				return true
			}
		}
		return false
	}
	if !allowed(result.WSHandshake, "PASS", "FAIL", "UNKNOWN") || !allowed(result.HTTPClass, "101", "2xx", "401", "403", "407", "429", "5xx", "OTHER", "UNKNOWN") || !allowed(result.WSCloseClass, "NONE", "NORMAL", "POLICY", "OTHER", "UNKNOWN") || (result.WSHandshake == "PASS" && (!result.AuthAvailable || result.HTTPClass != "101")) {
		return empty, ErrPlannerRuntimeFailure
	}
	return result, nil
}

// ProbeHandshake uses the existing lease-owned container, proxy and auth home.
// The fixed diagnostic process opens one handshake without starting a thread.
func (c *PlannerHostContainer) ProbeHandshake(ctx context.Context) (PlannerProbeEvidence, error) {
	var empty PlannerProbeEvidence
	if c == nil || c.base == nil || c.base.destroyed || c.base.id == "" || c.base.process != nil {
		return empty, ErrPlannerRuntimeFailure
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var err error
	c.base.process, err = startPlannerHostProcessRuntime(ctx, plannerHostDocker{ownedCodexEgress: c.base.egress, probe: true}, c.base.id)
	if err != nil {
		return empty, ErrPlannerRuntimeFailure
	}
	p := c.base.process
	data, err := p.Read()
	if err != nil {
		return empty, ErrPlannerRuntimeFailure
	}
	defer clear(data)
	report, err := decodePlannerProbe(data)
	if err != nil {
		return empty, err
	}
	p.readMu.Lock()
	endErr := plannerHostEOF(p.reader)
	p.readMu.Unlock()
	if endErr != nil || p.Close() != nil {
		return empty, ErrPlannerRuntimeFailure
	}
	inspect, err := c.base.driver.client.ExecInspect(ctx, p.attachment.execID, client.ExecInspectOptions{})
	if err != nil || inspect.Running || inspect.ExitCode != 0 || ctx.Err() != nil {
		return empty, ErrPlannerRuntimeFailure
	}
	return report, nil
}

// The shared transport treats EOF as an RPC failure. This single-shot protocol
// requires exact EOF instead, and rejects even one trailing byte.
func plannerHostEOF(reader io.ByteReader) error {
	_, err := reader.ReadByte()
	if !errors.Is(err, io.EOF) {
		return ErrPlannerRuntimeFailure
	}
	return nil
}
func plannerHostError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrPlannerRuntimeFailure
}
