package infrastructure

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"dev-orchestrator/internal/ports"
)

type fakeAuthSource struct {
	material []byte
	err      error
}

func (s fakeAuthSource) obtain(context.Context) ([]byte, error) { return s.material, s.err }

type authRecordingDocker struct {
	recordingDocker
	prepareErr error
	received   bool
	target     string
}

func (d *authRecordingDocker) prepareAuth(_ context.Context, _ string, target string, material []byte) error {
	d.calls = append(d.calls, "auth")
	d.target = target
	d.received = string(material) == "TEST_SECRET_DO_NOT_LEAK"
	return d.prepareErr
}

func TestChatGPTAuthIsolationAndCleanup(t *testing.T) {
	material := []byte("TEST_SECRET_DO_NOT_LEAK")
	d := &authRecordingDocker{}
	e := newAuthenticatedDockerEnvironment(d, fakeAuthSource{material: material})
	if err := e.RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"}); err != nil {
		t.Fatal("authenticated lifecycle failed")
	}
	if !d.received || d.target != "/run/codex-auth/auth.json" {
		t.Fatal("auth delivery failed")
	}
	if d.config.AuthTmpfsTarget() != "/run/codex-auth" || strings.HasPrefix(d.config.AuthTmpfsTarget(), "/workspace") {
		t.Fatal("unsafe auth storage")
	}
	if d.config.WorkspaceSource() != "/trusted/project" || d.config.WorkspaceTarget() != "/workspace" || d.config.WorkingDirectory() != "/workspace" || d.config.Privileged() {
		t.Fatal("workspace isolation changed")
	}
	if !reflect.DeepEqual(d.calls, []string{"create", "start", "auth", "stop", "remove"}) {
		t.Fatal("invalid lifecycle order")
	}
	for _, b := range material {
		if b != 0 {
			t.Fatal("auth material retained after cleanup")
		}
	}
	if strings.Contains(fmt.Sprintf("%+v", d.config), "TEST_SECRET_DO_NOT_LEAK") {
		t.Fatal("configuration leaks auth")
	}
}

func TestChatGPTAuthFailuresAreClosedAndRedacted(t *testing.T) {
	for _, stage := range []string{"source", "empty", "missing", "prepare", "create", "start", "stop", "remove"} {
		t.Run(stage, func(t *testing.T) {
			secretErr := errors.New("TEST_SECRET_DO_NOT_LEAK")
			source := fakeAuthSource{material: []byte("TEST_SECRET_DO_NOT_LEAK")}
			d := &authRecordingDocker{}
			switch stage {
			case "source":
				source.err = secretErr
			case "empty":
				source.material = nil
			case "missing":
				source.material = nil
			case "prepare":
				d.prepareErr = secretErr
			case "create":
				d.createErr = secretErr
				d.partialCreate = true
			case "start":
				d.startErr = secretErr
			case "stop":
				d.stopErr = secretErr
			case "remove":
				d.removeErr = secretErr
			}
			var authSource chatGPTAuthSource = source
			if stage == "missing" {
				authSource = nil
			}
			err := newAuthenticatedDockerEnvironment(d, authSource).RunLifecycle(context.Background(), ports.RuntimeExecutionRequest{Workspace: "/trusted/project"})
			if err == nil || strings.Contains(err.Error(), "TEST_SECRET_DO_NOT_LEAK") {
				t.Fatal("missing or sensitive error")
			}
			if stage == "source" || stage == "empty" || stage == "missing" {
				if len(d.calls) != 0 {
					t.Fatal("source failure reached Docker")
				}
			}
			if stage == "prepare" && !reflect.DeepEqual(d.calls, []string{"create", "start", "auth", "remove"}) {
				t.Fatal("prepare failure started execution or skipped cleanup")
			}
			if stage == "start" && !reflect.DeepEqual(d.calls, []string{"create", "start", "remove"}) {
				t.Fatal("start failure reached auth")
			}
			for _, b := range source.material {
				if b != 0 {
					t.Fatal("failed lifecycle retained auth")
				}
			}
		})
	}
}
