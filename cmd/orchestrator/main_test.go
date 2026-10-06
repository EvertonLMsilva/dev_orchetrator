package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEntrypointFailClosed(t *testing.T) {
	for _, args := range [][]string{nil, {"-unknown"}, {"-config", "missing-config"}} {
		var output bytes.Buffer
		if run(context.Background(), args, &output) == nil {
			t.Fatal("invalid input accepted")
		}
		if output.Len() != 0 {
			t.Fatal("internal error leaked", output.String())
		}
	}
}

func TestEntrypointConfigCheckWithoutCredentialOrLive(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("official Linux validation")
	}
	state := t.TempDir()
	if e := os.Chmod(state, 0700); e != nil {
		t.Fatal(e)
	}
	workspace := t.TempDir()
	if e := os.WriteFile(filepath.Join(workspace, "public.txt"), []byte("public"), 0600); e != nil {
		t.Fatal(e)
	}
	config := map[string]any{
		"StateDir": state, "RequestTimeout": "75s", "ShutdownTimeout": "60s", "MaxIntentBytes": 4096, "MaxEvidenceBytes": 32768,
		"Projects": []any{map[string]any{"ID": "p", "Name": "Pilot", "Workspace": workspace, "Evidence": map[string]any{"READ_FILE": map[string]any{"ReadFile": map[string]any{"Path": "public.txt"}}}}},
		"Routes":   []any{map[string]any{"Source": map[string]any{"GuildID": "1", "ChannelID": "2"}, "ProjectID": "p"}},
	}
	data, _ := json.Marshal(config)
	path := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	var output bytes.Buffer
	if e := run(context.Background(), []string{"-config", path, "-check-config", "-discord-token-file", "missing-token"}, &output); e != nil {
		t.Fatal(e)
	}
	if output.String() != "READ_ONLY_CONFIG PASS\n" {
		t.Fatal("unexpected output")
	}
	if e := run(context.Background(), []string{"-config", path, "-check-config", "-register-commands"}, &output); e == nil {
		t.Fatal("ambiguous operation accepted")
	}
}
func TestCredentialBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discord-token")
	if e := os.WriteFile(path, bytes.Repeat([]byte("x"), 4097), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := readToken(path); e == nil {
		t.Fatal("oversized credential accepted")
	}
	if e := os.WriteFile(path, []byte("test-only"), 0600); e != nil {
		t.Fatal(e)
	}
	if got, e := readToken(path); e != nil || got != "test-only" {
		t.Fatal("credential reader failed")
	}
}
