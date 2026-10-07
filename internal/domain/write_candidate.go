package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const WriteSchemaVersion = 1
const (
	WriteCreate  WriteFileOperation = "CREATE"
	WriteReplace WriteFileOperation = "REPLACE"
)

type WriteFileOperation string

var ErrInvalidWriteCandidate = errors.New("invalid write candidate or manifest")

// Hashes describe immutable blobs. This contract performs no filesystem/blob
// access and does not establish that a caller actually possesses those bytes.
type WriteManifestEntry struct {
	Target     string
	Operation  WriteFileOperation
	FileType   string
	Preimage   string
	Postimage  string
	BeforeMode uint32
	AfterMode  uint32
}
type WriteManifest struct {
	SchemaVersion int
	Entries       []WriteManifestEntry
}

func validWriteDigest(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func writeLabel(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Portable literal paths only, with no Git metadata, Windows aliases, ADS,
// glob syntax or traversal. Physical confinement belongs to the future applier.
func validWriteTarget(value string) bool {
	if value == "" || len(value) > 256 || strings.HasPrefix(value, "/") {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-/", r)) {
			return false
		}
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") || strings.EqualFold(part, ".git") {
			return false
		}
		stem := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" || stem == "CONIN$" || stem == "CONOUT$" {
			return false
		}
		if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
			return false
		}
	}
	return true
}
func canonicalWriteTargets(targets []string) ([]string, error) {
	if len(targets) == 0 || len(targets) > 1024 {
		return nil, ErrInvalidWriteCandidate
	}
	copy := append([]string(nil), targets...)
	seen := make(map[string]bool, len(copy))
	for _, p := range copy {
		key := strings.ToLower(p)
		if !validWriteTarget(p) || seen[key] {
			return nil, ErrInvalidWriteCandidate
		}
		seen[key] = true
	}
	sort.Strings(copy)
	for key := range seen {
		parts := strings.Split(key, "/")
		for i := 1; i < len(parts); i++ {
			if seen[strings.Join(parts[:i], "/")] {
				return nil, ErrInvalidWriteCandidate
			}
		}
	}
	return copy, nil
}
func regularWriteMode(mode uint32) bool { return mode == 0600 || mode == 0644 || mode == 0755 }
func (m WriteManifest) canonical() (WriteManifest, error) {
	if m.SchemaVersion != WriteSchemaVersion {
		return WriteManifest{}, ErrInvalidWriteCandidate
	}
	entries := append([]WriteManifestEntry(nil), m.Entries...)
	targets := make([]string, len(entries))
	for i, e := range entries {
		targets[i] = e.Target
		if e.FileType != "regular" || !validWriteDigest(e.Postimage) || !regularWriteMode(e.AfterMode) {
			return WriteManifest{}, ErrInvalidWriteCandidate
		}
		switch e.Operation {
		case WriteCreate:
			if e.Preimage != "" || e.BeforeMode != 0 {
				return WriteManifest{}, ErrInvalidWriteCandidate
			}
		case WriteReplace:
			if !validWriteDigest(e.Preimage) || !regularWriteMode(e.BeforeMode) || e.AfterMode != e.BeforeMode {
				return WriteManifest{}, ErrInvalidWriteCandidate
			}
		default:
			return WriteManifest{}, ErrInvalidWriteCandidate
		}
	}
	if _, err := canonicalWriteTargets(targets); err != nil {
		return WriteManifest{}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Target < entries[j].Target })
	return WriteManifest{SchemaVersion: m.SchemaVersion, Entries: entries}, nil
}
func (m WriteManifest) CanonicalBytes() ([]byte, error) {
	canonical, err := m.canonical()
	if err != nil {
		return nil, err
	}
	return json.Marshal(canonical)
}
func writeIdentity(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func (m WriteManifest) Identity() (string, error) {
	data, err := m.CanonicalBytes()
	if err != nil {
		return "", err
	}
	return writeIdentity(data), nil
}

type WriteCandidate struct {
	SchemaVersion               int
	ArtifactID                  string
	ProjectID                   ProjectID
	TaskID                      TaskID
	CorrelationID               string
	WorkspaceIdentity           string
	BaseIdentity                string
	ExpectedPostIdentity        string
	OperationParametersIdentity string
	Manifest                    WriteManifest
}

func (c WriteCandidate) Validate() error {
	if c.SchemaVersion != WriteSchemaVersion {
		return ErrInvalidWriteCandidate
	}
	for _, value := range []string{c.ArtifactID, string(c.ProjectID), string(c.TaskID), c.CorrelationID, c.WorkspaceIdentity} {
		if !writeLabel(value) {
			return ErrInvalidWriteCandidate
		}
	}
	for _, value := range []string{c.BaseIdentity, c.ExpectedPostIdentity, c.OperationParametersIdentity} {
		if !validWriteDigest(value) {
			return ErrInvalidWriteCandidate
		}
	}
	_, err := c.Manifest.canonical()
	return err
}
func (c WriteCandidate) Identity() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	c.Manifest, _ = c.Manifest.canonical()
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return writeIdentity(data), nil
}

// Binding constructs descriptive context, not approval or runtime authority.
func (c WriteCandidate) Binding(policy, nonce string) (WriteBinding, error) {
	if err := c.Validate(); err != nil {
		return WriteBinding{}, err
	}
	diff, _ := c.Manifest.Identity()
	targets := make([]string, len(c.Manifest.Entries))
	for i, e := range c.Manifest.Entries {
		targets[i] = e.Target
	}
	targets, _ = canonicalWriteTargets(targets)
	b := WriteBinding{ProjectID: c.ProjectID, TaskID: c.TaskID, CorrelationID: c.CorrelationID, WorkspaceIdentity: c.WorkspaceIdentity, BaseIdentity: c.BaseIdentity, OperationKind: WriteApply, AllowedTargets: targets, ApprovedDiffIdentity: diff, ApprovedArtifactID: c.ArtifactID, ExpectedPostIdentity: c.ExpectedPostIdentity, OperationParametersIdentity: c.OperationParametersIdentity, PolicyVersion: policy, Nonce: nonce}
	return b, b.Validate()
}
