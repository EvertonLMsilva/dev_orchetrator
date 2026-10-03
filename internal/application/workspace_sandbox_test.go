package application

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceSandboxResolve(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "foo")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, workspace, path, want string
		reject                      bool
	}{
		{"file", workspace, "main.go", filepath.Join(root, "main.go"), false},
		{"subdirectory", workspace, filepath.Join("src", "new", "main.go"), filepath.Join(root, "src", "new", "main.go"), false},
		{"root", workspace, ".", root, false},
		{"parent", workspace, "..", "", true},
		{"deep traversal", workspace, filepath.Join("..", "..", "secret"), "", true},
		{"absolute outside", workspace, base, "", true},
		{"absolute inside", workspace, filepath.Join(workspace, "main.go"), filepath.Join(root, "main.go"), false},
		{"prefix collision", workspace, filepath.Join(base, "foobar", "file"), "", true},
		{"normalization", workspace, "src" + string(filepath.Separator) + ".." + string(filepath.Separator) + "." + string(filepath.Separator) + "main.go", filepath.Join(root, "main.go"), false},
		{"empty workspace", "", ".", "", true},
		{"blank workspace", " \t", ".", "", true},
		{"empty path", workspace, "", "", true},
		{"blank path", workspace, " \t", "", true},
		{"missing workspace", filepath.Join(base, "missing"), ".", "", true},
		{"invalid path", workspace, "bad\x00path", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := (WorkspaceSandbox{}).Resolve(tc.workspace, tc.path)
			if tc.reject {
				if err == nil || got != "" {
					t.Fatalf("got %q, %v; want rejection", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestWorkspaceSandboxSymlinks(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	inside := filepath.Join(workspace, "inside")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{inside, outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, target string
		reject       bool
	}{
		{"internal", inside, false},
		{"external", outside, true},
		{"dangling", filepath.Join(base, "missing"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := filepath.Join(workspace, tc.name)
			if err := os.Symlink(tc.target, link); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{".", filepath.Join("new", "file")} {
				got, err := (WorkspaceSandbox{}).Resolve(workspace, filepath.Join(tc.name, suffix))
				if tc.reject {
					if err == nil || got != "" {
						t.Fatalf("got %q, %v; want rejection", got, err)
					}
					continue
				}
				canonical, errCanonical := filepath.EvalSymlinks(inside)
				if errCanonical != nil {
					t.Fatal(errCanonical)
				}
				if err != nil || got != filepath.Join(canonical, suffix) {
					t.Fatalf("got %q, %v", got, err)
				}
			}
		})
	}
	file := filepath.Join(workspace, "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := (WorkspaceSandbox{}).Resolve(workspace, filepath.Join("file", "child")); err == nil || got != "" {
		t.Fatalf("non-directory ancestor accepted: %q, %v", got, err)
	}
	if got, err := (WorkspaceSandbox{}).Resolve(file, "."); err == nil || got != "" {
		t.Fatalf("file workspace accepted: %q, %v", got, err)
	}
}
