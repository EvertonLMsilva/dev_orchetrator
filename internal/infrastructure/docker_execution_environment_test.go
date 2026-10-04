package infrastructure

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"dev-orchestrator/internal/ports"
)

type recordingDocker struct {
	config                                  DockerEnvironmentConfig
	calls                                   []string
	createErr, startErr, stopErr, removeErr error
	cleanupCanceled                         bool
	partialCreate, emptyID                  bool
}

func (d *recordingDocker) Create(_ context.Context, config DockerEnvironmentConfig) (string, error) {
	d.calls = append(d.calls, "create")
	d.config = config
	if d.partialCreate {
		return "container-id", d.createErr
	}
	if d.emptyID {
		return "", nil
	}
	if d.createErr != nil {
		return "", d.createErr
	}
	return "container-id", nil
}
func (d *recordingDocker) Start(_ context.Context, id string) error {
	if id != "container-id" {
		panic("unexpected container")
	}
	d.calls = append(d.calls, "start")
	return d.startErr
}
func (d *recordingDocker) Stop(ctx context.Context, id string) error {
	if id != "container-id" {
		panic("unexpected container")
	}
	d.cleanupCanceled = d.cleanupCanceled || ctx.Err() != nil
	d.calls = append(d.calls, "stop")
	return d.stopErr
}
func (d *recordingDocker) Remove(ctx context.Context, id string) error {
	if id != "container-id" {
		panic("unexpected container")
	}
	d.cleanupCanceled = d.cleanupCanceled || ctx.Err() != nil
	d.calls = append(d.calls, "remove")
	return d.removeErr
}

func TestDockerEnvironmentIsolationAndSuccessCleanup(t *testing.T) {
	d := &recordingDocker{}
	err := NewDockerExecutionEnvironment(d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{
		Workspace: "/trusted/project", Objective: "ignored", Scope: []string{"/var/run/docker.sock"},
		Constraints: []string{"privileged=true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.config.WorkspaceSource() != "/trusted/project" || d.config.WorkspaceTarget() != "/workspace" || d.config.WorkingDirectory() != "/workspace" || d.config.Privileged() {
		t.Fatalf("unsafe configuration: %+v", d.config)
	}
	// Configuration has one private source, with no fields for additional mounts,
	// commands or privileges. Its exported API exposes only read access.
	typ := reflect.TypeOf(d.config)
	if typ.NumField() != 1 || typ.Field(0).IsExported() {
		t.Fatal("configuration exposes caller controls")
	}
	if !reflect.DeepEqual(d.calls, []string{"create", "start", "stop", "remove"}) {
		t.Fatal(d.calls)
	}
}

func TestDockerEnvironmentFailures(t *testing.T) {
	createErr := errors.New("create failed")
	startErr := errors.New("start failed")
	stopErr := errors.New("stop failed")
	removeErr := errors.New("remove failed")
	for _, tt := range []struct {
		name  string
		d     recordingDocker
		calls []string
		want  []error
	}{
		{"create", recordingDocker{createErr: createErr}, []string{"create"}, []error{createErr}},
		{"partial create", recordingDocker{partialCreate: true, createErr: createErr, removeErr: removeErr}, []string{"create", "remove"}, []error{createErr, removeErr}},
		{"start", recordingDocker{startErr: startErr}, []string{"create", "start", "remove"}, []error{startErr}},
		{"start and remove", recordingDocker{startErr: startErr, removeErr: removeErr}, []string{"create", "start", "remove"}, []error{startErr, removeErr}},
		{"stop", recordingDocker{stopErr: stopErr}, []string{"create", "start", "stop", "remove"}, []error{stopErr}},
		{"remove", recordingDocker{removeErr: removeErr}, []string{"create", "start", "stop", "remove"}, []error{removeErr}},
		{"stop and remove", recordingDocker{stopErr: stopErr, removeErr: removeErr}, []string{"create", "start", "stop", "remove"}, []error{stopErr, removeErr}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := NewDockerExecutionEnvironment(&tt.d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
			for _, want := range tt.want {
				if !errors.Is(err, want) {
					t.Fatalf("error %v does not preserve %v", err, want)
				}
			}
			if !reflect.DeepEqual(tt.d.calls, tt.calls) {
				t.Fatal(tt.d.calls)
			}
		})
	}
}

func TestDockerEnvironmentRejectsInvalidWorkspace(t *testing.T) {
	for _, workspace := range []string{"", " ", "relative", "/", "/trusted/..", "/var/run/docker.sock", "/trusted/\x00project"} {
		t.Run(workspace, func(t *testing.T) {
			d := &recordingDocker{}
			err := NewDockerExecutionEnvironment(d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: workspace})
			if err == nil || len(d.calls) != 0 {
				t.Fatalf("workspace accepted: %q, calls=%v", workspace, d.calls)
			}
		})
	}
}

type cancelOnStartDocker struct {
	*recordingDocker
	cancel context.CancelFunc
}

func (d cancelOnStartDocker) Start(ctx context.Context, id string) error {
	d.cancel()
	return d.recordingDocker.Start(ctx, id)
}

func TestDockerEnvironmentCancellationStillCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &recordingDocker{startErr: context.Canceled}
	err := NewDockerExecutionEnvironment(cancelOnStartDocker{d, cancel}).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if !errors.Is(err, context.Canceled) || d.cleanupCanceled || !reflect.DeepEqual(d.calls, []string{"create", "start", "remove"}) {
		t.Fatalf("error=%v calls=%v canceled=%v", err, d.calls, d.cleanupCanceled)
	}
}

func TestDockerEnvironmentMissingDriver(t *testing.T) {
	if NewDockerExecutionEnvironment(nil).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"}) == nil {
		t.Fatal("missing driver accepted")
	}
}

func TestDockerEnvironmentEmptyContainerID(t *testing.T) {
	d := &recordingDocker{emptyID: true}
	err := NewDockerExecutionEnvironment(d).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if err == nil || !reflect.DeepEqual(d.calls, []string{"create"}) {
		t.Fatalf("error=%v calls=%v", err, d.calls)
	}
}

func TestDockerEnvironmentCanceledBeforeCreate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &recordingDocker{}
	err := NewDockerExecutionEnvironment(d).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if !errors.Is(err, context.Canceled) || len(d.calls) != 0 {
		t.Fatalf("error=%v calls=%v", err, d.calls)
	}
}

func TestDockerEnvironmentCanceledAfterStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &recordingDocker{}
	err := NewDockerExecutionEnvironment(cancelOnStartDocker{d, cancel}).RunLifecycle(ctx, ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
	if !errors.Is(err, context.Canceled) || d.cleanupCanceled || !reflect.DeepEqual(d.calls, []string{"create", "start", "stop", "remove"}) {
		t.Fatalf("error=%v calls=%v canceled=%v", err, d.calls, d.cleanupCanceled)
	}
}
