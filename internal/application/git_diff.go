package application

import (
	"context"
	"path/filepath"
)

type GitDiffResult struct {
	Path  string
	Diff  string
	Bytes int
}
type GitDiffExecutor struct{}

func (GitDiffExecutor) Execute(ctx context.Context, workspace, path string) (GitDiffResult, error) {
	if err := ctx.Err(); err != nil {
		return GitDiffResult{}, err
	}
	root, err := gitWorkspace(workspace)
	if err != nil {
		return GitDiffResult{}, err
	}
	relative := ""
	if path != "" {
		resolved, err := (WorkspaceSandbox{}).Resolve(root, path)
		if err != nil {
			return GitDiffResult{}, ErrUnsafePath
		}
		relative, err = filepath.Rel(root, resolved)
		if err != nil {
			return GitDiffResult{}, ErrUnsafePath
		}
		relative = filepath.ToSlash(relative)
	}
	out, err := gitRead(ctx, root, false, relative)
	if err != nil {
		return GitDiffResult{}, err
	}
	return GitDiffResult{Path: relative, Diff: string(out), Bytes: len(out)}, nil
}
