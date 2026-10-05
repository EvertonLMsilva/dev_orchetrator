package infrastructure

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type compareFakeFS struct {
	files        map[string]string
	stats, opens []string
}
type compareInfo struct{ size int64 }

func (i compareInfo) Name() string       { return "known" }
func (i compareInfo) Size() int64        { return i.size }
func (i compareInfo) Mode() fs.FileMode  { return 0600 }
func (i compareInfo) ModTime() time.Time { return time.Time{} }
func (i compareInfo) IsDir() bool        { return false }
func (i compareInfo) Sys() any           { return nil }
func (f *compareFakeFS) Lstat(p string) (fs.FileInfo, error) {
	f.stats = append(f.stats, p)
	s, ok := f.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return compareInfo{int64(len(s))}, nil
}
func (f *compareFakeFS) ReadConfig(p string) ([]byte, error) {
	f.opens = append(f.opens, p)
	if filepath.Base(p) == "auth.json" {
		panic("auth opened")
	}
	s, ok := f.files[p]
	if !ok {
		return nil, errors.New("SECRET")
	}
	return []byte(s), nil
}
func compareFixture() (compareInput, *compareFakeFS) {
	h, _ := filepath.Abs("synthetic-host")
	r, _ := filepath.Abs("synthetic-runtime")
	return compareInput{Flag: "1", Host: compareSide{Home: h}, Runtime: compareSide{Home: r}}, &compareFakeFS{files: map[string]string{}}
}
func TestHostCompareOptInAndExplicitPath(t *testing.T) {
	for _, flag := range []string{"", "0", "true"} {
		in, f := compareFixture()
		in.Flag = flag
		in.Host.Home = ""
		out, err := runHostCompare(in, f)
		if err != nil || out != "SKIP\n" || len(f.stats)+len(f.opens) != 0 {
			t.Fatal("opt-in bypass")
		}
	}
	for _, path := range []string{"", "relative"} {
		in, f := compareFixture()
		in.Host.Home = path
		out, err := runHostCompare(in, f)
		if err == nil || out != "" || len(f.stats)+len(f.opens) != 0 || strings.Contains(err.Error(), path) && path != "" {
			t.Fatal("explicit path not enforced safely")
		}
	}
}
func TestHostCompareProjectionAndPresenceOnly(t *testing.T) {
	in, f := compareFixture()
	f.files[filepath.Join(in.Host.Home, "auth.json")] = "TOKEN_SECRET"
	f.files[filepath.Join(in.Host.Home, "config.toml")] = `chatgpt_base_url="https://SECRET"
cli_auth_credentials_store="SECRET"
forced_login_method="SECRET"
forced_chatgpt_workspace_id=["SECRET"]
model_provider="CUSTOM_SECRET"
unknown_SECRET="SECRET"
[profiles.PRIVATE_SECRET]
chatgpt_base_url="SECRET"
[model_providers.CUSTOM_SECRET]
base_url="SECRET"
[features]
PRIVATE_SECRET=true
[network]
proxy_url="SECRET"
`
	in.Host.EnvKnown = true
	in.Runtime.EnvKnown = true
	in.Host.EnvNames = []string{"CODEX_HOME", "HTTPS_PROXY", "PRIVATE_SECRET", "HTTP_PROXY=TOKEN_SECRET", "https_proxy"}
	out, err := runHostCompare(in, f)
	if err != nil {
		t.Fatal("projection failed")
	}
	for _, expected := range []string{"key=chatgpt_base_url present=yes type=string", "key=forced_chatgpt_workspace_id present=yes type=array", "category=network name=config.toml present=yes type=table", "name=HTTPS_PROXY present=yes", "difference=yes", "auth_json_opened=no"} {
		if !strings.Contains(out, expected) {
			t.Fatal("missing safe projection: " + expected)
		}
	}
	for _, forbidden := range []string{"SECRET", "https://", in.Host.Home, in.Runtime.Home, "profiles", "model_providers", "proxy_url"} {
		if strings.Contains(out, forbidden) {
			t.Fatal("unsafe output")
		}
	}
	for _, p := range f.opens {
		if filepath.Base(p) != "config.toml" && filepath.Base(p) != "managed_config.toml" {
			t.Fatal("non-config opened")
		}
	}
	for _, p := range f.stats {
		switch filepath.Base(p) {
		case "auth.json", "config.toml", "managed_config.toml":
		default:
			t.Fatal("arbitrary path inspected")
		}
	}
	if len(f.opens) != 1 || len(f.stats) != 6 {
		t.Fatal("unexpected filesystem access")
	}
}
func TestHostCompareBoundedErrorsAndLayers(t *testing.T) {
	in, f := compareFixture()
	in.Host.Project = in.Host.Home
	in.Host.System = in.Host.Home
	in.Host.Managed = in.Host.Home
	for _, content := range []string{"chatgpt_base_url = SECRET invalid", strings.Repeat("S", compareConfigLimit+1)} {
		f.files[filepath.Join(in.Host.Home, "config.toml")] = content
		out, err := runHostCompare(in, f)
		if err != nil || len(out) > compareOutputLimit || strings.Contains(out, "SECRET") || strings.Contains(out, strings.Repeat("S", 100)) {
			t.Fatal("unbounded or raw error")
		}
		for _, layer := range []string{"HOME", "ENVIRONMENT", "PROJECT/CWD", "SYSTEM", "MANAGED", "CLI/SESSION"} {
			if !strings.Contains(out, "layer="+layer) {
				t.Fatal("missing layer")
			}
		}
		if !strings.Contains(out, "difference=unknown") {
			t.Fatal("unavailable inferred equal")
		}
	}
}
func TestHostCompareNoCodexExecution(t *testing.T) {
	// The collector's only capabilities are metadata and bounded TOML reads.
	// No process, RPC, Docker, auth-source, network or directory-listing dependency exists.
	in, f := compareFixture()
	out, err := runHostCompare(in, f)
	if err != nil || !strings.Contains(out, "codex_started=no\nrpc_executed=no") {
		t.Fatal("execution scope changed")
	}
	// Enforce the harness capability boundary, rather than trusting its report.
	file, err := parser.ParseFile(token.NewFileSet(), "codex_host_compare_harness_test.go", nil, 0)
	if err != nil {
		t.Fatal("harness source unavailable")
	}
	allowed := map[string]bool{"\"errors\"": true, "\"fmt\"": true, "\"io\"": true, "\"io/fs\"": true, "\"os\"": true, "\"path/filepath\"": true, "\"strings\"": true, "\"testing\"": true, "\"github.com/pelletier/go-toml/v2\"": true}
	for _, imp := range file.Imports {
		if !allowed[imp.Path.Value] {
			t.Fatal("unexpected harness capability")
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		if s, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := s.X.(*ast.Ident); ok && id.Name == "os" {
				switch s.Sel.Name {
				case "Lstat", "Open", "SameFile", "Getenv", "LookupEnv":
				default:
					t.Fatal("unexpected OS capability")
				}
			}
		}
		return true
	})
}

