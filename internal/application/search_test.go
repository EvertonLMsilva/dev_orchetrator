package application

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dev-orchestrator/internal/domain"
)

func TestSearch(t *testing.T) {
	root := t.TempDir()
	putCapabilityFile(t, root, "z.txt", "needle\n")
	putCapabilityFile(t, root, "sub/a.txt", "other\nneedle twice needle\nneedle\n")
	putCapabilityFile(t, root, "binary", "needle\x00")
	putCapabilityFile(t, root, "large", strings.Repeat("needle\n", MaxSearchFileBytes))
	outside := t.TempDir()
	putCapabilityFile(t, outside, "secret", "needle")
	if err := os.Symlink(outside, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	want := []SearchMatch{{Path: "sub/a.txt", Line: 2, Text: "needle twice needle"}, {Path: "sub/a.txt", Line: 3, Text: "needle"}, {Path: "z.txt", Line: 1, Text: "needle"}}
	for i := 0; i < 3; i++ {
		got, err := (SearchExecutor{}).Execute(root, "needle", "")
		if err != nil || !reflect.DeepEqual(got.Matches, want) || got.Limited {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
	got, err := (SearchExecutor{}).Execute(root, "needle", "sub")
	if err != nil || !reflect.DeepEqual(got.Matches, want[:2]) {
		t.Fatalf("base: %+v, %v", got, err)
	}
	got, err = (SearchExecutor{}).Execute(root, "needle", "z.txt")
	if err != nil || !reflect.DeepEqual(got.Matches, want[2:]) {
		t.Fatalf("file: %+v, %v", got, err)
	}
	for _, query := range []string{"", " \t\n"} {
		if _, err := (SearchExecutor{}).Execute(root, query, ""); !errors.Is(err, domain.ErrSearchQueryRequired) {
			t.Fatalf("got %v", err)
		}
	}
	for _, path := range []string{"../outside", "external"} {
		if _, err := (SearchExecutor{}).Execute(root, "needle", path); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("got %v", err)
		}
	}
}

func TestSearchResultLimit(t *testing.T) {
	root := t.TempDir()
	putCapabilityFile(t, root, "file", strings.Repeat("needle\n", MaxSearchResults+20))
	got, err := (SearchExecutor{}).Execute(root, "needle", "")
	if err != nil || len(got.Matches) != MaxSearchResults || !got.Limited || got.Matches[MaxSearchResults-1].Line != MaxSearchResults {
		t.Fatalf("got %+v, %v", got, err)
	}
}
