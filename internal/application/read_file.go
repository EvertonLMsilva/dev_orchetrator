package application

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"unicode/utf8"
)

const MaxReadFileBytes = 256 * 1024

var (
	ErrUnsafePath     = errors.New("unsafe workspace path")
	ErrNotRegularFile = errors.New("path is not a regular file")
	ErrFileTooLarge   = errors.New("file exceeds read limit")
	ErrBinaryFile     = errors.New("file is not text")
)

type ReadFileResult struct {
	Path    string
	Content string
	Size    int64
}

type ReadFileExecutor struct{}

func (ReadFileExecutor) Execute(workspace, path string) (ReadFileResult, error) {
	access, err := openCapabilityWorkspace(workspace)
	if err != nil {
		return ReadFileResult{}, err
	}
	defer access.Close()
	resolved, err := (WorkspaceSandbox{}).Resolve(workspace, path)
	if err != nil {
		return ReadFileResult{}, errors.Join(ErrUnsafePath, capabilityError(err))
	}
	content, size, err := readCapabilityFile(access, resolved, MaxReadFileBytes)
	if err != nil {
		return ReadFileResult{}, err
	}
	rel, err := filepath.Rel(access.Name(), resolved)
	if err != nil {
		return ReadFileResult{}, ErrUnsafePath
	}
	return ReadFileResult{Path: filepath.ToSlash(rel), Content: string(content), Size: size}, nil
}

// Resolve supplies containment policy; Root confines actual access, including
// symlink replacements after Resolve. The initial workspace must be trusted.
// This does not isolate mounts or guarantee a consistent content snapshot.
func openCapabilityWorkspace(workspace string) (*os.Root, error) {
	// Root cannot guarantee confinement on these platforms.
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		return nil, ErrUnsafePath
	}
	canonical, err := (WorkspaceSandbox{}).Resolve(workspace, ".")
	if err != nil {
		return nil, errors.Join(ErrUnsafePath, capabilityError(err))
	}
	before, err := os.Stat(canonical)
	if err != nil {
		return nil, capabilityError(err)
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, capabilityError(err)
	}
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		root.Close()
		return nil, ErrUnsafePath
	}
	return root, nil
}

func readCapabilityFile(root *os.Root, resolved string, limit int) ([]byte, int64, error) {
	rel, err := filepath.Rel(root.Name(), resolved)
	if err != nil {
		return nil, 0, ErrUnsafePath
	}
	info, err := root.Stat(rel)
	if err != nil {
		return nil, 0, capabilityError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, ErrNotRegularFile
	}
	if info.Size() > int64(limit) {
		return nil, 0, ErrFileTooLarge
	}
	file, err := root.Open(rel)
	if err != nil {
		return nil, 0, capabilityError(err)
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil {
		return nil, 0, capabilityError(err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, ErrNotRegularFile
	}
	if info.Size() > int64(limit) {
		return nil, 0, ErrFileTooLarge
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, 0, capabilityError(err)
	}
	if len(content) > limit {
		return nil, 0, ErrFileTooLarge
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return nil, 0, ErrBinaryFile
	}
	return content, int64(len(content)), nil
}

// Keep error identity without leaking filesystem paths through PathError.
func capabilityError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return capabilityError(pe.Err)
	}
	return err
}
