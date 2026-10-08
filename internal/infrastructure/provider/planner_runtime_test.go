package provider

import (
	"context"
	"dev-orchestrator/internal/infrastructure"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type plannerMemoryContainer struct {
	memoryRuntimeHome
	mode           string
	inferenceCalls int
}

type hostStartFailureContainer struct{ plannerMemoryContainer }

func (*hostStartFailureContainer) Infer(context.Context, infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error) {
	return infrastructure.PlannerRuntimeResult{}, errors.New("SECRET_HOST")
}
func (*hostStartFailureContainer) SanitizedFailureClass() string { return "host_start_failure" }

func TestPlannerFailureStages(t *testing.T) {
	for _, stage := range []string{"NEW_CONTAINER", "MATERIALIZE", "PREPARE", "HOST_START", "INFER", "TIMEOUT", "CANCELLED", "CLEANUP"} {
		t.Run(stage, func(t *testing.T) {
			store := testRuntimeStore(t)
			if stage != "MATERIALIZE" {
				if err := store.StoreSession(context.Background(), []byte(runtimeAuth)); err != nil {
					t.Fatal(err)
				}
			}
			c := &plannerMemoryContainer{}
			var container plannerContainer = c
			switch stage {
			case "PREPARE":
				c.fail = "prepare"
			case "HOST_START":
				container = &hostStartFailureContainer{}
			case "INFER":
				c.mode = "runtime"
			case "TIMEOUT", "CANCELLED":
				c.mode = "wait"
			case "CLEANUP":
				c.fail = "destroy"
			}
			r := &ToolFreeCodexPlannerRuntime{home: store, newContainer: func() (plannerContainer, error) {
				if stage == "NEW_CONTAINER" {
					return nil, errors.New("SECRET_DOCKER")
				}
				return container, nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			q := plannerRequest()
			q.Limits.Timeout = 20 * time.Millisecond
			if stage == "CANCELLED" {
				time.AfterFunc(5*time.Millisecond, cancel)
			}
			out, err := r.Plan(ctx, q)
			var failure interface{ FailureStage() string }
			if err == nil || !errors.As(err, &failure) || failure.FailureStage() != stage {
				t.Fatalf("stage %s lost: %v", stage, err)
			}
			if strings.Contains(err.Error(), "SECRET") || len(out.StructuredOutput) != 0 {
				t.Fatal("unsafe failure output")
			}
			if stage == "CLEANUP" {
				if err := store.acquire(context.Background()); !errors.Is(err, ErrRuntimeAuthBusy) {
					t.Fatal("cleanup released exclusion", err)
				}
			}
		})
	}
}

func (c *plannerMemoryContainer) Infer(ctx context.Context, _ infrastructure.PlannerRuntimeRequest) (infrastructure.PlannerRuntimeResult, error) {
	c.inferenceCalls++
	if c.mode == "wait" {
		<-ctx.Done()
		return infrastructure.PlannerRuntimeResult{}, ctx.Err()
	}
	if c.mode == "runtime" {
		return infrastructure.PlannerRuntimeResult{}, errors.New("SECRET_RUNTIME")
	}
	if c.mode == "refresh" {
		c.auth = []byte(strings.Replace(runtimeAuth, "SECRET_ACCESS", "REFRESHED_ACCESS", 1))
	}
	if c.mode == "output" {
		return infrastructure.PlannerRuntimeResult{StructuredOutput: []byte(strings.Repeat("x", 2048))}, nil
	}
	return infrastructure.PlannerRuntimeResult{StructuredOutput: []byte(`{"ok":true}`)}, nil
}

func plannerRequest() infrastructure.PlannerRuntimeRequest {
	return infrastructure.PlannerRuntimeRequest{Instructions: "Decide", Input: json.RawMessage(`{}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Limits: infrastructure.PlannerRuntimeLimits{MaxOutputBytes: 1024, Timeout: time.Second}}
}

func TestPlannerRuntimeCommitAfterLifecycle(t *testing.T) {
	for _, stage := range []string{"success", "refresh", "runtime", "prepare", "capture", "persist", "cleanup", "output", "auth"} {
		t.Run(stage, func(t *testing.T) {
			store := testRuntimeStore(t)
			if stage != "auth" {
				if err := store.StoreSession(context.Background(), []byte(runtimeAuth)); err != nil {
					t.Fatal(err)
				}
			}
			c := &plannerMemoryContainer{}
			switch stage {
			case "refresh", "runtime", "output":
				c.mode = stage
			case "prepare", "capture":
				c.fail = stage
			case "persist":
				c.mode = "refresh"
				failRuntimePersistence(store, "write")
			case "cleanup":
				c.fail = "destroy"
			}
			r := &ToolFreeCodexPlannerRuntime{home: store, newContainer: func() (plannerContainer, error) { return c, nil }}
			result, err := r.Plan(context.Background(), plannerRequest())
			success := stage == "success" || stage == "refresh"
			if (err == nil) != success {
				t.Fatalf("stage=%s err=%v", stage, err)
			}
			if !success && len(result.StructuredOutput) != 0 {
				t.Fatal("uncommitted result escaped")
			}
			if success && (!c.removed || string(result.StructuredOutput) != `{"ok":true}`) {
				t.Fatal("result escaped before cleanup")
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("diagnostic leaked")
			}
			if c.inferenceCalls > 1 {
				t.Fatal("second inference after failure")
			}
			if stage == "refresh" {
				data, readErr := store.read()
				defer clear(data)
				if readErr != nil || !strings.Contains(string(data), "REFRESHED_ACCESS") {
					t.Fatal("refresh not persisted")
				}
			}
		})
	}
}

func TestPlannerRuntimeCancelAndTimeout(t *testing.T) {
	for _, cancelCaller := range []bool{true, false} {
		store := testRuntimeStore(t)
		if store.StoreSession(context.Background(), []byte(runtimeAuth)) != nil {
			t.Fatal("fixture failed")
		}
		c := &plannerMemoryContainer{mode: "wait"}
		r := &ToolFreeCodexPlannerRuntime{home: store, newContainer: func() (plannerContainer, error) { return c, nil }}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		request := plannerRequest()
		request.Limits.Timeout = 20 * time.Millisecond
		if cancelCaller {
			time.AfterFunc(5*time.Millisecond, cancel)
		}
		result, err := r.Plan(ctx, request)
		expected := context.DeadlineExceeded
		if cancelCaller {
			expected = context.Canceled
		}
		if !errors.Is(err, expected) || len(result.StructuredOutput) != 0 || !c.removed {
			t.Fatalf("cancel/timeout not cleaned: %v", err)
		}
	}
}

func TestPlannerRuntimeInvalidInputBeforeAuth(t *testing.T) {
	r := &ToolFreeCodexPlannerRuntime{newContainer: func() (plannerContainer, error) { t.Fatal("invalid request reached runtime"); return nil, nil }}
	request := plannerRequest()
	request.Input = json.RawMessage(`{`)
	if _, err := r.Plan(context.Background(), request); !errors.Is(err, infrastructure.ErrInvalidPlannerRuntimeRequest) {
		t.Fatal(err)
	}
}
