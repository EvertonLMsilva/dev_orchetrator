package infrastructure

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

const compareConfigLimit = 64 * 1024
const compareOutputLimit = 32 * 1024

// Test-only offline collector. It has no process, RPC, network, auth or directory
// enumeration capability. Runtime inputs are explicit offline observations,
// not a claim about the effective configuration of a running container.
type compareFS interface {
	Lstat(string) (fs.FileInfo, error)
	ReadConfig(string) ([]byte, error)
}
type compareSide struct {
	Home, Project, System, Managed string
	EnvKnown                       bool
	EnvNames                       []string
}
type compareInput struct {
	Flag          string
	Host, Runtime compareSide
}
type compareObservation struct{ presence, kind string }

var compareKeys = []string{"chatgpt_base_url", "cli_auth_credentials_store", "forced_login_method", "forced_chatgpt_workspace_id", "model_provider"}

// Only these category names are projected. Nested/custom names never leave memory.
var compareCategories = []struct{ source, label string }{
	{"network", "network"}, {"auth", "auth"}, {"model_providers", "provider_http_auth"},
}
var compareEnv = []string{"CODEX_HOME", "CODEX_ACCESS_TOKEN", "CODEX_API_KEY", "CODEX_INTERNAL_ORIGINATOR_OVERRIDE", "CODEX_CA_CERTIFICATE", "SSL_CERT_FILE", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"}

func compareType(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case int64:
		return "integer"
	case float64:
		return "float"
	case []any:
		return "array"
	case map[string]any:
		return "table"
	default:
		return "other"
	}
}
func compareFile(f compareFS, root, name string) (compareObservation, map[string]any) {
	if root == "" {
		return compareObservation{"unknown", "unknown"}, nil
	}
	p := filepath.Join(root, name)
	i, err := f.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return compareObservation{"no", "absent"}, nil
	}
	if err != nil || i == nil {
		return compareObservation{"unknown", "unknown"}, nil
	}
	// auth.json is metadata-only, including symlinks/nonregular entries.
	if name == "auth.json" {
		return compareObservation{"yes", "presence_only"}, nil
	}
	if !i.Mode().IsRegular() || i.Size() < 0 || i.Size() > compareConfigLimit {
		return compareObservation{"yes", "unavailable"}, nil
	}
	b, err := f.ReadConfig(p)
	if err != nil || len(b) > compareConfigLimit {
		clear(b)
		return compareObservation{"yes", "unavailable"}, nil
	}
	defer clear(b)
	var config map[string]any
	if toml.Unmarshal(b, &config) != nil {
		return compareObservation{"yes", "unavailable"}, nil
	}
	return compareObservation{"yes", "table"}, config
}
func compareKey(file compareObservation, m map[string]any, key string) compareObservation {
	if file.presence == "no" {
		return compareObservation{"no", "absent"}
	}
	if m == nil {
		return compareObservation{"unknown", "unknown"}
	}
	v, ok := m[key]
	if !ok {
		return compareObservation{"no", "absent"}
	}
	return compareObservation{"yes", compareType(v)}
}
func compareDifference(a, b compareObservation) string {
	if a.presence == "unknown" || b.presence == "unknown" || a.kind == "unavailable" || b.kind == "unavailable" {
		return "unknown"
	}
	if a != b {
		return "yes"
	}
	return "no"
}
func runHostCompare(in compareInput, f compareFS) (string, error) {
	if in.Flag != "1" {
		return "SKIP\n", nil
	}
	if in.Host.Home == "" || !filepath.IsAbs(in.Host.Home) || f == nil {
		return "", errors.New("explicit absolute host home required")
	}
	for _, s := range []compareSide{in.Host, in.Runtime} {
		for _, p := range []string{s.Home, s.Project, s.System, s.Managed} {
			if p != "" && (!filepath.IsAbs(p) || len(p) > 4096) {
				return "", errors.New("explicit absolute layer path required")
			}
		}
		if len(s.EnvNames) > 64 {
			return "", errors.New("environment presence input limit exceeded")
		}
		for _, n := range s.EnvNames {
			if len(n) > 128 {
				return "", errors.New("environment name limit exceeded")
			}
		}
	}
	var out strings.Builder
	out.WriteString("codex_started=no\nrpc_executed=no\n")
	for _, n := range []string{"auth_json_opened", "auth_value_logged", "config_value_logged", "env_value_logged", "url_logged", "workspace_id_logged", "account_id_logged", "token_logged", "hash_generated", "unknown_names_logged"} {
		fmt.Fprintf(&out, "%s=no\n", n)
	}
	row := func(layer, category, name string, a, b compareObservation) {
		fmt.Fprintf(&out, "side=HOST layer=%s category=%s %s present=%s type=%s\n", layer, category, name, a.presence, a.kind)
		fmt.Fprintf(&out, "side=RUNTIME layer=%s category=%s %s present=%s type=%s\n", layer, category, name, b.presence, b.kind)
		fmt.Fprintf(&out, "layer=%s category=%s %s difference=%s\n", layer, category, name, compareDifference(a, b))
	}
	layers := []struct {
		name, h, r string
		files      []string
	}{
		{"HOME", in.Host.Home, in.Runtime.Home, []string{"auth.json", "config.toml", "managed_config.toml"}},
		{"PROJECT/CWD", in.Host.Project, in.Runtime.Project, []string{"config.toml"}},
		{"SYSTEM", in.Host.System, in.Runtime.System, []string{"config.toml"}},
		{"MANAGED", in.Host.Managed, in.Runtime.Managed, []string{"managed_config.toml"}},
	}
	for _, l := range layers {
		for _, name := range l.files {
			a, am := compareFile(f, l.h, name)
			b, bm := compareFile(f, l.r, name)
			row(l.name, "file", "name="+name, a, b)
			if name == "auth.json" {
				continue
			}
			for _, k := range compareKeys {
				row(l.name, name, "key="+k, compareKey(a, am, k), compareKey(b, bm, k))
			}
			for _, k := range compareCategories {
				row(l.name, k.label, "name="+name, compareKey(a, am, k.source), compareKey(b, bm, k.source))
			}
		}
	}
	env := func(s compareSide, n string) compareObservation {
		if !s.EnvKnown {
			return compareObservation{"unknown", "unknown"}
		}
		for _, k := range s.EnvNames {
			if k == n {
				return compareObservation{"yes", "presence_only"}
			}
		}
		return compareObservation{"no", "absent"}
	}
	for _, n := range compareEnv {
		row("ENVIRONMENT", "environment", "name="+n, env(in.Host, n), env(in.Runtime, n))
	}
	row("CLI/SESSION", "configuration", "name=overrides", compareObservation{"unknown", "unknown"}, compareObservation{"unknown", "unknown"})
	if out.Len() > compareOutputLimit {
		return "", errors.New("comparison output limit exceeded")
	}
	return out.String(), nil
}

