package infrastructure

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

const codexProcessMessageLimit = 1024 * 1024

// Attachment is infrastructure-only. Docker's ExecAttach starts the exec while
// upgrading the HTTP connection; a second ExecStart would start it twice.
type codexProcessAttachment struct {
	execID     string
	input      io.Writer
	output     io.Reader
	close      func()
	closeWrite func() error
}

type codexProcessDocker interface {
	startCodexProcess(context.Context, string) (codexProcessAttachment, error)
	stopCodexProcess(context.Context, string, string) error
	Remove(context.Context, string) error
}

// CodexProcessTransport owns app-server lifecycle. The default startup also owns
// its dedicated container, including failure cleanup; lease startup leaves the
// container and tmpfs lifetime with RuntimeHomeLease. Neither creates RPC tasks.
// The codec remains separate: Write accepts encoded bytes; Read returns one line.
type CodexProcessTransport struct {
	docker      codexProcessDocker
	containerID string
	attachment  codexProcessAttachment
	output      *io.PipeReader
	reader      *bufio.Reader
	pumpDone    chan struct{}
	closed      chan struct{}
	writeMu     sync.Mutex
	readMu      sync.Mutex
	closeOnce   sync.Once
	closeErr    error
	leaseOwned  bool
}

type leaseCodexProcessDocker interface {
	codexProcessDocker
	stopCodexAppServer(context.Context, string, string) error
}

// The caller must already own the container through a RuntimeHomeLease. Startup
// failures and Close leave final destruction to that lease, including failures
// to confirm process termination. No ownership transfers to the transport.
func startLeaseCodexProcessRuntime(ctx context.Context, docker leaseCodexProcessDocker, id string) (*CodexProcessTransport, error) {
	return startOwnedCodexProcessRuntime(ctx, docker, id, true)
}

func startCodexProcessRuntime(ctx context.Context, docker codexProcessDocker, containerID string) (*CodexProcessTransport, error) {
	return startOwnedCodexProcessRuntime(ctx, docker, containerID, false)
}

func startOwnedCodexProcessRuntime(ctx context.Context, docker codexProcessDocker, containerID string, leaseOwned bool) (*CodexProcessTransport, error) {
	if docker == nil || containerID == "" {
		return nil, errors.New("codex process requires owned container and driver")
	}
	attachment, err := docker.startCodexProcess(ctx, containerID)
	if err != nil || attachment.execID == "" || attachment.input == nil || attachment.output == nil || attachment.close == nil || attachment.closeWrite == nil {
		if attachment.close != nil {
			attachment.close()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
		defer cancel()
		failure := errors.New("codex process start/attach failed")
		if !leaseOwned {
			if err := docker.Remove(cleanup, containerID); err != nil {
				failure = errors.Join(failure, errors.New("codex process container cleanup failed"))
			}
		}
		return nil, failure
	}
	output, writer := io.Pipe()
	t := &CodexProcessTransport{docker: docker, containerID: containerID, attachment: attachment, output: output, reader: bufio.NewReader(output), pumpDone: make(chan struct{}), closed: make(chan struct{}), leaseOwned: leaseOwned}
	go func() {
		defer close(t.pumpDone)
		// stderr is drained and discarded: zero retained diagnostic bytes. This
		// prevents secrets and stderr JSON from reaching the protocol or logs.
		err := copyCodexProcessStdout(writer, attachment.output)
		if err != nil {
			writer.CloseWithError(errors.New("codex process stdout ended or Docker stream malformed"))
		} else {
			writer.Close()
		}
	}()
	go func() {
		select {
		case <-ctx.Done():
		case <-t.pumpDone:
		case <-t.closed:
			return
		}
		_ = t.Close()
	}()
	return t, nil
}

// Write frames exactly one JSON value, with a bounded single-line encoding.
// Compact only removes JSON whitespace; it does not interpret RPC semantics.
func (t *CodexProcessTransport) Write(message []byte) error {
	if len(message) > codexProcessMessageLimit {
		return errors.New("codex process message limit exceeded")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, message); err != nil {
		return errors.New("codex process requires one complete JSON message")
	}
	compact.WriteByte('\n')
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	select {
	case <-t.closed:
		return errors.New("codex process transport closed")
	default:
	}
	n, err := t.attachment.input.Write(compact.Bytes())
	if err != nil || n != compact.Len() {
		cleanupErr := t.Close()
		return errors.Join(errors.New("codex process stdin write failed"), cleanupErr)
	}
	return nil
}

// Read rejects an unterminated final message, even if its bytes are valid JSON.
// It bounds a line across arbitrary Docker frame and network read boundaries.
func (t *CodexProcessTransport) Read() ([]byte, error) {
	t.readMu.Lock()
	defer t.readMu.Unlock()
	var message []byte
	for {
		part, err := t.reader.ReadSlice('\n')
		if len(message)+len(part) > codexProcessMessageLimit+1 {
			return nil, errors.Join(errors.New("codex process message limit exceeded"), t.Close())
		}
		message = append(message, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, errors.Join(errors.New("codex process unexpected EOF/read failure"), t.Close())
		}
		message = bytes.TrimSuffix(message, []byte{'\n'})
		message = bytes.TrimSuffix(message, []byte{'\r'})
		if len(message) == 0 {
			return nil, errors.Join(errors.New("codex process empty message"), t.Close())
		}
		return message, nil
	}
}

// Close is idempotent. Default sessions always attempt container removal; lease
// sessions only confirm app-server exit after stdin EOF. Cleanup uses fresh,
// bounded contexts independent of caller cancellation and omits daemon output.
func (t *CodexProcessTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.closed)
		if err := t.attachment.closeWrite(); err != nil {
			t.closeErr = errors.New("codex process stdin close failed")
		}
		t.attachment.close()
		_ = t.output.Close()
		ctx, cancel := context.WithTimeout(context.Background(), dockerOperationTimeout)
		if t.leaseOwned {
			if t.docker.(leaseCodexProcessDocker).stopCodexAppServer(ctx, t.containerID, t.attachment.execID) != nil {
				t.closeErr = errors.Join(t.closeErr, errors.New("codex process termination failed"))
			}
			cancel()
			return
		}
		if err := t.docker.stopCodexProcess(ctx, t.containerID, t.attachment.execID); err != nil {
			t.closeErr = errors.Join(t.closeErr, errors.New("codex process termination failed"))
		}
		cancel()
		ctx, cancel = context.WithTimeout(context.Background(), dockerOperationTimeout)
		if err := t.docker.Remove(ctx, t.containerID); err != nil {
			t.closeErr = errors.Join(t.closeErr, errors.New("codex process container cleanup failed"))
		}
		cancel()
	})
	return t.closeErr
}

// Docker non-TTY attach frames have an 8-byte header followed by a payload.
// CopyN drains without allocating according to an untrusted frame size.
func copyCodexProcessStdout(stdout io.Writer, multiplexed io.Reader) error {
	var header [8]byte
	for {
		if _, err := io.ReadFull(multiplexed, header[:]); err != nil {
			return err
		}
		if header[1] != 0 || header[2] != 0 || header[3] != 0 {
			return errors.New("invalid Docker stream header")
		}
		var destination io.Writer
		switch header[0] {
		case 1:
			destination = stdout
		case 2:
			destination = io.Discard
		default:
			return errors.New("unsupported Docker stream")
		}
		size := int64(binary.BigEndian.Uint32(header[4:]))
		if size > codexProcessMessageLimit {
			return errors.New("Docker frame limit exceeded")
		}
		if _, err := io.CopyN(destination, multiplexed, size); err != nil {
			return err
		}
	}
}
