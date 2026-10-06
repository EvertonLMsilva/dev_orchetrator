package composition

import (
	"bytes"
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Git read commands can invoke clean filters from repository configuration.
// Accept only inert configuration keys before entering the existing executors.
// config --file/--no-includes only parses local data; no tool or shell authority
// is supplied by the Planner. Its input file and output are bounded.
func safeGitConfig(ctx context.Context, workspace string) error {
	root, e := (application.WorkspaceSandbox{}).Resolve(workspace, ".")
	if e != nil {
		return application.ErrReadOnlyDenied
	}
	metadata := filepath.Join(root, ".git")
	info, e := os.Lstat(metadata)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return application.ErrReadOnlyDenied
	}
	config := filepath.Join(metadata, "config")
	info, e = os.Lstat(config)
	if e != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return application.ErrReadOnlyDenied
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(bounded, "git", "config", "--file", config, "--no-includes", "--null", "--list")
	cmd.WaitDelay = time.Second
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	output, e := cmd.Output()
	if e != nil || len(output) > 128*1024 {
		return application.ErrReadOnlyDenied
	}
	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		key, _, ok := strings.Cut(string(entry), "\n")
		if !ok || !inertGitKey(key) {
			return application.ErrReadOnlyDenied
		}
	}
	return nil
}
func inertGitKey(key string) bool {
	switch key {
	case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "core.longpaths", "user.name", "user.email":
		return true
	}
	if strings.HasPrefix(key, "remote.") {
		return strings.HasSuffix(key, ".url") || strings.HasSuffix(key, ".fetch") || strings.HasSuffix(key, ".pushurl")
	}
	if strings.HasPrefix(key, "branch.") {
		return strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge")
	}
	return false
}
func safeEvidencePath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		lower := strings.ToLower(part)
		if strings.HasPrefix(part, ".") || lower == "secrets" || strings.HasPrefix(lower, "credentials") || lower == "auth.json" || strings.HasSuffix(lower, ".pem") || strings.HasSuffix(lower, ".key") || strings.HasSuffix(lower, ".p12") || strings.HasSuffix(lower, ".pfx") {
			return false
		}
	}
	return true
}
func safeEvidenceTree(ctx context.Context, workspace, path string) error {
	resolved, e := (application.WorkspaceSandbox{}).Resolve(workspace, path)
	if e != nil {
		return application.ErrReadOnlyDenied
	}
	count := 0
	return filepath.WalkDir(resolved, func(current string, d fs.DirEntry, e error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		count++
		relative, _ := filepath.Rel(workspace, current)
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil || count > 100000 || d.Type()&fs.ModeSymlink != 0 || !safeEvidencePath(relative) {
			return application.ErrReadOnlyDenied
		}
		return nil
	})
}

type readOnlyCapabilities struct{}

func (readOnlyCapabilities) Search(ctx context.Context, w string, p domain.SearchParams) (ports.SearchResult, error) {
	if e := safeEvidenceTree(ctx, w, p.Path); e != nil {
		return ports.SearchResult{}, e
	}
	return (application.SearchExecutor{}).ExecuteContext(ctx, w, p.Query, p.Path)
}
func (readOnlyCapabilities) ReadFile(ctx context.Context, w string, p domain.ReadFileParams) (ports.ReadFileResult, error) {
	if e := safeEvidenceTree(ctx, w, p.Path); e != nil {
		return ports.ReadFileResult{}, e
	}
	return (application.ReadFileExecutor{}).ExecuteContext(ctx, w, p.Path)
}
func (readOnlyCapabilities) GitStatus(ctx context.Context, w string) (ports.GitStatusResult, error) {
	if e := safeGitConfig(ctx, w); e != nil {
		return ports.GitStatusResult{}, e
	}
	return (application.GitStatusExecutor{}).Execute(ctx, w)
}
func (readOnlyCapabilities) GitDiff(ctx context.Context, w string, p domain.GitDiffParams) (ports.GitDiffResult, error) {
	if e := safeGitConfig(ctx, w); e != nil {
		return ports.GitDiffResult{}, e
	}
	if e := safeEvidenceTree(ctx, w, p.Path); e != nil {
		return ports.GitDiffResult{}, e
	}
	return (application.GitDiffExecutor{}).Execute(ctx, w, p.Path)
}
func (readOnlyCapabilities) RunTests(context.Context, string, domain.RunTestsParams) (ports.RunTestsResult, error) {
	return ports.RunTestsResult{}, errors.New("read-only capability denied")
}
