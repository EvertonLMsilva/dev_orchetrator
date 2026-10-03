package application

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putCapabilityFile(t *testing.T, root, name, content string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	content := "Olá\r\nline two\n"
	putCapabilityFile(t, root, "sub/file.txt", content)
	got, err := (ReadFileExecutor{}).Execute(root, "sub/file.txt")
	if err != nil || got.Content != content || got.Path != "sub/file.txt" || got.Size != int64(len(content)) {
		t.Fatalf("got %+v, %v", got, err)
	}
	putCapabilityFile(t, root, "large", strings.Repeat("a", MaxReadFileBytes+1))
	putCapabilityFile(t, root, "binary", "text\x00data")
	for _, tc := range []struct {
		path string
		want error
	}{
		{"missing", os.ErrNotExist}, {"sub", ErrNotRegularFile}, {"../outside", ErrUnsafePath}, {"large", ErrFileTooLarge}, {"binary", ErrBinaryFile},
	} {
		t.Run(tc.path, func(t *testing.T) {
			_, err := (ReadFileExecutor{}).Execute(root, tc.path)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	putCapabilityFile(t, root, "exact", strings.Repeat("a", MaxReadFileBytes))
	if _, err := (ReadFileExecutor{}).Execute(root, "exact"); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileExternalSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	putCapabilityFile(t, outside, "secret", "secret")
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := (ReadFileExecutor{}).Execute(root, "link"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("got %v", err)
	}
}

func TestConfinedReadRejectsReplacementSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	putCapabilityFile(t, root, "file", "safe")
	putCapabilityFile(t, outside, "secret", "secret")
	access, err := openCapabilityWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	resolved, err := (WorkspaceSandbox{}).Resolve(root, "file")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "file")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readCapabilityFile(access, resolved, MaxReadFileBytes); err == nil {
		t.Fatal("replacement symlink accepted")
	}
}
