package infrastructure

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/moby/moby/client"
)

const plannerHostImage = "dev-orchestrator-codex-planner-runtime:0.159.2"

// PlannerHostContainer exposes inference and RuntimeHome lifecycle only.
// Image, argv, egress, mounts and tool policy are runtime-owned constants.
type PlannerHostContainer struct{ base *RuntimeHomeDockerContainer }

func NewPlannerHostContainer() (*PlannerHostContainer, error) {
	base, err := NewRuntimeHomeDockerContainer("auth.openai.com,chatgpt.com")
	if err != nil {
		return nil, ErrPlannerRuntimeFailure
	}
	return &PlannerHostContainer{base: base}, nil
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

type plannerHostDocker struct{ *ownedCodexEgress }

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
	created, err := d.client.ExecCreate(ctx, id, plannerHostProcessOptions())
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
	if json.Unmarshal(data, &fields) != nil || len(fields) != 2 || fields["version"] == nil || fields["structuredOutput"] == nil {
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
	c.base.process, err = startPlannerHostProcessRuntime(ctx, plannerHostDocker{c.base.egress}, c.base.id)
	if err != nil {
		return PlannerRuntimeResult{}, ErrPlannerRuntimeFailure
	}
	p := c.base.process
	if p.Write(data) != nil {
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	response, err := p.Read()
	if err != nil {
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	defer clear(response)
	result, err := decodePlannerHostResponse(response, r.Limits.MaxOutputBytes)
	if err != nil {
		return PlannerRuntimeResult{}, err
	}
	// One response only; await stdout EOF and successful process exit before capture.
	p.readMu.Lock()
	endErr := plannerHostEOF(p.reader)
	p.readMu.Unlock()
	if endErr != nil || p.Close() != nil {
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	inspect, err := c.base.driver.client.ExecInspect(ctx, p.attachment.execID, client.ExecInspectOptions{})
	if err != nil || inspect.Running || inspect.ExitCode != 0 {
		return PlannerRuntimeResult{}, plannerHostError(ctx)
	}
	if err := ctx.Err(); err != nil {
		return PlannerRuntimeResult{}, err
	}
	return result, nil
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
