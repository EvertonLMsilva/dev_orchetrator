package domain

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

func writeHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func writeManifest() WriteManifest {
	return WriteManifest{SchemaVersion: 1, Entries: []WriteManifestEntry{
		{Target: "src/b.go", Operation: WriteCreate, FileType: "regular", Postimage: writeHash("b"), AfterMode: 0644},
		{Target: "src/a.go", Operation: WriteReplace, FileType: "regular", Preimage: writeHash("old"), Postimage: writeHash("a"), BeforeMode: 0644, AfterMode: 0644},
	}}
}
func TestWriteManifestCanonicalIdentity(t *testing.T) {
	m := writeManifest()
	original := m.Entries[0]
	bytes, err := m.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Identity()
	if err != nil {
		t.Fatal(err)
	}
	m.Entries[0], m.Entries[1] = m.Entries[1], m.Entries[0]
	bytes2, _ := m.CanonicalBytes()
	id2, _ := m.Identity()
	if string(bytes) != string(bytes2) || id != id2 || !validWriteDigest(id) {
		t.Fatal("noncanonical identity")
	}
	if m.Entries[1] != original {
		t.Fatal("canonicalization mutated input")
	}
	m.Entries[0].Postimage = writeHash("changed")
	id3, _ := m.Identity()
	if id3 == id {
		t.Fatal("content not bound")
	}
}
func TestWriteManifestDeny(t *testing.T) {
	mutations := map[string]func(*WriteManifest){
		"version":          func(m *WriteManifest) { m.SchemaVersion = 2 },
		"empty":            func(m *WriteManifest) { m.Entries = nil },
		"duplicate":        func(m *WriteManifest) { m.Entries = append(m.Entries, m.Entries[0]) },
		"alias":            func(m *WriteManifest) { m.Entries[1].Target = "SRC/B.GO" },
		"ancestor":         func(m *WriteManifest) { m.Entries[1].Target = "src" },
		"delete":           func(m *WriteManifest) { m.Entries[0].Operation = "DELETE" },
		"rename":           func(m *WriteManifest) { m.Entries[0].Operation = "RENAME" },
		"symlink":          func(m *WriteManifest) { m.Entries[0].FileType = "symlink" },
		"hardlink":         func(m *WriteManifest) { m.Entries[0].FileType = "hardlink" },
		"device":           func(m *WriteManifest) { m.Entries[0].FileType = "device" },
		"submodule":        func(m *WriteManifest) { m.Entries[0].FileType = "submodule" },
		"special mode":     func(m *WriteManifest) { m.Entries[0].AfterMode = 04755 },
		"mode change":      func(m *WriteManifest) { m.Entries[1].AfterMode = 0755 },
		"missing preimage": func(m *WriteManifest) { m.Entries[1].Preimage = "" },
		"create preimage":  func(m *WriteManifest) { m.Entries[0].Preimage = writeHash("exists") },
		"hash":             func(m *WriteManifest) { m.Entries[0].Postimage = "not-a-hash" },
	}
	for _, p := range []string{"../x", "/x", "a\\b", "a:b", "a/*", "a//b", "a/./b", "a/../b", ".git/config", "a/.GIT/x", "a\n", "a.", "a ", "CON", "x/NUL.txt"} {
		path := p
		mutations["path "+p] = func(m *WriteManifest) { m.Entries[0].Target = path }
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			m := writeManifest()
			mutate(&m)
			if _, err := m.CanonicalBytes(); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}
func writeCandidate() WriteCandidate {
	return WriteCandidate{SchemaVersion: 1, ArtifactID: "artifact", ProjectID: "p", TaskID: "t", CorrelationID: "c", WorkspaceIdentity: "workspace-generation-1", BaseIdentity: writeHash("base"), ExpectedPostIdentity: writeHash("post"), OperationParametersIdentity: writeHash("parameters"), Manifest: writeManifest()}
}
func TestWriteCandidateIdentity(t *testing.T) {
	c := writeCandidate()
	a, err := c.Identity()
	if err != nil {
		t.Fatal(err)
	}
	c.Manifest.Entries[0], c.Manifest.Entries[1] = c.Manifest.Entries[1], c.Manifest.Entries[0]
	b, _ := c.Identity()
	if a != b {
		t.Fatal("candidate order dependent")
	}
	c.BaseIdentity = writeHash("another base")
	b, _ = c.Identity()
	if a == b {
		t.Fatal("base not bound")
	}
	c.ArtifactID = ""
	if _, err := c.Identity(); err == nil {
		t.Fatal("missing identity accepted")
	}
}

func TestWriteCandidateRejectsAmbiguousLabels(t *testing.T) {
	c := writeCandidate()
	c.ArtifactID = string([]byte{0xff})
	if _, err := c.Identity(); err == nil {
		t.Fatal("invalid UTF-8 can collide during JSON encoding")
	}
}
