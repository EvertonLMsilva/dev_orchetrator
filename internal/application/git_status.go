package application

import (
	"bytes"
	"context"
)

type GitStatusEntry struct {
	Path           string
	OriginalPath   string
	IndexStatus    string
	WorkTreeStatus string
}
type GitStatusResult struct{ Entries []GitStatusEntry }
type GitStatusExecutor struct{}

func (GitStatusExecutor) Execute(ctx context.Context, workspace string) (GitStatusResult, error) {
	root, err := gitWorkspace(workspace)
	if err != nil {
		return GitStatusResult{}, err
	}
	out, err := gitRead(ctx, root, true, "")
	if err != nil {
		return GitStatusResult{}, err
	}
	result := GitStatusResult{Entries: []GitStatusEntry{}}
	for len(out) > 0 {
		end := bytes.IndexByte(out, 0)
		if end < 4 || out[2] != ' ' {
			return GitStatusResult{}, ErrGitExecution
		}
		entry := GitStatusEntry{Path: string(out[3:end]), IndexStatus: string(out[0:1]), WorkTreeStatus: string(out[1:2])}
		out = out[end+1:]
		if entry.IndexStatus == "R" || entry.IndexStatus == "C" || entry.WorkTreeStatus == "R" || entry.WorkTreeStatus == "C" {
			end = bytes.IndexByte(out, 0)
			if end < 1 {
				return GitStatusResult{}, ErrGitExecution
			}
			entry.OriginalPath = string(out[:end])
			out = out[end+1:]
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}