func TestHostCompareRealBoundedConfigReader(t *testing.T) {
	root := t.TempDir()
	f := compareOSFS{}
	if err := os.WriteFile(filepath.Join(root, "auth.json"), []byte("TOKEN_SECRET"), 0600); err != nil {
		t.Fatal("fixture failed")
	}
	if _, err := f.ReadConfig(filepath.Join(root, "auth.json")); err == nil {
		t.Fatal("auth can be opened")
	}
	config := filepath.Join(root, "config.toml")
	if err := os.WriteFile(config, []byte("chatgpt_base_url=\"SECRET\"\n"), 0600); err != nil {
		t.Fatal("fixture failed")
	}
	observed, m := compareFile(f, root, "config.toml")
	if observed.kind != "table" || compareKey(observed, m, "chatgpt_base_url").kind != "string" {
		t.Fatal("real TOML projection failed")
	}
	if err := os.WriteFile(config, []byte(strings.Repeat("S", compareConfigLimit+1)), 0600); err != nil {
		t.Fatal("fixture failed")
	}
	if _, err := f.ReadConfig(config); err == nil {
		t.Fatal("input limit bypass")
	}
	if err := os.Mkdir(filepath.Join(root, "managed_config.toml"), 0700); err != nil {
		t.Fatal("fixture failed")
	}
	observed, _ = compareFile(f, root, "managed_config.toml")
	if observed.kind != "unavailable" {
		t.Fatal("nonregular config read")
	}
}

func TestHostCompareRejectsConfigSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "auth.json")
	if err := os.WriteFile(target, []byte("TOKEN_SECRET"), 0600); err != nil {
		t.Fatal("fixture failed")
	}
	if err := os.Symlink(target, filepath.Join(root, "config.toml")); err != nil {
		t.Skip("symlink fixture unavailable")
	}
	f := compareOSFS{}
	observed, m := compareFile(f, root, "config.toml")
	if observed.kind != "unavailable" || m != nil {
		t.Fatal("config symlink followed")
	}
	if _, err := f.ReadConfig(filepath.Join(root, "config.toml")); err == nil {
		t.Fatal("symlink can be opened")
	}
}

func TestHostCompareUnknownAndStructuralDifferences(t *testing.T) {
	in, f := compareFixture()
	f.files[filepath.Join(in.Host.Home, "config.toml")] = "model_provider=\"SECRET_A\"\n"
	f.files[filepath.Join(in.Runtime.Home, "config.toml")] = "model_provider=\"SECRET_B\"\n"
	out, err := runHostCompare(in, f)
	if err != nil || !strings.Contains(out, "key=model_provider difference=no") || strings.Contains(out, "SECRET") {
		t.Fatal("compared values instead of structure")
	}
	in.Runtime.Home = ""
	out, err = runHostCompare(in, f)
	if err != nil || !strings.Contains(out, "key=model_provider difference=unknown") {
		t.Fatal("unobserved runtime inferred absent")
	}
	in.Runtime.Home = "relative"
	before := len(f.stats)
	if _, err = runHostCompare(in, f); err == nil || len(f.stats) != before {
		t.Fatal("invalid runtime path accessed")
	}
}
