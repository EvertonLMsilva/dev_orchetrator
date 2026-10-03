package application

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
)

const (
	MaxSearchResults   = 100
	MaxSearchFileBytes = 256 * 1024
)

type SearchMatch = ports.SearchMatch

type SearchResult = ports.SearchResult

type SearchExecutor struct{}

// Execute performs literal, case-sensitive matching, returning one match per
// line in lexical path order and ascending line order. Oversized and binary
// files are skipped entirely. Directory symlinks are never traversed.
func (e SearchExecutor) Execute(workspace, query, path string) (SearchResult, error) {
	return e.ExecuteContext(context.Background(), workspace, query, path)
}

// ExecuteContext preserves caller cancellation during bounded file traversal.
func (SearchExecutor) ExecuteContext(ctx context.Context, workspace, query, path string) (SearchResult, error) {
	if err := ctx.Err(); err != nil {
		return SearchResult{}, err
	}
	result := SearchResult{Matches: []SearchMatch{}}
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, domain.ErrSearchQueryRequired
	}
	access, err := openCapabilityWorkspace(workspace)
	if err != nil {
		return SearchResult{}, err
	}
	defer access.Close()
	if path == "" {
		path = "."
	}
	base, err := (WorkspaceSandbox{}).Resolve(workspace, path)
	if err != nil {
		return SearchResult{}, errors.Join(ErrUnsafePath, capabilityError(err))
	}
	rel, err := filepath.Rel(access.Name(), base)
	if err != nil {
		return SearchResult{}, ErrUnsafePath
	}
	err = fs.WalkDir(access.FS(), filepath.ToSlash(rel), func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return capabilityError(walkErr)
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		resolved, err := (WorkspaceSandbox{}).Resolve(workspace, filepath.Join(access.Name(), filepath.FromSlash(name)))
		if err != nil {
			return errors.Join(ErrUnsafePath, capabilityError(err))
		}
		content, _, err := readCapabilityFile(access, resolved, MaxSearchFileBytes)
		if errors.Is(err, ErrFileTooLarge) || errors.Is(err, ErrBinaryFile) || errors.Is(err, ErrNotRegularFile) {
			return nil
		}
		if err != nil {
			return err
		}
		// The entire buffer is bounded by MaxSearchFileBytes; no project index is kept.
		for i, line := range strings.Split(string(content), "\n") {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !strings.Contains(line, query) {
				continue
			}
			result.Matches = append(result.Matches, SearchMatch{Path: name, Line: i + 1, Text: strings.TrimSuffix(line, "\r")})
			if len(result.Matches) == MaxSearchResults {
				result.Limited = true
				return fs.SkipAll
			}
		}
		return nil
	})
	if err != nil {
		return SearchResult{}, capabilityError(err)
	}
	return result, nil
}