type compareOSFS struct{}

// Refuse symlink/reparse-point traversal, without listing any directory.
func (compareOSFS) Lstat(p string) (fs.FileInfo, error) {
	for parent := filepath.Dir(p); ; parent = filepath.Dir(parent) {
		i, err := os.Lstat(parent)
		if err != nil {
			return nil, err
		}
		if !i.IsDir() || i.Mode()&fs.ModeSymlink != 0 {
			return nil, errors.New("layer unavailable")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return os.Lstat(p)
}
func (f compareOSFS) ReadConfig(p string) ([]byte, error) {
	if filepath.Base(p) != "config.toml" && filepath.Base(p) != "managed_config.toml" {
		return nil, errors.New("config name rejected")
	}
	i, err := f.Lstat(p)
	if err != nil || !i.Mode().IsRegular() {
		return nil, errors.New("config unavailable")
	}
	file, err := os.Open(p)
	if err != nil {
		return nil, errors.New("config unavailable")
	}
	defer file.Close()
	j, err := file.Stat()
	if err != nil || !j.Mode().IsRegular() || !os.SameFile(i, j) {
		return nil, errors.New("config unavailable")
	}
	b, err := io.ReadAll(io.LimitReader(file, compareConfigLimit+1))
	if err != nil || len(b) > compareConfigLimit {
		clear(b)
		return nil, errors.New("config unavailable")
	}
	return b, nil
}

// Run manually with DEV_ORCHESTRATOR_CODEX_HOST_COMPARE=1 and explicit absolute
// DEV_ORCHESTRATOR_CODEX_COMPARE_HOST_HOME. Optional RUNTIME_HOME must refer to
// an already available offline directory; this harness never obtains/copies it.
// Optional *_PROJECT_CONFIG_DIR points to the exact .codex directory, not CWD;
// *_SYSTEM_CONFIG_DIR and *_MANAGED_CONFIG_DIR also identify exact directories.
// *_ means DEV_ORCHESTRATOR_CODEX_COMPARE_HOST or ..._RUNTIME.
// DEV_ORCHESTRATOR_CODEX_COMPARE_RUNTIME_ENV_NAMES is a comma-separated list of
// observed names only (empty explicitly means none); unset means unknown.
// Missing layer paths and CLI/session overrides remain unknown. No system,
// managed/cloud, parent-project, profile or runtime discovery occurs.
// Command: go test ./internal/infrastructure -run '^TestCodexHostCompareOfflineOptIn$' -count=1 -v -timeout 30s
// Differences concern presence/types only, never effective configuration equality.
func TestCodexHostCompareOfflineOptIn(t *testing.T) {
	if os.Getenv("DEV_ORCHESTRATOR_CODEX_HOST_COMPARE") != "1" {
		t.Skip("SKIP")
	}
	// Paths are supplied explicitly; no home, project or system discovery.
	load := func(prefix string) compareSide {
		return compareSide{Home: os.Getenv(prefix + "_HOME"), Project: os.Getenv(prefix + "_PROJECT_CONFIG_DIR"), System: os.Getenv(prefix + "_SYSTEM_CONFIG_DIR"), Managed: os.Getenv(prefix + "_MANAGED_CONFIG_DIR")}
	}
	h := load("DEV_ORCHESTRATOR_CODEX_COMPARE_HOST")
	r := load("DEV_ORCHESTRATOR_CODEX_COMPARE_RUNTIME")
	h.EnvKnown = true
	for _, n := range compareEnv {
		if _, ok := os.LookupEnv(n); ok {
			h.EnvNames = append(h.EnvNames, n)
		}
	}
	if names, ok := os.LookupEnv("DEV_ORCHESTRATOR_CODEX_COMPARE_RUNTIME_ENV_NAMES"); ok {
		if len(names) > 8192 {
			t.Fatal("environment presence input limit exceeded")
		}
		r.EnvKnown = true
		if names != "" {
			r.EnvNames = strings.Split(names, ",")
		}
	}
	out, err := runHostCompare(compareInput{Flag: "1", Host: h, Runtime: r}, compareOSFS{})
	if err != nil {
		t.Fatal(err.Error())
	}
	t.Log("\n" + out)
}
