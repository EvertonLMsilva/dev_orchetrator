//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMediatedCreateReplaceReplay(t *testing.T) {
	ctx := context.Background()
	control := t.TempDir()
	if err := os.Chmod(control, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := NewManagedWorkspaceStore(control, "instance")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.Provision(ctx, "project", map[string][]byte{"old.txt": []byte("before")})
	if err != nil {
		t.Fatal(err)
	}
	p := mediatedPolicy()
	a, err := s.BuildFixtureCandidate(ctx, w, p, domain.CandidateContext{ProjectID: "project", TaskID: "task", CorrelationID: "corr"}, map[string][]byte{"old.txt": []byte("after"), "new.txt": []byte("created")})
	if err != nil {
		t.Fatal(err)
	}
	q := mediatedRequest(t, s, w, p, a, "tx", "approval")
	r, err := s.Apply(ctx, q)
	if err != nil || r.State != domain.WriteApplied {
		t.Fatalf("apply: %+v %v", r, err)
	}
	for n, want := range map[string]string{"old.txt": "after", "new.txt": "created"} {
		b, err := os.ReadFile(filepath.Join(s.rootPath(w), n))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", n, b, err)
		}
	}
	effects := s.effects
	r, err = s.Apply(ctx, q)
	if err != nil || r.State != domain.WriteApplied || s.effects != effects {
		t.Fatalf("replay: %+v %v", r, err)
	}
	q.TransactionID = "other"
	if _, err = s.Apply(ctx, q); err == nil {
		t.Fatal("approval reused")
	}
}

func mediatedPolicy() domain.CandidatePolicy {
	return domain.CandidatePolicy{InputTargets: []string{"old.txt"}, WriteTargets: []string{"old.txt", "new.txt"}, Limits: domain.CandidateLimits{MaxOperations: 4, MaxFileBytes: 1024, MaxTotalBytes: 4096, MaxPathBytes: 256, MaxOutputBytes: 8192}}
}

func (s *ManagedWorkspaceStore) rootPath(w ManagedWorkspace) string {
	return filepath.Join(s.controlPath, w.WorkspaceID)
}

// Test-only fixture builder: no production API delivers a managed path to a
// CandidateGenerator or CandidateWriter.
func (s *ManagedWorkspaceStore) BuildFixtureCandidate(ctx context.Context, w ManagedWorkspace, p domain.CandidatePolicy, c domain.CandidateContext, edits map[string][]byte) (domain.CandidateArtifact, error) {
	var artifact domain.CandidateArtifact
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		root, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		defer root.Close()
		before, err := candidateSnapshot(ctx, int(root.Fd()), p)
		if err != nil {
			return err
		}
		after := map[string]domain.CandidateFile{}
		for path, f := range before {
			after[path] = f.Clone()
		}
		manifest := domain.WriteManifest{SchemaVersion: 1}
		blobs := map[string][]byte{}
		for path, data := range edits {
			old, exists := before[path]
			e := domain.WriteManifestEntry{Target: path, Operation: domain.WriteCreate, FileType: "regular", Postimage: domain.CandidateDigest(data), AfterMode: 0644}
			if exists {
				e.Operation = domain.WriteReplace
				e.Preimage = old.Identity
				e.BeforeMode = old.Mode
				e.AfterMode = old.Mode
			}
			manifest.Entries = append(manifest.Entries, e)
			blobs[e.Postimage] = data
			after[path] = domain.CandidateFile{Target: path, Identity: e.Postimage, Mode: e.AfterMode, Content: data}
		}
		candidate := domain.WriteCandidate{SchemaVersion: 1, ArtifactID: "fixture", ProjectID: c.ProjectID, TaskID: c.TaskID, CorrelationID: c.CorrelationID, WorkspaceIdentity: w.Identity(), BaseIdentity: candidateStateIdentity(p, before), ExpectedPostIdentity: candidateStateIdentity(p, after), OperationParametersIdentity: mediatedParametersIdentity(p), Manifest: manifest}
		artifact, err = domain.NewCandidateArtifact(candidate, blobs)
		return err
	})
	return artifact, err
}
func mediatedRequest(t *testing.T, s *ManagedWorkspaceStore, w ManagedWorkspace, p domain.CandidatePolicy, a domain.CandidateArtifact, tx, id string) MediatedWriteRequest {
	t.Helper()
	b, err := a.Candidate().Binding("policy-v1", id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	approval := domain.WriteApproval{SchemaVersion: 1, ApprovalID: domain.ApprovalID(id), WriteBinding: b, ApproverIdentity: "trusted-test", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)}
	if err = s.Issue(context.Background(), approval); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(context.Background(), approval, tx, now)
	if err != nil || !r.Granted {
		t.Fatalf("reserve: %v", err)
	}
	return MediatedWriteRequest{Workspace: w, Policy: p, Artifact: a, Context: b, ApprovalID: approval.ApprovalID, TransactionID: tx}
}
