package application

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProcessLimitsValidation(t *testing.T) {
	for _, tc := range []struct {
		timeout        time.Duration
		stdout, stderr int
		valid          bool
	}{
		{time.Second, 10, 20, true}, {0, 10, 20, false}, {-1, 10, 20, false},
		{time.Second, 0, 20, false}, {time.Second, -1, 20, false},
		{time.Second, 10, 0, false}, {time.Second, 10, -1, false},
	} {
		limits, err := NewProcessLimits(tc.timeout, tc.stdout, tc.stderr)
		if tc.valid {
			if err != nil || limits.Timeout() != tc.timeout || limits.StdoutBytes() != tc.stdout || limits.StderrBytes() != tc.stderr {
				t.Fatalf("%+v: %v", limits, err)
			}
		} else if !errors.Is(err, ErrInvalidProcessLimits) {
			t.Fatalf("invalid limits: %v", err)
		}
	}
	typ := reflect.TypeOf(ProcessLimits{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).IsExported() {
			t.Fatal("mutable exported field")
		}
	}
}

func TestProcessDefaults(t *testing.T) {
	for _, tc := range []struct {
		limits  ProcessLimits
		timeout time.Duration
	}{{gitProcessLimits(), 10 * time.Second}, {testProcessLimits(), 60 * time.Second}} {
		if tc.limits.Timeout() != tc.timeout || tc.limits.StdoutBytes() != 1024*1024 || tc.limits.StderrBytes() != 1024*1024 {
			t.Fatalf("%+v", tc.limits)
		}
	}
}

func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("P28_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "stdout":
		fmt.Print(strings.Repeat("x", 32))
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 32))
	case "stdout-overflow":
		fmt.Print(strings.Repeat("x", 32))
		time.Sleep(10 * time.Second)
	case "stderr-overflow":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 32))
		time.Sleep(10 * time.Second)
	case "sleep":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func helperCommand(ctx context.Context, mode string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProcessHelper$")
	cmd.Env = append(os.Environ(), "P28_HELPER="+mode)
	return cmd
}

func TestProcessStreams(t *testing.T) {
	for _, stream := range []string{"stdout", "stderr"} {
		for _, limit := range []int{32, 16} {
			t.Run(fmt.Sprintf("%s/%d", stream, limit), func(t *testing.T) {
				limits, _ := NewProcessLimits(5*time.Second, limit, limit)
				ctx, cancel := processContext(context.Background(), limits)
				defer cancel()
				mode := stream
				if limit == 16 {
					mode += "-overflow"
				}
				start := time.Now()
				out, errout, err := runLimitedProcess(ctx, helperCommand(ctx, mode), limits)
				if len(out) > limit || len(errout) > limit {
					t.Fatal("unbounded output")
				}
				if limit == 16 {
					if !errors.Is(err, ErrProcessOutputLimit) || time.Since(start) > 2*time.Second {
						t.Fatal(err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if stream == "stdout" && string(out) != strings.Repeat("x", 32) || stream == "stderr" && string(errout) != strings.Repeat("x", 32) {
					t.Fatal("wrong captured stream")
				}
			})
		}
	}
}

func TestProcessContext(t *testing.T) {
	for _, mode := range []string{"canceled", "caller", "default", "longer", "cancel-running"} {
		t.Run(mode, func(t *testing.T) {
			parent := context.Background()
			var stop context.CancelFunc = func() {}
			want := context.DeadlineExceeded
			limits, _ := NewProcessLimits(100*time.Millisecond, 32, 32)
			switch mode {
			case "canceled":
				parent, stop = context.WithCancel(parent)
				stop()
				want = context.Canceled
			case "caller":
				parent, stop = context.WithTimeout(parent, 50*time.Millisecond)
				limits, _ = NewProcessLimits(5*time.Second, 32, 32)
			case "longer":
				parent, stop = context.WithTimeout(parent, 5*time.Second)
			case "cancel-running":
				parent, stop = context.WithCancel(parent)
				time.AfterFunc(50*time.Millisecond, stop)
				want = context.Canceled
			}
			defer stop()
			ctx, cancel := processContext(parent, limits)
			defer cancel()
			cmd := helperCommand(ctx, "sleep")
			start := time.Now()
			_, _, err := runLimitedProcess(ctx, cmd, limits)
			if !errors.Is(err, want) || time.Since(start) > 2*time.Second {
				t.Fatalf("%v after %v", err, time.Since(start))
			}
			if mode == "canceled" && cmd.Process != nil {
				t.Fatal("started canceled process")
			}
		})
	}
	cmd := helperCommand(context.Background(), "stdout")
	if _, _, err := runLimitedProcess(context.Background(), cmd, ProcessLimits{}); !errors.Is(err, ErrInvalidProcessLimits) || cmd.Process != nil {
		t.Fatalf("zero limits: %v", err)
	}
}
