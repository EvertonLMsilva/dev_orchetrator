package application

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGitDiff(t *testing.T) {
	root := gitFixture(t)
	putCapabilityFile(t, root, "file", "after\n")
	putCapabilityFile(t, root, "-option", "option changed\n")
	for _, path := range []string{"", "file", "-option"} {
		got, err := (GitDiffExecutor{}).Execute(context.Background(), root, path)
		if err != nil || !strings.Contains(got.Diff, "+") || got.Bytes != len(got.Diff) {
			t.Fatalf("%s: %+v %v", path, got, err)
		}
		if path == "file" && strings.Contains(got.Diff, "option changed") {
			t.Fatal("path filter ignored")
		}
		if path == "-option" && strings.Contains(got.Diff, "+after") {
			t.Fatal("option interpreted incorrectly")
		}
	}
	if _, err := (GitDiffExecutor{}).Execute(context.Background(), root, "../outside"); !errors.Is(err, ErrUnsafePath) {
		t.Fatal(err)
	}
	// Literal pathspecs must not interpret Git's magic/glob syntax.
	if got, err := (GitDiffExecutor{}).Execute(context.Background(), root, "*"); err != nil || got.Diff != "" {
		t.Fatalf("magic pathspec: %+v %v", got, err)
	}
	putCapabilityFile(t, root, "file", strings.Repeat("large line\n", MaxGitOutputBytes/8))
	if _, err := (GitDiffExecutor{}).Execute(context.Background(), root, "file"); !errors.Is(err, ErrGitOutputLimit) {
		t.Fatalf("limit: %v", err)
	}
}
