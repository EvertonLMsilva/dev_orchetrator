package composition

import (
	"bytes"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrConfig = errors.New("invalid read-only configuration")

type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) != nil {
		return ErrConfig
	}
	v, e := time.ParseDuration(s)
	*d = Duration(v)
	return e
}

type ProjectConfig struct {
	ID        domain.ProjectID
	Name      string
	Workspace string
	Evidence  map[domain.ActionType]domain.ActionParams
}
type Config struct {
	StateDir         string
	ApplicationID    string
	RequestTimeout   Duration
	ShutdownTimeout  Duration
	MaxIntentBytes   int
	MaxEvidenceBytes int
	Projects         []ProjectConfig
	Routes           []application.ProjectRoute
	DisableDiscord   bool
	MCP              *MCPInboundConfig
}

func LoadConfig(path string) (Config, error) {
	f, e := os.Open(path)
	if e != nil {
		return Config{}, ErrConfig
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if e != nil || len(data) > 1024*1024 {
		return Config{}, ErrConfig
	}
	return DecodeConfig(data)
}
func DecodeConfig(data []byte) (Config, error) {
	if uniqueJSON(data) != nil {
		return Config{}, ErrConfig
	}
	var c Config
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || c.Validate() != nil {
		return Config{}, ErrConfig
	}
	return c, nil
}

// Reject duplicate keys at every level; decoding into structs alone does not.
func uniqueJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delimiter, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter != '{' && delimiter != '[' {
			return ErrConfig
		}
		keys := map[string]bool{}
		for d.More() {
			if delimiter == '{' {
				key, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := key.(string)
				s = strings.ToLower(s)
				if !ok || keys[s] {
					return ErrConfig
				}
				keys[s] = true
			}
			if value() != nil {
				return ErrConfig
			}
		}
		end, e := d.Token()
		if e != nil || (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
			return ErrConfig
		}
		return nil
	}
	if value() != nil {
		return ErrConfig
	}
	if _, e := d.Token(); e != io.EOF {
		return ErrConfig
	}
	return nil
}
func canonicalDir(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", ErrConfig
	}
	p, e := filepath.EvalSymlinks(path)
	if e != nil || p != filepath.Clean(path) {
		return "", ErrConfig
	}
	info, e := os.Stat(p)
	if e != nil || !info.IsDir() {
		return "", ErrConfig
	}
	return p, nil
}
func contains(root, path string) bool {
	rel, e := filepath.Rel(root, path)
	return e == nil && (rel == "." || filepath.IsLocal(rel))
}
func (c Config) Validate() error {
	if c.ApplicationID != "" && !validDiscordID(c.ApplicationID) {
		return ErrConfig
	}
	if c.RequestTimeout <= 0 || c.ShutdownTimeout <= 0 || c.MaxIntentBytes <= 0 || c.MaxIntentBytes > 8192 || c.MaxEvidenceBytes <= 0 || c.MaxEvidenceBytes > 32768 || len(c.Projects) == 0 || (!c.DisableDiscord && len(c.Routes) == 0) || (c.DisableDiscord && c.MCP == nil) {
		return ErrConfig
	}
	state, e := canonicalDir(c.StateDir)
	if e != nil {
		return e
	}
	info, e := os.Stat(state)
	if e != nil || info.Mode().Perm()&0077 != 0 {
		return ErrConfig
	}
	auth := "/var/lib/dev-orchestrator/runtime-auth"
	if contains(auth, state) || contains(state, auth) {
		return ErrConfig
	}
	projects := map[domain.ProjectID]bool{}
	roots := []string{}
	for _, p := range c.Projects {
		if _, e := domain.NewProject(p.ID, p.Name, p.Workspace); e != nil || projects[p.ID] {
			return ErrConfig
		}
		projects[p.ID] = true
		root, e := canonicalDir(p.Workspace)
		if e != nil || contains(root, state) || contains(state, root) || contains(root, auth) || contains(auth, root) {
			return ErrConfig
		}
		for _, other := range roots {
			if contains(other, root) || contains(root, other) {
				return ErrConfig
			}
		}
		roots = append(roots, root)
		if !c.DisableDiscord && len(p.Evidence) == 0 {
			return ErrConfig
		}
		for kind, params := range p.Evidence {
			if !application.NewReadOnlyActionAllowlist().Allows(kind) {
				return ErrConfig
			}
			if _, e := domain.NewAction(kind, p.ID, nil, params); e != nil {
				return ErrConfig
			}
			path := ""
			switch kind {
			case domain.ActionTypeReadFile:
				path = params.ReadFile.Path
			case domain.ActionTypeSearch:
				path = params.Search.Path
			case domain.ActionTypeGitDiff:
				path = params.GitDiff.Path
			}
			if kind != domain.ActionTypeGitStatus {
				if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || !filepath.IsLocal(path) {
					return ErrConfig
				}
				resolved, e := (application.WorkspaceSandbox{}).Resolve(root, path)
				if e != nil {
					return ErrConfig
				}
				rel, _ := filepath.Rel(root, resolved)
				if !safeEvidencePath(rel) {
					return ErrConfig
				}
			}
		}
	}
	if _, e := application.NewConfiguredProjectRoutes(c.Routes); !c.DisableDiscord && e != nil {
		return ErrConfig
	}
	for _, r := range c.Routes {
		if !projects[r.ProjectID] || !validDiscordID(r.Source.GuildID) || !validDiscordID(r.Source.ChannelID) {
			return ErrConfig
		}
	}
	if c.MCP != nil && validateMCP(c) != nil {
		return ErrConfig
	}
	return nil
}

func validDiscordID(s string) bool {
	id, e := strconv.ParseUint(s, 10, 64)
	return e == nil && id != 0 && strconv.FormatUint(id, 10) == s
}
