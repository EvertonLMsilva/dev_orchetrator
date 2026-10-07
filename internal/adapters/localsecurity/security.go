// Package localsecurity provides administrator-controlled local MCP security
// infrastructure. Paths and files are configuration, never request arguments.
package localsecurity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"dev-orchestrator/internal/ports"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var ErrSecurity = errors.New("local security configuration or operation unavailable")

const maxConfigBytes = 65536

type credential struct {
	PrincipalID string `json:"principalId"`
	SHA256      string `json:"sha256"`
}
type authenticationConfig struct {
	Credentials []credential `json:"credentials"`
}
type grantEntry struct {
	PrincipalID string `json:"principalId"`
	Operation   string `json:"operation"`
	ProjectID   string `json:"projectId"`
}
type grantsConfig struct {
	Grants []grantEntry `json:"grants"`
}
type Authentication struct{ path string }
type Grants struct{ path string }

func NewAuthentication(path string) *Authentication { return &Authentication{path: path} }
func NewGrants(path string) *Grants                 { return &Grants{path: path} }

func validID(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && utf8.ValidString(s) && !strings.ContainsAny(s, "*\x00\r\n")
}
func validOperation(s string) bool {
	switch s {
	case "project.status", "project.tasks", "git.status", "execution.status":
		return true
	}
	return false
}

func (a *Authentication) Authenticate(ctx context.Context, e ports.AuthenticationEvidence) (ports.Principal, error) {
	if a == nil || ctx.Err() != nil || len(e.Material) == 0 || len(e.Material) > 4096 {
		return ports.Principal{}, ErrSecurity
	}
	var config authenticationConfig
	if load(a.path, &config) != nil || len(config.Credentials) == 0 {
		return ports.Principal{}, ErrSecurity
	}
	digests := make([][]byte, len(config.Credentials))
	seen := map[string]bool{}
	for i, c := range config.Credentials {
		digest, err := hex.DecodeString(c.SHA256)
		if err != nil || len(digest) != sha256.Size || !validID(c.PrincipalID) || seen[strings.ToLower(c.SHA256)] {
			return ports.Principal{}, ErrSecurity
		}
		seen[strings.ToLower(c.SHA256)] = true
		digests[i] = digest
	}
	sum := sha256.Sum256(e.Material)
	principal := ports.Principal{}
	// Compare all fixed-size digests, without early return on a match.
	for i, digest := range digests {
		if subtle.ConstantTimeCompare(sum[:], digest) == 1 {
			principal.ID = config.Credentials[i].PrincipalID
		}
	}
	if principal.ID == "" || ctx.Err() != nil {
		return ports.Principal{}, ErrSecurity
	}
	return principal, nil
}

func (g *Grants) FindExact(ctx context.Context, wanted ports.Grant) (ports.Grant, bool, error) {
	if g == nil || ctx.Err() != nil || !validID(wanted.PrincipalID) || !validID(wanted.ProjectID) || !validOperation(wanted.Operation) {
		return ports.Grant{}, false, ErrSecurity
	}
	var config grantsConfig
	if load(g.path, &config) != nil || config.Grants == nil {
		return ports.Grant{}, false, ErrSecurity
	}
	seen := map[ports.Grant]bool{}
	found := false
	for _, entry := range config.Grants {
		grant := ports.Grant{PrincipalID: entry.PrincipalID, Operation: entry.Operation, ProjectID: entry.ProjectID}
		if !validID(grant.PrincipalID) || !validID(grant.ProjectID) || !validOperation(grant.Operation) || seen[grant] {
			return ports.Grant{}, false, ErrSecurity
		}
		seen[grant] = true
		if grant == wanted {
			found = true
		}
	}
	if ctx.Err() != nil {
		return ports.Grant{}, false, ErrSecurity
	}
	if found {
		return wanted, true, nil
	}
	return ports.Grant{}, false, nil
}

// Validate checks startup configuration without caching it. Runtime reads again.
func Validate(authenticationPath, grantsPath string) error {
	// A nonmatching synthetic probe still exercises whole-file validation.
	a := NewAuthentication(authenticationPath)
	var config authenticationConfig
	if load(a.path, &config) != nil || len(config.Credentials) == 0 {
		return ErrSecurity
	}
	seen := map[string]bool{}
	for _, c := range config.Credentials {
		d, e := hex.DecodeString(c.SHA256)
		key := strings.ToLower(c.SHA256)
		if e != nil || len(d) != sha256.Size || !validID(c.PrincipalID) || seen[key] {
			return ErrSecurity
		}
		seen[key] = true
	}
	_, _, err := NewGrants(grantsPath).FindExact(context.Background(), ports.Grant{PrincipalID: "startup-validation", Operation: "project.status", ProjectID: "startup-validation"})
	return err
}

func trustedPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	return err == nil && parent == filepath.Dir(path)
}
func load(path string, dst any) error {
	if !trustedPath(path) {
		return ErrSecurity
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ErrSecurity
	}
	f, err := os.Open(path)
	if err != nil {
		return ErrSecurity
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return ErrSecurity
	}
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes || uniqueJSON(data) != nil {
		return ErrSecurity
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(dst) != nil {
		return ErrSecurity
	}
	return nil
}

// Strict, bounded JSON: no duplicate keys (including case aliases), deep nesting,
// trailing values, or unknown schema fields. Whole-file invalidity denies access.
func uniqueJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func(int) error
	value = func(depth int) error {
		if depth > 8 {
			return ErrSecurity
		}
		token, err := decoder.Token()
		if err != nil {
			return ErrSecurity
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter != '{' && delimiter != '[' {
			return ErrSecurity
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delimiter == '{' {
				key, err := decoder.Token()
				s, ok := key.(string)
				s = strings.ToLower(s)
				if err != nil || !ok || seen[s] {
					return ErrSecurity
				}
				seen[s] = true
			}
			if value(depth+1) != nil {
				return ErrSecurity
			}
		}
		end, err := decoder.Token()
		if err != nil || (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
			return ErrSecurity
		}
		return nil
	}
	if value(0) != nil {
		return ErrSecurity
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrSecurity
	}
	return nil
}
