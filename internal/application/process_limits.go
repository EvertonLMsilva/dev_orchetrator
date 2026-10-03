package application

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const MaxGitOutputBytes = 1024 * 1024
const MaxTestOutputBytes = 1024 * 1024 // Per stream.
const RunTestsTimeout = 60 * time.Second

var ErrInvalidProcessLimits = errors.New("process limits must be positive")
var ErrProcessOutputLimit = errors.New("process output exceeds limit")

// ProcessLimits is an immutable value. Its zero value is invalid.
type ProcessLimits struct {
	timeout     time.Duration
	stdoutBytes int
	stderrBytes int
}

func NewProcessLimits(timeout time.Duration, stdoutBytes, stderrBytes int) (ProcessLimits, error) {
	limits := ProcessLimits{timeout, stdoutBytes, stderrBytes}
	if err := limits.validate(); err != nil {
		return ProcessLimits{}, err
	}
	return limits, nil
}

func (l ProcessLimits) Timeout() time.Duration { return l.timeout }
func (l ProcessLimits) StdoutBytes() int       { return l.stdoutBytes }
func (l ProcessLimits) StderrBytes() int       { return l.stderrBytes }
func (l ProcessLimits) validate() error {
	if l.timeout <= 0 || l.stdoutBytes <= 0 || l.stderrBytes <= 0 {
		return ErrInvalidProcessLimits
	}
	return nil
}

func gitProcessLimits() ProcessLimits {
	return ProcessLimits{10 * time.Second, MaxGitOutputBytes, MaxGitOutputBytes}
}

func testProcessLimits() ProcessLimits {
	return ProcessLimits{RunTestsTimeout, MaxTestOutputBytes, MaxTestOutputBytes}
}

// WithTimeout preserves cancellation and the earlier caller deadline.
func processContext(ctx context.Context, limits ProcessLimits) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, limits.Timeout())
}

type processOutput struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	stop     func()
}

func (b *processOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.exceeded = true
		b.stop()
		return 0, ErrProcessOutputLimit
	}
	return b.buffer.Write(p)
}

// runLimitedProcess only captures a command already constructed with the
// process context by a capability. It exposes no executable/argv API to Actions.
// CommandContext/overflow kill the direct process only. Reliable descendant
// cleanup needs platform-specific hardening (Windows and Linux). WaitDelay
// bounds inherited-pipe waiting; it does not guarantee process-tree cleanup.
func runLimitedProcess(ctx context.Context, cmd *exec.Cmd, limits ProcessLimits) ([]byte, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := limits.validate(); err != nil {
		return nil, nil, err
	}
	stop := func() { _ = cmd.Process.Kill() }
	stdout := processOutput{limit: limits.StdoutBytes(), stop: stop}
	stderr := processOutput{limit: limits.StderrBytes(), stop: stop}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if stdout.exceeded || stderr.exceeded {
		err = ErrProcessOutputLimit
	}
	return stdout.buffer.Bytes(), stderr.buffer.Bytes(), err
}
