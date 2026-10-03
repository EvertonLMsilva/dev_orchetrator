package application

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceSandbox validates paths against an existing workspace directory.
// It only inspects filesystem metadata, never file contents. Resolve is a
// snapshot: future consumers must protect against filesystem changes between
// validation and use; a returned string is not an atomic access capability.
type WorkspaceSandbox struct{}

// Resolve returns a canonical absolute path, including any nonexistent suffix.
// Relative paths are normalized before checking existing components. Existing
// symlinks must resolve successfully and remain inside the canonical workspace.
func (WorkspaceSandbox) Resolve(workspace, requestedPath string) (string, error) {
	if strings.TrimSpace(workspace) == "" || strings.TrimSpace(requestedPath) == "" {
		return "", errors.New("workspace and requested path are required")
	}
	root, err := filepath.Abs(workspace)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	path := requestedPath
	if !filepath.IsAbs(path) {
		// Reject volume-relative paths whose interpretation depends on drive state.
		if filepath.VolumeName(path) != "" {
			return "", errors.New("volume-relative path is not allowed")
		}
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	if !sandboxContains(root, path) {
		return "", errors.New("path escapes workspace")
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(canonicalRoot)
	if err != nil {
		return "", fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("workspace must be a directory")
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", fmt.Errorf("relative path: %w", err)
	}
	current := canonicalRoot
	if rel == "." {
		return current, nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		next := filepath.Join(current, part)
		_, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			result := filepath.Join(current, filepath.Join(parts[i:]...))
			if !sandboxContains(canonicalRoot, result) {
				return "", errors.New("path escapes workspace")
			}
			return result, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect path: %w", err)
		}
		current, err = filepath.EvalSymlinks(next)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		if !sandboxContains(canonicalRoot, current) {
			return "", errors.New("symlink escapes workspace")
		}
		if i < len(parts)-1 {
			info, err := os.Stat(current)
			if err != nil {
				return "", fmt.Errorf("inspect ancestor: %w", err)
			}
			if !info.IsDir() {
				return "", errors.New("path ancestor must be a directory")
			}
		}
	}
	return current, nil
}

func sandboxContains(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}
