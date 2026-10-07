//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
)

// VerifyManagedCommit independently reads physical content, Git objects/refs
// and persisted P10 journal receipts. It does not trust model summaries.
func (s *ManagedWorkspaceStore) VerifyManagedCommit(ctx context.Context, w ManagedWorkspace, r domain.ManagedRepositoryIdentity, writeID, branch, oid, tree, candidate string) error {
	return s.exclusive(ctx, func(db *managedDatabase) error {
		root, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		defer root.Close()
		repo, err := s.openRepository(db, w, r)
		if err != nil {
			return err
		}
		defer repo.Close()
		j, ok := db.Journals[writeID]
		if !ok || j.Workspace != w || j.State != string(domain.WriteApplied) {
			return domain.ErrWriteDenied
		}
		identity, err := j.Candidate.Identity()
		if err != nil || identity != candidate {
			return domain.ErrWriteDenied
		}
		files, err := managedSnapshot(ctx, int(root.Fd()), j.Policy)
		if err != nil || candidateStateIdentity(j.Policy, files) != j.Candidate.ExpectedPostIdentity {
			return domain.ErrWriteDenied
		}
		ref, err := gitRef(ctx, root, repo, branch)
		if err != nil || ref != oid {
			return domain.ErrWriteDenied
		}
		actual, err := managedGitTree(ctx, root, repo, oid)
		if err != nil {
			return err
		}
		receipt := false
		for _, op := range db.GitOperations {
			if op.Kind == string(domain.GitCommit) && op.State == "APPLIED" && op.Workspace == w && op.Binding.AppliedWriteTransactionID == writeID && op.Binding.CandidateIdentity == candidate && op.Result.OID == oid && op.Result.Tree == tree && op.Binding.BranchName == branch {
				commit, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "commit", oid)
				if err != nil || gitObjectID("commit", commit) != oid || string(commit) != string(op.CommitBytes) {
					return domain.ErrWriteDenied
				}
				receipt = true
			}
		}
		if !receipt {
			return domain.ErrWriteDenied
		}
		for _, entry := range j.Candidate.Manifest.Entries {
			f, ok := actual[entry.Target]
			if !ok || f.OID != gitObjectID("blob", files[entry.Target].Content) || f.Mode != gitMode(entry.AfterMode) {
				return domain.ErrWriteDenied
			}
			bytes, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "blob", f.OID)
			if err != nil || domain.CandidateDigest(bytes) != entry.Postimage {
				return domain.ErrWriteDenied
			}
		}
		return nil
	})
}
