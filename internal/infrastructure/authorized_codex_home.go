package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCodexAuthBytes = 1024 * 1024

// AuthorizedCodexHome reads only auth.json from an explicitly configured home.
// It does not consult environment variables, user homes, keyrings or browsers.
// Tokens remain Codex-managed. P6.2 owns private tmpfs delivery and login lifecycle.
type AuthorizedCodexHome struct{ path string }

// NewAuthorizedCodexHome requires a trusted infrastructure caller's explicit
// authorization for this exact directory. The flag records that decision; it
// does not infer consent from filesystem access or perform identity verification.
// An explicitly authorized default-named directory is allowed; implicit defaults
// and home expansion are never used. Symlink aliases are rejected.
func NewAuthorizedCodexHome(configuredPath string, explicitlyAuthorized bool) (*AuthorizedCodexHome, error) {
	if !explicitlyAuthorized || strings.TrimSpace(configuredPath) == "" ||
		strings.ContainsRune(configuredPath, '\x00') || !filepath.IsAbs(configuredPath) {
		return nil, errors.New("codex home configuration or authorization invalid")
	}
	clean := filepath.Clean(configuredPath)
	if filepath.Dir(clean) == clean {
		return nil, errors.New("codex home configuration invalid")
	}
	return &AuthorizedCodexHome{path: clean}, nil
}

// Format redacts even the configured path, including Go-syntax formatting.
func (*AuthorizedCodexHome) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "AuthorizedCodexHome{redacted}")
}

// obtain transfers a fresh buffer only to the infrastructure seam. The caller
// must clear it after use. No material or parser/filesystem error is retained.
func (s *AuthorizedCodexHome) obtain(ctx context.Context) ([]byte, error) {
	failure := errors.New("codex managed chatgpt authentication unavailable")
	if s == nil || s.path == "" || ctx == nil || ctx.Err() != nil {
		return nil, failure
	}
	resolved, err := filepath.EvalSymlinks(s.path)
	if err != nil || resolved != s.path {
		return nil, failure
	}
	root, err := os.OpenRoot(s.path)
	if err != nil {
		return nil, failure
	}
	defer root.Close()
	info, err := root.Lstat("auth.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxCodexAuthBytes {
		return nil, failure
	}
	// OpenRoot confines the read even if a path changes between check and open.
	file, err := root.Open("auth.json")
	if err != nil {
		return nil, failure
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, failure
	}
	material, err := io.ReadAll(io.LimitReader(file, maxCodexAuthBytes+1))
	if err != nil || len(material) > maxCodexAuthBytes || ctx.Err() != nil || !validManagedCodexAuth(material) {
		clear(material)
		return nil, failure
	}
	return material, nil
}

// Minimal persisted ChatGPT structure pinned to rust-v0.159.2:
// codex-rs/login/src/auth/storage.rs (AuthDotJson) and token_data.rs (TokenData).
// This is structural validation, not token verification or an entitlement check.
// Legacy files may omit auth_mode; alternate credential modes fail closed.
func validManagedCodexAuth(material []byte) bool {
	var auth struct {
		Mode   *string `json:"auth_mode"`
		APIKey *string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			ID      string  `json:"id_token"`
			Access  string  `json:"access_token"`
			Refresh string  `json:"refresh_token"`
			Account *string `json:"account_id"`
		} `json:"tokens"`
		LastRefresh *time.Time `json:"last_refresh"`
	}
	if !uniqueAuthJSON(material) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(material))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&auth) != nil || decoder.Decode(new(any)) != io.EOF ||
		(auth.Mode != nil && *auth.Mode != "chatgpt") || auth.APIKey != nil || auth.Tokens == nil {
		return false
	}
	tokens := auth.Tokens
	return strings.TrimSpace(tokens.ID) != "" && strings.TrimSpace(tokens.Access) != "" &&
		strings.TrimSpace(tokens.Refresh) != "" && (tokens.Account == nil || strings.TrimSpace(*tokens.Account) != "")
}

// ValidRuntimeCodexAuth reuses the managed-file schema but requires the explicit
// runtime-owned ChatGPT mode. It never returns parser errors or sensitive data.
// AuthorizedCodexHome retains its existing legacy-file compatibility.
func ValidRuntimeCodexAuth(material []byte) bool {
	if len(material) == 0 || len(material) > maxCodexAuthBytes || !validManagedCodexAuth(material) {
		return false
	}
	var mode struct {
		Mode string `json:"auth_mode"`
	}
	return json.Unmarshal(material, &mode) == nil && mode.Mode == "chatgpt"
}

// Reject duplicate keys rather than letting Go and the provider disagree about
// the selected auth mode or material. Bound nesting before decoding credentials.
func uniqueAuthJSON(material []byte) bool {
	d := json.NewDecoder(bytes.NewReader(material))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 8 {
			return false
		}
		token, err := d.Token()
		if err != nil {
			return false
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return true
		}
		if delim != '{' && delim != '[' {
			return false
		}
		keys := make(map[string]bool)
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				name, ok := key.(string)
				if err != nil || !ok || keys[name] {
					return false
				}
				// encoding/json matches struct fields case-insensitively; the
				// pinned Rust schema requires these exact names.
				switch name {
				case "auth_mode", "OPENAI_API_KEY", "tokens", "last_refresh",
					"id_token", "access_token", "refresh_token", "account_id":
				default:
					return false
				}
				keys[name] = true
			}
			if !value(depth + 1) {
				return false
			}
		}
		end, err := d.Token()
		return err == nil && ((delim == '{' && end == json.Delim('}')) || (delim == '[' && end == json.Delim(']')))
	}
	if !value(0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
