package domain

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"unicode/utf8"
)

var ErrCandidateDenied = errors.New("candidate denied")

// All limits and paths are trusted configuration; none come from model output.
type CandidateLimits struct {
	MaxOperations  int
	MaxFileBytes   int64
	MaxTotalBytes  int64
	MaxPathBytes   int
	MaxOutputBytes int64
}
type CandidatePolicy struct {
	InputTargets    []string
	WriteTargets    []string
	ExcludedTargets []string
	Limits          CandidateLimits
}

func (p CandidatePolicy) Clone() CandidatePolicy {
	p.InputTargets = append([]string(nil), p.InputTargets...)
	p.WriteTargets = append([]string(nil), p.WriteTargets...)
	p.ExcludedTargets = append([]string(nil), p.ExcludedTargets...)
	return p
}
func (p CandidatePolicy) Targets() []string {
	seen := map[string]bool{}
	for _, list := range [][]string{p.InputTargets, p.WriteTargets} {
		for _, s := range list {
			seen[s] = true
		}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
func (p CandidatePolicy) Excluded(target string) bool {
	for _, e := range p.ExcludedTargets {
		if strings.EqualFold(target, e) || strings.HasPrefix(strings.ToLower(target), strings.ToLower(e)+"/") {
			return true
		}
	}
	return false
}
func (p CandidatePolicy) Validate() error {
	l := p.Limits
	if l.MaxOperations < 1 || l.MaxOperations > 1024 || l.MaxFileBytes < 1 || l.MaxFileBytes > 16<<20 || l.MaxTotalBytes < l.MaxFileBytes || l.MaxTotalBytes > 64<<20 || l.MaxPathBytes < 1 || l.MaxPathBytes > 256 || l.MaxOutputBytes < 1 || l.MaxOutputBytes > 128<<20 {
		return ErrCandidateDenied
	}
	if _, err := canonicalWriteTargets(p.WriteTargets); err != nil {
		return ErrCandidateDenied
	}
	if len(p.InputTargets) > 0 {
		if _, err := canonicalWriteTargets(p.InputTargets); err != nil {
			return ErrCandidateDenied
		}
	}
	if _, err := canonicalWriteTargets(p.Targets()); err != nil {
		return ErrCandidateDenied
	}
	for _, e := range p.ExcludedTargets {
		if !validWriteTarget(e) {
			return ErrCandidateDenied
		}
	}
	for _, s := range p.Targets() {
		if len(s) > l.MaxPathBytes || p.Excluded(s) {
			return ErrCandidateDenied
		}
	}
	return nil
}

type CandidateFile struct {
	Target   string
	Identity string
	Mode     uint32
	Content  []byte
}

func (f CandidateFile) Clone() CandidateFile { f.Content = append([]byte(nil), f.Content...); return f }
func CandidateDigest(data []byte) string     { return writeIdentity(data) }

type CandidateContext struct {
	ProjectID     ProjectID
	TaskID        TaskID
	CorrelationID string
}

func (c CandidateContext) Validate() error {
	if !writeLabel(string(c.ProjectID)) || !writeLabel(string(c.TaskID)) || !writeLabel(c.CorrelationID) {
		return ErrCandidateDenied
	}
	return nil
}

type StructuredEdit struct {
	Operation                WriteFileOperation
	Target                   string
	ExpectedPreimageIdentity string
	PostimageContent         string
	PostimageIdentity        string
}
type StructuredProposal struct {
	SchemaVersion int
	Edits         []StructuredEdit
}

// Inspect every object key before decoding: encoding/json otherwise accepts
// duplicate keys and case-insensitive field aliases. Depth is bounded too.
func inspectCandidateJSON(d *json.Decoder, depth int) error {
	if depth > 8 {
		return ErrCandidateDenied
	}
	tok, err := d.Token()
	if err != nil {
		return ErrCandidateDenied
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return ErrCandidateDenied
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return ErrCandidateDenied
			}
			seen[s] = true
			if err := inspectCandidateJSON(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := inspectCandidateJSON(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return ErrCandidateDenied
	}
	_, err = d.Token()
	return err
}
func exactCandidateObject(data []byte, keys ...string) (map[string]json.RawMessage, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(data, &m) != nil || len(m) != len(keys) {
		return nil, ErrCandidateDenied
	}
	for _, key := range keys {
		v, ok := m[key]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, ErrCandidateDenied
		}
	}
	return m, nil
}
func ValidateStructuredProposal(data []byte, p CandidatePolicy, before map[string]CandidateFile) (StructuredProposal, error) {
	deny := func() (StructuredProposal, error) { return StructuredProposal{}, ErrCandidateDenied }
	if p.Validate() != nil || int64(len(data)) > p.Limits.MaxOutputBytes || !utf8.Valid(data) {
		return deny()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if inspectCandidateJSON(d, 0) != nil {
		return deny()
	}
	if _, err := d.Token(); err != io.EOF {
		return deny()
	}
	top, err := exactCandidateObject(data, "SchemaVersion", "Edits")
	if err != nil {
		return deny()
	}
	var version int
	if json.Unmarshal(top["SchemaVersion"], &version) != nil || version != WriteSchemaVersion {
		return deny()
	}
	var raw []json.RawMessage
	if json.Unmarshal(top["Edits"], &raw) != nil || len(raw) == 0 || len(raw) > p.Limits.MaxOperations {
		return deny()
	}
	allowed := map[string]bool{}
	for _, s := range p.WriteTargets {
		allowed[s] = true
	}
	proposal := StructuredProposal{SchemaVersion: version}
	targets := []string{}
	var total int64
	for _, r := range raw {
		if _, err := exactCandidateObject(r, "Operation", "Target", "ExpectedPreimageIdentity", "PostimageContent", "PostimageIdentity"); err != nil {
			return deny()
		}
		var e StructuredEdit
		if json.Unmarshal(r, &e) != nil || !allowed[e.Target] || p.Excluded(e.Target) || !validWriteTarget(e.Target) || len(e.Target) > p.Limits.MaxPathBytes {
			return deny()
		}
		content, err := base64.StdEncoding.Strict().DecodeString(e.PostimageContent)
		if err != nil || base64.StdEncoding.EncodeToString(content) != e.PostimageContent || !validWriteDigest(e.PostimageIdentity) || CandidateDigest(content) != e.PostimageIdentity || int64(len(content)) > p.Limits.MaxFileBytes {
			return deny()
		}
		total += int64(len(content))
		if total > p.Limits.MaxTotalBytes {
			return deny()
		}
		old, exists := before[e.Target]
		switch e.Operation {
		case WriteCreate:
			if exists || e.ExpectedPreimageIdentity != "" {
				return deny()
			}
		case WriteReplace:
			if !exists || !validWriteDigest(e.ExpectedPreimageIdentity) || old.Identity != e.ExpectedPreimageIdentity || CandidateDigest(old.Content) != old.Identity {
				return deny()
			}
		default:
			return deny()
		}
		targets = append(targets, e.Target)
		proposal.Edits = append(proposal.Edits, e)
	}
	if _, err := canonicalWriteTargets(targets); err != nil {
		return deny()
	}
	sort.Slice(proposal.Edits, func(i, j int) bool { return proposal.Edits[i].Target < proposal.Edits[j].Target })
	return proposal, nil
}

// An artifact owns all bytes and returns detached views. It grants no authority.
type CandidateArtifact struct {
	candidate WriteCandidate
	blobs     map[string][]byte
}

func NewCandidateArtifact(c WriteCandidate, blobs map[string][]byte) (CandidateArtifact, error) {
	if c.Validate() != nil {
		return CandidateArtifact{}, ErrCandidateDenied
	}
	canonical, err := c.Manifest.canonical()
	if err != nil {
		return CandidateArtifact{}, err
	}
	c.Manifest = canonical
	a := CandidateArtifact{candidate: c, blobs: map[string][]byte{}}
	for _, e := range c.Manifest.Entries {
		b, ok := blobs[e.Postimage]
		if !ok || CandidateDigest(b) != e.Postimage {
			return CandidateArtifact{}, ErrCandidateDenied
		}
		a.blobs[e.Postimage] = append([]byte(nil), b...)
	}
	return a, nil
}
func (a CandidateArtifact) Candidate() WriteCandidate {
	c := a.candidate
	c.Manifest.Entries = append([]WriteManifestEntry(nil), c.Manifest.Entries...)
	return c
}
func (a CandidateArtifact) Blob(identity string) []byte {
	return append([]byte(nil), a.blobs[identity]...)
}
