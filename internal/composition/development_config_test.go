//go:build linux

package composition

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func developmentConfigFixture(t *testing.T) DevelopmentConfig {
	t.Helper()
	data, err := os.ReadFile("testdata/development.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var c DevelopmentConfig
	if json.Unmarshal(data, &c) != nil {
		t.Fatal("example")
	}
	c.ControlRoot = t.TempDir()
	c.ScratchRoot = t.TempDir()
	c.StateRoot = t.TempDir()
	for _, dir := range []string{c.ControlRoot, c.ScratchRoot, c.StateRoot} {
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return c
}
func TestDevelopmentConfigAndRestartFailClosed(t *testing.T) {
	c := developmentConfigFixture(t)
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := c
	bad.Mappings = append(bad.Mappings, bad.Mappings[0])
	if err := bad.Validate(); err == nil {
		t.Fatal("duplicate mapping")
	}
	bad = c
	bad.Grants = append(bad.Grants, bad.Grants[0])
	if err := bad.Validate(); err == nil {
		t.Fatal("duplicate grant")
	}
	bad = c
	bad.ScratchRoot = c.ControlRoot
	if err := bad.Validate(); err == nil {
		t.Fatal("root alias")
	}
	bad = c
	bad.ScratchRoot = "/var/lib/dev-orchestrator/runtime-auth"
	if err := bad.Validate(); err == nil {
		t.Fatal("auth root")
	}
	if err := os.WriteFile(filepath.Join(c.StateRoot, "tasks.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDevelopmentService(context.Background(), c); err == nil {
		t.Fatal("startup replay")
	}
	entries, err := os.ReadDir(c.ControlRoot)
	if err != nil || len(entries) != 0 {
		t.Fatal("bootstrap mutation after denied replay", err)
	}
	if _, err := os.Stat(filepath.Join(c.StateRoot, "instance.lock")); !os.IsNotExist(err) {
		t.Fatal("instance lease cleanup")
	}
}
