//go:build linux

package main

import (
	"context"
	"dev-orchestrator/internal/composition"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadinessIsLoopbackObservationalAndCloses(t *testing.T) {
	for _, address := range []string{"0.0.0.0:0", "localhost:0", "[::]:0"} {
		if _, _, err := developmentReadiness(address); err == nil {
			t.Fatal("unsafe listener accepted")
		}
	}
	address, close, err := developmentReadiness("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	response, err := http.Get("http://" + address + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	close()
	if _, err := http.Get("http://" + address + "/readyz"); err == nil {
		t.Fatal("readiness survived shutdown")
	}
}

type registrationTransport struct {
	t     *testing.T
	names []string
}

func (r *registrationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	r.t.Helper()
	if request.Method != http.MethodPost || request.URL.Host != "discord.com" || request.URL.Path != "/api/v10/applications/1557061194257403974/commands" {
		return nil, errors.New("unexpected network operation during registration")
	}
	if request.Header.Get("Authorization") != "Bot trusted-test-token" {
		return nil, errors.New("trusted credential not used")
	}
	var command struct {
		Name    string `json:"name"`
		Options []struct {
			Name     string `json:"name"`
			Required bool   `json:"required"`
		} `json:"options"`
	}
	if err := json.NewDecoder(request.Body).Decode(&command); err != nil {
		return nil, err
	}
	if command.Name == "develop" && (len(command.Options) != 2 || command.Options[1].Name != "targets" || !command.Options[1].Required) {
		return nil, errors.New("required targets contract changed")
	}
	r.names = append(r.names, command.Name)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"123","application_id":"1557061194257403974","version":"1","type":1,"name":"` + command.Name + `","description":"test"}`)), Request: request}, nil
}

func operationalRegistrationFixture(t *testing.T) (string, []string) {
	t.Helper()
	data, err := os.ReadFile("../../internal/composition/testdata/operational-development.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var config composition.OperationalDevelopmentConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	var roots []string
	private := func() string {
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sentinel"), []byte("unchanged"), 0600); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
		return root
	}
	config.Registry.StateDir = private()
	config.ScratchRoot = private()
	for i := range config.Registry.Projects {
		config.Registry.Projects[i].Workspace = private()
	}
	// Registration must not need an operational lease, even with an existing one.
	if err := os.Mkdir(filepath.Join(config.Registry.StateDir, "instance.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "operational.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := composition.LoadOperationalDevelopmentConfig(path); err != nil {
		t.Fatal("invalid P11 fixture", err)
	}
	return path, roots
}

func TestExplicitRegistrationWithP11OperationalConfig(t *testing.T) {
	for _, operational := range []bool{false, true} {
		t.Run(map[bool]string{false: "administrative", true: "operational_flag"}[operational], func(t *testing.T) {
			path, roots := operationalRegistrationFixture(t)
			tokenPath := filepath.Join(t.TempDir(), "discord-token")
			if err := os.WriteFile(tokenPath, []byte("trusted-test-token\n"), 0600); err != nil {
				t.Fatal(err)
			}
			transport := &registrationTransport{t: t}
			previous := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			args := []string{"--config", path, "--discord-token-file", tokenPath, "--register-commands", "1557061194257403974"}
			if operational {
				args = append(args, "--operational")
			}
			if err := run(context.Background(), args); err != nil {
				t.Fatal("explicit registration rejected validated P11 config", err)
			}
			if !reflect.DeepEqual(transport.names, []string{"develop", "develop-confirm", "develop-cancel", "develop-status"}) {
				t.Fatal("wrong registered commands", transport.names)
			}
			for i, root := range roots {
				entries, err := os.ReadDir(root)
				want := 1
				if i == 0 {
					want = 2
				}
				if err != nil || len(entries) != want {
					t.Fatal("registration mutated operational storage/workspace", err)
				}
				data, err := os.ReadFile(filepath.Join(root, "sentinel"))
				if err != nil || string(data) != "unchanged" {
					t.Fatal("registration changed persisted data", err)
				}
			}
		})
	}
}

func TestExplicitRegistrationRejectsInvalidConfigCredentialAndID(t *testing.T) {
	for _, failure := range []string{"config", "credential", "application_id", "normal_startup"} {
		t.Run(failure, func(t *testing.T) {
			path, _ := operationalRegistrationFixture(t)
			tokenPath := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(tokenPath, []byte("trusted-test-token"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--config", path, "--discord-token-file", tokenPath, "--register-commands", "1557061194257403974"}
			switch failure {
			case "config":
				data, _ := os.ReadFile(path)
				data = append([]byte(`{"Untrusted":true,`), data[1:]...)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			case "credential":
				args[3] = filepath.Join(t.TempDir(), "missing")
			case "application_id":
				args[5] = "invalid"
			case "normal_startup":
				args = args[:4]
			}
			transport := &registrationTransport{t: t}
			previous := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			if err := run(context.Background(), args); err == nil {
				t.Fatal("invalid administrative invocation accepted")
			}
			if len(transport.names) != 0 {
				t.Fatal("invalid invocation reached Discord")
			}
		})
	}
}

func TestOfflineConfigModesRequireNoCredentialsOrEffects(t *testing.T) {
	for _, operational := range []bool{false, true} {
		t.Run(map[bool]string{false: "pilot", true: "operational"}[operational], func(t *testing.T) {
			private := func() string {
				d := t.TempDir()
				if err := os.Chmod(d, 0700); err != nil {
					t.Fatal(err)
				}
				return d
			}
			var config any
			var roots []string
			if operational {
				data, err := os.ReadFile("../../internal/composition/testdata/operational-development.example.json")
				if err != nil {
					t.Fatal(err)
				}
				var c composition.OperationalDevelopmentConfig
				if json.Unmarshal(data, &c) != nil {
					t.Fatal("example")
				}
				c.Registry.StateDir = private()
				c.ScratchRoot = private()
				roots = []string{c.Registry.StateDir, c.ScratchRoot}
				for i := range c.Registry.Projects {
					c.Registry.Projects[i].Workspace = private()
					roots = append(roots, c.Registry.Projects[i].Workspace)
				}
				config = c
			} else {
				data, err := os.ReadFile("../../internal/composition/testdata/development.example.json")
				if err != nil {
					t.Fatal(err)
				}
				var c composition.DevelopmentConfig
				if json.Unmarshal(data, &c) != nil {
					t.Fatal("example")
				}
				c.ControlRoot = private()
				c.ScratchRoot = private()
				c.StateRoot = private()
				roots = []string{c.ControlRoot, c.ScratchRoot, c.StateRoot}
				config = c
			}
			data, _ := json.Marshal(config)
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--config", path, "--check-config", "--discord-token-file", filepath.Join(t.TempDir(), "absent")}
			if operational {
				args = append(args, "--operational")
			}
			if err := run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			for _, root := range roots {
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatal("effect during offline check", err)
				}
			}
			// The other mode cannot silently accept this contract or fall back.
			if operational {
				args = args[:len(args)-1]
			} else {
				args = append(args, "--operational")
			}
			if err := run(context.Background(), args); err == nil {
				t.Fatal("mode contract bypass")
			}
		})
	}
}
