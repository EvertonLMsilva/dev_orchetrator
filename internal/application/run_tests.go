package application

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"dev-orchestrator/internal/domain"
)

var (
	ErrUnknownTestTarget = errors.New("unknown test target ID")
	ErrTestWorkspace     = errors.New("invalid test workspace or root module")
	ErrTestOutputLimit   = errors.New("test output exceeds limit")
	ErrTestExecution     = errors.New("could not execute Go tests")
)

// TestTargetRegistry is closed and immutable. Its zero value resolves only
// these configured IDs; callers cannot register commands, flags, or packages.
type TestTargetRegistry struct{}

func (TestTargetRegistry) Resolve(id domain.TestTargetID) (string, error) {
	switch id {
	case "all":
		return "./...", nil
	case "domain":
		return "./internal/domain", nil
	case "application":
		return "./internal/application", nil
	case "ports":
		return "./internal/ports", nil
	case "adapters":
		return "./internal/adapters/...", nil
	default:
		return "", ErrUnknownTestTarget
	}
}

type RunTestsResult struct {
	Target   domain.TestTargetID
	Stdout   string
	Stderr   string
	Success  bool
	ExitCode int
}

// RunTests executes only the registry's Go package target, without a shell.
// CommandContext kills the Go process, not necessarily descendant test binaries.
// WaitDelay bounds pipe waiting; reliable process-tree cleanup remains future
// platform-specific hardening, as documented in runLimitedProcess.
// Sandbox validation is a snapshot, as documented by WorkspaceSandbox.
func RunTests(ctx context.Context, workspace string, params domain.RunTestsParams) (RunTestsResult, error) {
	result := RunTestsResult{Target: params.Target, ExitCode: -1}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	target, err := (TestTargetRegistry{}).Resolve(params.Target)
	if err != nil {
		return result, err
	}
	limits := testProcessLimits()
	ctx, cancel := processContext(ctx, limits)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	sandbox := WorkspaceSandbox{}
	root, err := sandbox.Resolve(workspace, ".")
	if err != nil {
		return result, ErrTestWorkspace
	}
	// Require a regular root go.mod: no symlink or ancestor module discovery.
	info, err := os.Lstat(filepath.Join(root, "go.mod"))
	if err != nil || !info.Mode().IsRegular() {
		return result, ErrTestWorkspace
	}
	packageDir := strings.TrimSuffix(target, "/...")
	if target == "./..." {
		packageDir = "."
	}
	resolved, err := sandbox.Resolve(root, packageDir)
	if err != nil {
		return result, ErrTestWorkspace
	}
	info, err = os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return result, ErrTestWorkspace
	}
	cmd := exec.CommandContext(ctx, "go", "test", target)
	cmd.Dir = root
	// Environment must not add Go flags, toolchain executables, or ancestor workspaces.
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GO") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GOENV=off", "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local", "GO111MODULE=on")
	stdout, stderr, err := runLimitedProcess(ctx, cmd, limits)
	result.Stdout, result.Stderr = string(stdout), string(stderr)
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(err, ErrProcessOutputLimit) {
		return result, ErrTestOutputLimit
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return result, nil
		}
		return result, errors.Join(ErrTestExecution, err)
	}
	result.Success = true
	return result, nil
}
