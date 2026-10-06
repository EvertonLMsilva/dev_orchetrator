package infrastructure

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testManagedAuth = `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"TEST_SECRET_DO_NOT_LEAK","access_token":"TEST_SECRET_DO_NOT_LEAK","refresh_token":"TEST_SECRET_DO_NOT_LEAK","account_id":"account"},"last_refresh":"2026-10-05T12:00:00Z"}`

func TestAuthorizedCodexHomeAvailable(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(testManagedAuth), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewAuthorizedCodexHome(home, true)
	if err != nil {
		t.Fatal(err)
	}
	var seam chatGPTAuthSource = source
	material, err := seam.obtain(context.Background())
	if err != nil || !bytes.Equal(material, []byte(testManagedAuth)) {
		t.Fatal("authorized material not delivered")
	}
	clear(material)
	// The source does not cache secrets; ownership of each read transfers to the caller.
	again, err := seam.obtain(context.Background())
	defer clear(again)
	if err != nil || !bytes.Equal(again, []byte(testManagedAuth)) {
		t.Fatal("source retained caller's buffer")
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, source), "TEST_SECRET_DO_NOT_LEAK") || strings.Contains(fmt.Sprintf(format, source), home) {
			t.Fatal("source formatting leaks sensitive data")
		}
	}
}

func TestAuthorizedCodexHomeConfigurationFailsClosed(t *testing.T) {
	defaultHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(defaultHome, "auth.json"), []byte(testManagedAuth), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", defaultHome)
	for _, tc := range []struct {
		name, path string
		authorized bool
	}{
		{"missing", "", true}, {"empty", "  ", true}, {"implicit", "~/.codex", true},
		{"relative", ".codex", true}, {"invalid", "\x00TEST_SECRET_DO_NOT_LEAK", true},
		{"unauthorized", t.TempDir(), false}, {"root", string(filepath.Separator), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, err := NewAuthorizedCodexHome(tc.path, tc.authorized)
			if source != nil || err == nil || strings.Contains(fmt.Sprintf("%+v", err), "TEST_SECRET_DO_NOT_LEAK") {
				t.Fatal("configuration did not fail closed")
			}
		})
	}
	var missing *AuthorizedCodexHome
	material, err := missing.obtain(context.Background())
	if err == nil || material != nil {
		t.Fatal("nil source did not fail closed")
	}
	material, err = (&AuthorizedCodexHome{}).obtain(context.Background())
	if err == nil || material != nil {
		t.Fatal("zero source did not fail closed")
	}
}

func TestAuthorizedCodexHomeMaterialFailsClosed(t *testing.T) {
	for _, tc := range []struct{ name, content string }{
		{"empty", ""}, {"malformed", "TEST_SECRET_DO_NOT_LEAK"}, {"missing_tokens", `{}`},
		{"api_key", `{"OPENAI_API_KEY":"TEST_SECRET_DO_NOT_LEAK"}`},
		{"mixed_api_key", strings.Replace(testManagedAuth, `"OPENAI_API_KEY":null`, `"OPENAI_API_KEY":"TEST_SECRET_DO_NOT_LEAK"`, 1)},
		{"external_tokens", strings.Replace(testManagedAuth, `"chatgpt"`, `"chatgptAuthTokens"`, 1)},
		{"missing_refresh", strings.Replace(testManagedAuth, `"refresh_token":"TEST_SECRET_DO_NOT_LEAK"`, `"refresh_token":""`, 1)},
		{"missing_id", strings.Replace(testManagedAuth, `"id_token":"TEST_SECRET_DO_NOT_LEAK"`, `"id_token":null`, 1)},
		{"missing_access", strings.Replace(testManagedAuth, `"access_token":"TEST_SECRET_DO_NOT_LEAK"`, `"access_token":" "`, 1)},
		{"duplicate_mode", strings.Replace(testManagedAuth, `"auth_mode":"chatgpt"`, `"auth_mode":"apikey","auth_mode":"chatgpt"`, 1)},
		{"wrong_field_case", strings.Replace(testManagedAuth, `"id_token"`, `"ID_TOKEN"`, 1)},
		{"unknown_material", strings.Replace(testManagedAuth, `"auth_mode":"chatgpt"`, `"secret":"TEST_SECRET_DO_NOT_LEAK","auth_mode":"chatgpt"`, 1)},
		{"bad_timestamp", strings.Replace(testManagedAuth, "2026-10-05T12:00:00Z", "TEST_SECRET_DO_NOT_LEAK", 1)},
		{"trailing", testManagedAuth + `{}`}, {"oversized", strings.Repeat("X", 1024*1024+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			source, err := NewAuthorizedCodexHome(home, true)
			if err != nil {
				t.Fatal(err)
			}
			material, err := source.obtain(context.Background())
			if material != nil || err == nil || strings.Contains(fmt.Sprintf("%+v", err), "TEST_SECRET_DO_NOT_LEAK") {
				t.Fatal("material did not fail closed or error leaked")
			}
		})
	}
}

func TestAuthorizedCodexHomeLegacyManagedMaterial(t *testing.T) {
	home := t.TempDir()
	legacy := strings.Replace(testManagedAuth, `"auth_mode":"chatgpt",`, "", 1)
	legacy = strings.Replace(legacy, `,"account_id":"account"`, "", 1)
	legacy = strings.Replace(legacy, `,"last_refresh":"2026-10-05T12:00:00Z"`, "", 1)
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := NewAuthorizedCodexHome(home, true)
	if err != nil {
		t.Fatal(err)
	}
	material, err := source.obtain(context.Background())
	defer clear(material)
	if err != nil || !bytes.Equal(material, []byte(legacy)) {
		t.Fatal("legacy managed material rejected")
	}
}

func TestAuthorizedCodexHomeInvalidDirectory(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte(testManagedAuth), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{file, filepath.Join(base, "TEST_SECRET_DO_NOT_LEAK")} {
		source, err := NewAuthorizedCodexHome(path, true)
		if err != nil {
			t.Fatal(err)
		}
		material, err := source.obtain(context.Background())
		if err == nil || material != nil || strings.Contains(fmt.Sprintf("%+v", err), "TEST_SECRET_DO_NOT_LEAK") {
			t.Fatal("invalid directory accepted or error leaked")
		}
	}
}

func TestAuthorizedCodexHomeUnavailableAndCancelled(t *testing.T) {
	home := t.TempDir()
	source, err := NewAuthorizedCodexHome(home, true)
	if err != nil {
		t.Fatal(err)
	}
	material, err := source.obtain(context.Background())
	if err == nil || material != nil {
		t.Fatal("missing auth accepted")
	}
	if err := os.Mkdir(filepath.Join(home, "auth.json"), 0700); err != nil {
		t.Fatal(err)
	}
	material, err = source.obtain(context.Background())
	if err == nil || material != nil {
		t.Fatal("directory auth accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	material, err = source.obtain(ctx)
	if err == nil || material != nil {
		t.Fatal("cancelled read accepted")
	}
}

func TestAuthorizedCodexHomeRejectsEscapingSymlink(t *testing.T) {
	home, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "auth.json"), []byte(testManagedAuth), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "auth.json"), filepath.Join(home, "auth.json")); err != nil {
		t.Skip("symlinks unavailable")
	}
	source, err := NewAuthorizedCodexHome(home, true)
	if err != nil {
		t.Fatal(err)
	}
	material, err := source.obtain(context.Background())
	if err == nil || material != nil {
		t.Fatal("credential outside authorized home accepted")
	}
}
