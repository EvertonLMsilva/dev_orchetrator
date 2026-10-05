package infrastructure

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func codexDockerFrame(stream byte, text string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
	return append(header, text...)
}

type fakeCodexProcessDocker struct {
	mu                           sync.Mutex
	calls                        []string
	input                        bytes.Buffer
	output                       io.Reader
	startErr, stopErr, removeErr error
	writeErr                     bool
	closed                       bool
}

func (d *fakeCodexProcessDocker) record(call string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, call)
}
func (d *fakeCodexProcessDocker) startCodexProcess(_ context.Context, id string) (codexProcessAttachment, error) {
	if id != "container" {
		panic("wrong container")
	}
	d.record("start")
	if d.startErr != nil {
		return codexProcessAttachment{}, d.startErr
	}
	return codexProcessAttachment{execID: "exec", input: d, output: d.output, close: func() {
		d.mu.Lock()
		d.closed = true
		d.mu.Unlock()
		if closer, ok := d.output.(io.Closer); ok {
			closer.Close()
		}
	}, closeWrite: func() error { d.record("closeWrite"); return nil }}, nil
}
func (d *fakeCodexProcessDocker) Write(data []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.writeErr {
		return 0, errors.New("TEST_SECRET_DO_NOT_LEAK")
	}
	return d.input.Write(data)
}
func (d *fakeCodexProcessDocker) stopCodexProcess(ctx context.Context, id, execID string) error {
	if ctx.Err() != nil || id != "container" || execID != "exec" {
		panic("bad cleanup")
	}
	d.record("stop")
	return d.stopErr
}
func (d *fakeCodexProcessDocker) Remove(ctx context.Context, id string) error {
	if ctx.Err() != nil || id != "container" {
		panic("bad remove")
	}
	d.record("remove")
	return d.removeErr
}

func TestCodexProcessSpec(t *testing.T) {
	opts := codexProcessCreateOptions()
	if !reflect.DeepEqual(opts.Cmd, []string{"codex", "app-server", "--listen", "stdio://"}) || opts.WorkingDir != "/workspace" || !opts.AttachStdin || !opts.AttachStdout || !opts.AttachStderr || opts.TTY || opts.Privileged || !reflect.DeepEqual(opts.Env, []string{"CODEX_HOME=/run/codex-process"}) {
		t.Fatalf("unsafe fixed spec: %+v", opts)
	}
	// Returned slices cannot change the next process specification.
	opts.Cmd[0] = "shell"
	if codexProcessCreateOptions().Cmd[0] != "codex" {
		t.Fatal("mutable process spec")
	}
}

func TestCodexProcessStartWriteReadFramingAndStderr(t *testing.T) {
	reader, writer := io.Pipe()
	d := &fakeCodexProcessDocker{output: reader}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	transport, err := startCodexProcessRuntime(ctx, d, "container")
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	defer reader.Close()
	defer writer.Close()
	request, err := EncodeCodexInitialize(CodexIntegerID(1), CodexClientInfo{Name: "fake", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Write(request); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	got := d.input.String()
	d.mu.Unlock()
	if got != string(request)+"\n" {
		t.Fatalf("stdin framing: %q", got)
	}
	go func() {
		writer.Write(codexDockerFrame(2, "TEST_SECRET_DO_NOT_LEAK\n{\"method\":\"fake-stderr\"}\n"))
		writer.Write(codexDockerFrame(1, `{"id":1,"result":`))
		writer.Write(codexDockerFrame(1, "{\"userAgent\":\"fake\",\"codexHome\":\"/tmp/codex\",\"platformFamily\":\"unix\",\"platformOs\":\"linux\"}}\n{\"method\":\"initialized\"}\n"))
	}()
	message, err := transport.Read()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCodexMessage(message, &CodexResponseExpectation{ID: CodexIntegerID(1), Method: CodexInitialize})
	if err != nil || decoded.Response.Initialize.UserAgent != "fake" {
		t.Fatalf("codec not reusable: %v", err)
	}
	message, err = transport.Read()
	if err != nil || string(message) != `{"method":"initialized"}` {
		t.Fatalf("messages not separated: %s %v", message, err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed || !reflect.DeepEqual(d.calls, []string{"start", "closeWrite", "stop", "remove"}) {
		t.Fatalf("cleanup: %v", d.calls)
	}
}

func TestCodexProcessTransportFailuresCleanUp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output []byte
		write  bool
	}{
		{"unexpected_eof", nil, false},
		{"partial_message", codexDockerFrame(1, `{"id":1}`), false},
		{"partial_header", []byte{1, 0, 0}, false},
		{"partial_frame", []byte{1, 0, 0, 0, 0, 0, 0, 9, '{'}, false},
		{"unknown_stream", codexDockerFrame(3, "fake"), false},
		{"oversized_line", codexDockerFrame(1, strings.Repeat("x", codexProcessMessageLimit+1)), false},
		{"broken_pipe", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output io.Reader = bytes.NewReader(tc.output)
			var reader *io.PipeReader
			var writer *io.PipeWriter
			if tc.write {
				reader, writer = io.Pipe()
				output = reader
				defer reader.Close()
				defer writer.Close()
			}
			d := &fakeCodexProcessDocker{output: output, writeErr: tc.write}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			tr, err := startCodexProcessRuntime(ctx, d, "container")
			if err != nil {
				t.Fatal(err)
			}
			if tc.write {
				err = tr.Write([]byte(`{"method":"initialized"}`))
			} else {
				_, err = tr.Read()
			}
			if err == nil || strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK") {
				t.Fatalf("failure not explicit/redacted: %v", err)
			}
			tr.Close()
			d.mu.Lock()
			defer d.mu.Unlock()
			if !d.closed || d.calls[len(d.calls)-1] != "remove" {
				t.Fatal("failure skipped cleanup")
			}
		})
	}
}

func TestCodexProcessStartFailureAndCleanupError(t *testing.T) {
	d := &fakeCodexProcessDocker{startErr: errors.New("TEST_SECRET_DO_NOT_LEAK")}
	if tr, err := startCodexProcessRuntime(context.Background(), d, "container"); tr != nil || err == nil || strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK") {
		t.Fatalf("bad start failure: %v", err)
	}
	if !reflect.DeepEqual(d.calls, []string{"start", "remove"}) {
		t.Fatal(d.calls)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	d = &fakeCodexProcessDocker{output: reader, stopErr: errors.New("TEST_SECRET_DO_NOT_LEAK"), removeErr: errors.New("TEST_SECRET_DO_NOT_LEAK")}
	tr, err := startCodexProcessRuntime(context.Background(), d, "container")
	if err != nil {
		t.Fatal(err)
	}
	err = tr.Close()
	if err == nil || strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK") || d.calls[len(d.calls)-1] != "remove" {
		t.Fatalf("cleanup failure: %v %v", err, d.calls)
	}
}

func TestCodexProcessRejectsMultipleMessagesAndCanceledContext(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	d := &fakeCodexProcessDocker{output: reader}
	ctx, cancel := context.WithCancel(context.Background())
	tr, err := startCodexProcessRuntime(ctx, d, "container")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"", "{}\n{}", "{", strings.Repeat("x", codexProcessMessageLimit+1)} {
		if tr.Write([]byte(message)) == nil {
			t.Fatal("invalid framing accepted")
		}
	}
	cancel()
	if _, err := tr.Read(); err == nil {
		t.Fatal("cancel did not unblock read")
	}
	tr.Close()
	if tr.Write([]byte("{}")) == nil {
		t.Fatal("write after close")
	}
}
