package application

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
	"unicode/utf8"
)

func TestExecutionLimitsValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		output  int
		valid   bool
	}{
		{"valid", time.Second, 8, true}, {"zero_timeout", 0, 8, false},
		{"negative_timeout", -1, 8, false}, {"zero_output", time.Second, 0, false},
		{"negative_output", time.Second, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewExecutionLimits(tc.timeout, tc.output)
			if tc.valid {
				if err != nil || l.Validate() != nil || l.Timeout() != tc.timeout || l.MaxOutputBytes() != tc.output {
					t.Fatalf("limits: %+v %v", l, err)
				}
			} else if !errors.Is(err, ErrInvalidExecutionLimits) || l != (ExecutionLimits{}) {
				t.Fatalf("invalid accepted: %+v %v", l, err)
			}
		})
	}
	if !errors.Is((ExecutionLimits{}).Validate(), ErrInvalidExecutionLimits) {
		t.Fatal("zero limits accepted")
	}
	ctx, cancel, err := BoundedExecutionContext(context.Background(), ExecutionLimits{})
	if !errors.Is(err, ErrInvalidExecutionLimits) || ctx != nil || cancel != nil {
		t.Fatal("invalid context created")
	}
	if out, err := NewExecutionOutput(ExecutionLimits{}); out != nil || !errors.Is(err, ErrInvalidExecutionLimits) {
		t.Fatal("invalid output created")
	}
}

func TestBoundedExecutionContext(t *testing.T) {
	l, _ := NewExecutionLimits(20*time.Millisecond, 8)
	ctx, cancel, err := BoundedExecutionContext(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if _, ok := ctx.Deadline(); !ok {
		t.Fatal("missing deadline")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("timeout did not cancel")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal(ctx.Err())
	}
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	ctx, cancel, err = BoundedExecutionContext(parent, l)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	stop()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal(ctx.Err())
	}
	parent, stop = context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	ctx, cancel, err = BoundedExecutionContext(parent, l)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	d, _ := ctx.Deadline()
	pd, _ := parent.Deadline()
	if !d.Equal(pd) {
		t.Fatal("parent deadline extended")
	}
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("cancel func ineffective")
	}
}

func TestExecutionOutput(t *testing.T) {
	l, _ := NewExecutionLimits(time.Second, 5)
	out, err := NewExecutionOutput(l)
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Append("é"); err != nil || out.String() != "é" {
		t.Fatal("below limit", err)
	}
	if err := out.Append("🙂"); !errors.Is(err, ErrExecutionOutputLimit) || out.String() != "é" {
		t.Fatal("partial overflow appended", err)
	}
	if err := out.Append("abc"); !errors.Is(err, ErrExecutionOutputLimit) || out.String() != "é" {
		t.Fatal("overflow not sticky", err)
	}
	out, _ = NewExecutionOutput(l)
	if err := out.Append("éabc"); err != nil || len(out.String()) != 5 {
		t.Fatal("exact limit", err)
	}
	if err := out.Append("x"); !errors.Is(err, ErrExecutionOutputLimit) || out.String() != "éabc" {
		t.Fatal("above limit", err)
	}
	if !utf8.ValidString(out.String()) {
		t.Fatal("invalid UTF-8")
	}
	out, _ = NewExecutionOutput(l)
	if err := out.Append(string([]byte{0xc3})); err == nil || out.String() != "" {
		t.Fatal("invalid UTF-8 accepted")
	}
	var zero ExecutionOutput
	if err := zero.Append("x"); !errors.Is(err, ErrInvalidExecutionLimits) {
		t.Fatal("zero output accepted")
	}
}

func TestExecutorSessionFinish(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		state ExecutorSessionState
	}{
		{"normal", nil, ExecutorSessionCompleted}, {"ordinary", errors.New("timeout in text"), ExecutorSessionFailed},
		{"cancel", fmt.Errorf("wrapped: %w", context.Canceled), ExecutorSessionCancelled},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), ExecutorSessionCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := testExecutorSession(t).Finish(context.Background(), tc.err)
			if err != nil || s.State() != tc.state {
				t.Fatalf("%s %v", s.State(), err)
			}
			if _, err := s.Finish(context.Background(), nil); !errors.Is(err, ErrInvalidExecutorSessionTransition) {
				t.Fatal("terminal reused")
			}
		})
	}
	for _, timeout := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if timeout {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		} else {
			cancel()
		}
		s, err := testExecutorSession(t).Finish(ctx, errors.New("transport closed"))
		cancel()
		if err != nil || s.State() != ExecutorSessionCancelled {
			t.Fatal("context cancellation lost", err)
		}
	}
}
