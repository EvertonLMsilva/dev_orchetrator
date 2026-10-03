package application

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrGitRepository  = errors.New("invalid workspace Git repository")
	ErrGitUnavailable = errors.New("Git is unavailable")
	ErrGitExecution   = errors.New("Git execution failed")
	ErrGitOutputLimit = errors.New("Git output exceeds limit")
)

// Only repositories with local directory metadata are supported. Gitfiles,
// external metadata symlinks and ancestor discovery fail closed.
func gitWorkspace(workspace string) (string, error) {
	root, err := (WorkspaceSandbox{}).Resolve(workspace, ".")
	if err != nil {
		return "", ErrGitRepository
	}
	metadata := filepath.Join(root, ".git")
	info, err := os.Lstat(metadata)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", ErrGitRepository
	}
	for _, name := range []string{"HEAD", "config"} {
		info, err := os.Lstat(filepath.Join(metadata, name))
		if err != nil || !info.Mode().IsRegular() {
			return "", ErrGitRepository
		}
	}
	for _, name := range []string{"commondir", "objects/info/alternates", "objects/info/http-alternates"} {
		if _, err := os.Lstat(filepath.Join(metadata, name)); !errors.Is(err, os.ErrNotExist) {
			return "", ErrGitRepository
		}
	}
	// Metadata symlinks must not redirect Git reads or index locks outside root.
	err = filepath.WalkDir(metadata, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrGitRepository
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrGitRepository
		}
		return nil
	})
	if err != nil {
		return "", ErrGitRepository
	}
	return root, nil
}

// This helper is private to the two fixed Git read operations; it is not a
// process executor API. Callers never supply flags or command strings.
func gitRead(ctx context.Context, root string, status bool, path string) ([]byte, error) {
	limits := gitProcessLimits()
	ctx, cancel := processContext(ctx, limits)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	args := []string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "core.bare=false", "-c", "core.worktree=" + root}
	if status {
		args = append(args, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	} else {
		args = append(args, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--ignore-submodules=all", "--")
		if path != "" {
			args = append(args, path)
		}
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_DIR="+filepath.Join(root, ".git"), "GIT_WORK_TREE="+root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	stdout, _, err := runLimitedProcess(ctx, cmd, limits)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(err, ErrProcessOutputLimit) {
		return nil, ErrGitOutputLimit
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitUnavailable
		}
		return nil, ErrGitExecution
	}
	return stdout, nil
}
