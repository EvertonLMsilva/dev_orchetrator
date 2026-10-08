//go:build linux

package main

import (
	"context"
	"dev-orchestrator/internal/composition"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

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
