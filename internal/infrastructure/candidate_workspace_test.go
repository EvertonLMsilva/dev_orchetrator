//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"encoding/base64"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testCandidatePolicy() domain.CandidatePolicy {
	return domain.CandidatePolicy{InputTargets: []string{"a"}, WriteTargets: []string{"a", "dir/b"}, ExcludedTargets: []string{"secret"}, Limits: domain.CandidateLimits{MaxOperations: 8, MaxFileBytes: 1024, MaxTotalBytes: 4096, MaxPathBytes: 256, MaxOutputBytes: 8192}}
}

func candidateProposalFor(w *candidateWorkspace, content string) []byte {
	input := w.Inputs()[0]
	data, _ := json.Marshal(domain.StructuredProposal{SchemaVersion: 1, Edits: []domain.StructuredEdit{{Operation: domain.WriteReplace, Target: "a", ExpectedPreimageIdentity: input.Identity, PostimageContent: base64.StdEncoding.EncodeToString([]byte(content)), PostimageIdentity: domain.CandidateDigest([]byte(content))}, {Operation: domain.WriteCreate, Target: "dir/b", PostimageContent: base64.StdEncoding.EncodeToString([]byte("b")), PostimageIdentity: domain.CandidateDigest([]byte("b"))}}})
	return data
}
func testCandidateWorkspace(t *testing.T) (*candidateWorkspace, string, string) {
	t.Helper()
	real, scratch := candidateFixture(t)
	port, err := NewCandidateWorkspaceFactory(scratch).Prepare(context.Background(), real, "registered", testCandidatePolicy())
	if err != nil {
		t.Fatal(err)
	}
	w := port.(*candidateWorkspace)
	t.Cleanup(func() { w.Close() })
	return w, real, scratch
}
func TestCandidateIndependentExtractorAndDenyMutations(t *testing.T) {
	for _, kind := range []string{"independent", "delete", "mode", "createdMode", "rootMode", "extra", "symlink", "hardlink", "git", "fifo", "emptyDirectory"} {
		t.Run(kind, func(t *testing.T) {
			w, real, scratch := testCandidateWorkspace(t)
			if err := w.Write(context.Background(), candidateProposalFor(w, "new")); err != nil {
				t.Fatal(err)
			}
			copyPath := filepath.Join(scratch, w.name)
			switch kind {
			case "independent":
				os.WriteFile(filepath.Join(copyPath, "a"), []byte("physically final"), 0644)
			case "delete":
				os.Remove(filepath.Join(copyPath, "a"))
			case "mode":
				os.Chmod(filepath.Join(copyPath, "a"), 0600)
			case "createdMode":
				os.Chmod(filepath.Join(copyPath, "dir", "b"), 0600)
			case "rootMode":
				os.Chmod(copyPath, 0755)
			case "extra":
				os.WriteFile(filepath.Join(copyPath, "outside"), []byte("evil"), 0644)
			case "symlink":
				os.Remove(filepath.Join(copyPath, "a"))
				os.Symlink(filepath.Join(real, "a"), filepath.Join(copyPath, "a"))
			case "hardlink":
				os.Remove(filepath.Join(copyPath, "a"))
				os.Link(filepath.Join(real, "a"), filepath.Join(copyPath, "a"))
			case "git":
				os.Mkdir(filepath.Join(copyPath, ".git"), 0700)
			case "fifo":
				os.Remove(filepath.Join(copyPath, "a"))
				unix.Mkfifo(filepath.Join(copyPath, "a"), 0600)
			case "emptyDirectory":
				os.Remove(filepath.Join(copyPath, "dir", "b"))
			}
			artifact, err := w.Extract(context.Background(), domain.CandidateContext{ProjectID: "p", TaskID: "t", CorrelationID: "c"})
			if kind == "independent" {
				if err != nil {
					t.Fatal(err)
				}
				if artifact.Candidate().Manifest.Entries[0].Postimage != domain.CandidateDigest([]byte("physically final")) {
					t.Fatal("trusted model delta")
				}
			} else if err == nil {
				t.Fatal("unsupported physical mutation accepted")
			}
			got, _ := os.ReadFile(filepath.Join(real, "a"))
			if string(got) != "old" {
				t.Fatal("real bytes changed")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestCandidateInvalidOutputAndPartialWriterDiscard(t *testing.T) {
	for _, kind := range []string{"invalid", "partial", "cancel", "timeout"} {
		t.Run(kind, func(t *testing.T) {
			w, real, scratch := testCandidateWorkspace(t)
			ctx := context.Background()
			data := candidateProposalFor(w, "new")
			if kind == "invalid" {
				data = []byte(`{"SchemaVersion":1,"Edits":[],"command":"touch /real/a"}`)
			}
			if kind == "partial" {
				w.beforeWrite = func(i int) error {
					if i == 1 {
						return errors.New("disk failure")
					}
					return nil
				}
			}
			if kind == "cancel" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if kind == "timeout" {
				c, cancel := context.WithDeadline(ctx, time.Unix(1, 0))
				defer cancel()
				ctx = c
			}
			if w.Write(ctx, data) == nil {
				t.Fatal("failure accepted")
			}
			if _, err := w.Extract(context.Background(), domain.CandidateContext{ProjectID: "p", TaskID: "t", CorrelationID: "c"}); err == nil {
				t.Fatal("partial candidate promoted")
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(scratch)
			if len(entries) != 0 {
				t.Fatal("not discarded")
			}
			got, _ := os.ReadFile(filepath.Join(real, "a"))
			if string(got) != "old" {
				t.Fatal("source changed")
			}
		})
	}
}
func TestCandidateSnapshotConfinementAndBase(t *testing.T) {
	for _, kind := range []string{"preexisting", "ancestorLink", "submodule", "excluded", "mode", "fifo", "scratchInside", "rootLink"} {
		t.Run(kind, func(t *testing.T) {
			real, scratch := candidateFixture(t)
			p := testCandidatePolicy()
			factory := NewCandidateWorkspaceFactory(scratch)
			var base string
			if kind == "preexisting" {
				w, err := factory.Prepare(context.Background(), real, "registered", p)
				if err != nil {
					t.Fatal(err)
				}
				base = w.(*candidateWorkspace).base
				w.Close()
				os.WriteFile(filepath.Join(real, "a"), []byte("preexisting local change"), 0644)
			}
			switch kind {
			case "ancestorLink":
				os.Mkdir(filepath.Join(real, "actual"), 0700)
				os.WriteFile(filepath.Join(real, "actual", "b"), []byte("secret"), 0644)
				os.Symlink("actual", filepath.Join(real, "dir"))
			case "submodule":
				os.Mkdir(filepath.Join(real, "dir"), 0700)
				os.WriteFile(filepath.Join(real, "dir", ".git"), []byte("gitdir: elsewhere"), 0644)
			case "excluded":
				p.ExcludedTargets = []string{"a"}
			case "mode":
				os.Chmod(filepath.Join(real, "a"), 0666)
			case "fifo":
				os.Remove(filepath.Join(real, "a"))
				unix.Mkfifo(filepath.Join(real, "a"), 0600)
			case "scratchInside":
				scratch = filepath.Join(real, "scratch")
				os.Mkdir(scratch, 0700)
				factory = NewCandidateWorkspaceFactory(scratch)
			case "rootLink":
				link := filepath.Join(t.TempDir(), "link")
				os.Symlink(real, link)
				real = link
			}
			w, err := factory.Prepare(context.Background(), real, "registered", p)
			if w != nil {
				defer w.Close()
			}
			if kind == "preexisting" {
				if err != nil || w.(*candidateWorkspace).base == base {
					t.Fatal("base ignored local change")
				}
			} else if err == nil {
				t.Fatal("unsafe snapshot accepted")
			}
		})
	}
}
func TestCandidateConcurrentCopiesAndCleanupIdentity(t *testing.T) {
	real, scratch := candidateFixture(t)
	factory := NewCandidateWorkspaceFactory(scratch)
	var wg sync.WaitGroup
	names := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			port, err := factory.Prepare(context.Background(), real, "registered", testCandidatePolicy())
			if err != nil {
				t.Error(err)
				return
			}
			w := port.(*candidateWorkspace)
			names <- w.name
			if err := w.Write(context.Background(), candidateProposalFor(w, "new")); err != nil {
				t.Error(err)
			}
			if _, err := w.Extract(context.Background(), domain.CandidateContext{ProjectID: "p", TaskID: "t", CorrelationID: "c"}); err != nil {
				t.Error(err)
			}
			if err := w.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	close(names)
	seen := map[string]bool{}
	for name := range names {
		if seen[name] {
			t.Fatal("shared copy")
		}
		seen[name] = true
	}
	port, err := factory.Prepare(context.Background(), real, "registered", testCandidatePolicy())
	if err != nil {
		t.Fatal(err)
	}
	w := port.(*candidateWorkspace)
	original := filepath.Join(scratch, w.name)
	moved := original + "-moved"
	if err = os.Rename(original, moved); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(original, 0700)
	if w.Close() == nil {
		t.Fatal("cleanup removed unowned root")
	}
	if _, err = os.Stat(original); err != nil {
		t.Fatal("unowned root removed")
	}
	os.Remove(original)
	os.Rename(moved, original)
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
}
func candidateFixture(t *testing.T) (string, string) {
	t.Helper()
	real := t.TempDir()
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "a"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	return real, scratch
}
func TestCandidateWorkspaceIndependentExtraction(t *testing.T) {
	real, scratch := candidateFixture(t)
	os.Mkdir(filepath.Join(real, ".git"), 0700)
	os.WriteFile(filepath.Join(real, ".git", "config"), []byte("git secret"), 0600)
	os.WriteFile(filepath.Join(real, "secret"), []byte("secret"), 0600)
	w, err := NewCandidateWorkspaceFactory(scratch).Prepare(context.Background(), real, "registered", testCandidatePolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	inputs := w.Inputs()
	if len(inputs) != 1 || string(inputs[0].Content) != "old" {
		t.Fatal(inputs)
	}
	copyRoot := filepath.Join(scratch, w.(*candidateWorkspace).name)
	for _, forbidden := range []string{".git", "secret"} {
		if _, err := os.Lstat(filepath.Join(copyRoot, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("exposed %s", forbidden)
		}
	}
	var sourceStat, copyStat unix.Stat_t
	if unix.Stat(filepath.Join(real, "a"), &sourceStat) != nil || unix.Stat(filepath.Join(copyRoot, "a"), &copyStat) != nil || copyStat.Nlink != 1 || copyStat.Dev == sourceStat.Dev && copyStat.Ino == sourceStat.Ino {
		t.Fatal("copy shares source inode")
	}
	data, _ := json.Marshal(domain.StructuredProposal{SchemaVersion: 1, Edits: []domain.StructuredEdit{{Operation: domain.WriteReplace, Target: "a", ExpectedPreimageIdentity: inputs[0].Identity, PostimageContent: base64.StdEncoding.EncodeToString([]byte("$(touch real); diff --git")), PostimageIdentity: domain.CandidateDigest([]byte("$(touch real); diff --git"))}, {Operation: domain.WriteCreate, Target: "dir/b", PostimageContent: base64.StdEncoding.EncodeToString([]byte("created")), PostimageIdentity: domain.CandidateDigest([]byte("created"))}}})
	if err := w.Write(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	artifact, err := w.Extract(context.Background(), domain.CandidateContext{ProjectID: "p", TaskID: "t", CorrelationID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	c := artifact.Candidate()
	if c.Validate() != nil || len(c.Manifest.Entries) != 2 {
		t.Fatal(c)
	}
	blob := artifact.Blob(c.Manifest.Entries[0].Postimage)
	blob[0] = '!'
	if artifact.Blob(c.Manifest.Entries[0].Postimage)[0] == '!' {
		t.Fatal("mutable blob")
	}
	c.Manifest.Entries[0].Target = "evil"
	if artifact.Candidate().Manifest.Entries[0].Target == "evil" {
		t.Fatal("mutable candidate")
	}
	got, _ := os.ReadFile(filepath.Join(real, "a"))
	if string(got) != "old" {
		t.Fatal("real mutation")
	}
	if _, err := os.Stat(filepath.Join(real, "dir")); !os.IsNotExist(err) {
		t.Fatal("real create")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(scratch)
	if len(entries) != 0 {
		t.Fatal("copy not removed")
	}
}
func TestCandidateSnapshotDenyLinks(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			real, scratch := candidateFixture(t)
			os.Rename(filepath.Join(real, "a"), filepath.Join(real, "outside"))
			var err error
			if kind == "symlink" {
				err = os.Symlink("outside", filepath.Join(real, "a"))
			} else {
				err = os.Link(filepath.Join(real, "outside"), filepath.Join(real, "a"))
			}
			if err != nil {
				t.Fatal(err)
			}
			w, err := NewCandidateWorkspaceFactory(scratch).Prepare(context.Background(), real, "registered", testCandidatePolicy())
			if w != nil {
				w.Close()
			}
			if err == nil {
				t.Fatal("link accepted")
			}
		})
	}
}
