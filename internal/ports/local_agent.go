package ports

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
)

// LocalAgent executes a validated action in its registered project workspace.
type LocalAgent interface {
	Execute(context.Context, domain.Action) (ActionResult, error)
}

var ErrInvalidActionResult = errors.New("result does not match action type")

type SearchMatch struct {
	Path string
	Line int
	Text string
}
type SearchResult struct {
	Matches []SearchMatch
	Limited bool
}
type ReadFileResult struct {
	Path    string
	Content string
	Size    int64
}
type GitStatusEntry struct {
	Path           string
	OriginalPath   string
	IndexStatus    string
	WorkTreeStatus string
}
type GitStatusResult struct{ Entries []GitStatusEntry }
type GitDiffResult struct {
	Path  string
	Diff  string
	Bytes int
}
type RunTestsResult struct {
	Target   domain.TestTargetID
	Stdout   string
	Stderr   string
	Success  bool
	ExitCode int
}

// ActionResult is a closed union. Call Validate at boundaries; exactly the
// result matching Type must be present, including for GIT_STATUS.
type ActionResult struct {
	Type            domain.ActionType
	SearchResult    *SearchResult
	ReadFileResult  *ReadFileResult
	GitStatusResult *GitStatusResult
	GitDiffResult   *GitDiffResult
	RunTestsResult  *RunTestsResult
}

func (r ActionResult) Validate() error {
	switch r.Type {
	case domain.ActionTypeSearch, domain.ActionTypeReadFile, domain.ActionTypeGitStatus, domain.ActionTypeGitDiff, domain.ActionTypeRunTests:
	default:
		return ErrInvalidActionResult
	}
	if (r.SearchResult != nil) != (r.Type == domain.ActionTypeSearch) ||
		(r.ReadFileResult != nil) != (r.Type == domain.ActionTypeReadFile) ||
		(r.GitStatusResult != nil) != (r.Type == domain.ActionTypeGitStatus) ||
		(r.GitDiffResult != nil) != (r.Type == domain.ActionTypeGitDiff) ||
		(r.RunTestsResult != nil) != (r.Type == domain.ActionTypeRunTests) {
		return ErrInvalidActionResult
	}
	return nil
}
