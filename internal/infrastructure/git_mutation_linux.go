//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// ProvisionRepository is trusted infrastructure bootstrap only: new external
// metadata, no import, remote, content checkout or user Git gate consumption.
func (s *ManagedWorkspaceStore) ProvisionRepository(ctx context.Context, w ManagedWorkspace, p domain.CandidatePolicy, policy string) (domain.ManagedRepositoryIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	p = p.Clone()
	var result domain.ManagedRepositoryIdentity
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		if p.Validate() != nil || (domain.CandidateContext{ProjectID: w.ProjectID, TaskID: "provision", CorrelationID: policy}).Validate() != nil {
			return domain.ErrWriteDenied
		}
		root, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		defer root.Close()
		gitMaps(db)
		if _, ok := db.Repositories[w.WorkspaceID]; ok {
			return domain.ErrWriteDenied
		}
		name := gitRepoName(w)
		var exists unix.Stat_t
		if err = unix.Fstatat(int(s.control.Fd()), name, &exists, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return ErrManagedWriteBlocked
		}
		j := gitOperationRecord{OperationID: "provision-" + w.WorkspaceID, Kind: "GIT_PROVISION", Workspace: w, Repository: domain.ManagedRepositoryIdentity{RepositoryID: name, WorkspaceID: w.WorkspaceID, WorkspaceGeneration: w.Generation, PolicyVersion: policy}, Policy: p.Clone(), State: "PREPARED"}
		if _, ok := db.GitOperations[j.OperationID]; ok {
			return ErrManagedWriteBlocked
		}
		if err = s.gitAcquire(db, j); err != nil {
			return err
		}
		if err = s.checkpoint("git-lease", 0); err != nil {
			return err
		}
		pinned, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		pinned.Close()
		snapshot, err := managedSnapshot(ctx, int(root.Fd()), p)
		if err != nil {
			return err
		}
		flat := map[string]gitTreeFile{}
		for path, f := range snapshot {
			flat[path] = gitTreeFile{gitMode(f.Mode), gitObjectID("blob", f.Content)}
		}
		tree, err := gitExpectedTree(flat)
		if err != nil {
			return err
		}
		actor := domain.GitActor{Name: "Dev Orchestrator", Email: "orchestrator@local.invalid", UnixSeconds: time.Now().UTC().Unix()}
		j.ExpectedTree = tree
		j.CommitBytes = gitCommitBytes(tree, "", "Managed repository baseline\n", actor, actor)
		j.ExpectedCommit = gitObjectID("commit", j.CommitBytes)
		j.Repository.InitialHead = j.ExpectedCommit
		j.Repository.InitialTree = tree
		j.Repository.InitialBaseIdentity = candidateStateIdentity(p, snapshot)
		j.IndexName, err = gitIndexName()
		if err != nil {
			return err
		}
		db.GitOperations[j.OperationID] = j
		if err = s.save(db); err != nil {
			return err
		}
		if err = unix.Mkdirat(int(s.control.Fd()), name, 0700); err != nil {
			return err
		}
		repo, err := candidateDirectory(int(s.control.Fd()), name)
		if err != nil {
			return err
		}
		defer repo.Close()
		var st unix.Stat_t
		if unix.Fstat(int(repo.Fd()), &st) != nil {
			return ErrManagedWriteBlocked
		}
		j.Repository.GitDirIdentity = managedRootIdentity(st)
		j.State = "APPLYING"
		db.GitOperations[j.OperationID] = j
		if err = s.control.Sync(); err != nil {
			return err
		}
		if err = s.save(db); err != nil {
			return err
		}
		if _, err = managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "init", "--bare", "--template=", "/proc/self/fd/4"); err != nil {
			return err
		}
		if err = managedGitMetadata(repo, true); err != nil {
			return err
		}
		if _, err = managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
			return err
		}
		built, err := gitBuildTree(ctx, root, repo, j.IndexName, "", snapshot)
		if err != nil || built != tree {
			return ErrManagedWriteBlocked
		}
		out, err := managedGit(ctx, root, repo, "", []byte("Managed repository baseline\n"), actor, actor, "commit-tree", "--no-gpg-sign", tree)
		if err != nil || strings.TrimSpace(string(out)) != j.ExpectedCommit {
			return ErrManagedWriteBlocked
		}
		if _, err = managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "update-ref", "--no-deref", "refs/heads/main", j.ExpectedCommit, gitZeroOID); err != nil {
			return err
		}
		if err = s.checkpoint("git-provisioned", 0); err != nil {
			return err
		}
		if !gitExactState(ctx, root, repo, j, db) || gitDurable(repo) != nil {
			return ErrManagedWriteBlocked
		}
		db.Repositories[w.WorkspaceID] = j.Repository
		res := GitMutationResult{State: "APPLIED", OID: j.ExpectedCommit, Tree: tree}
		if err = s.gitFinish(db, j, res); err != nil {
			return err
		}
		result = j.Repository
		return nil
	})
	return result, err
}

func (s *ManagedWorkspaceStore) gitCommitPlan(ctx context.Context, db *managedDatabase, root, repo *os.File, w ManagedWorkspace, r domain.ManagedRepositoryIdentity, b domain.GitBinding) (map[string]domain.CandidateFile, error) {
	write, ok := db.Journals[b.AppliedWriteTransactionID]
	if !ok || write.State != string(domain.WriteApplied) || write.Workspace != w || write.Candidate.ProjectID != b.ProjectID || write.Candidate.TaskID != b.TaskID || write.Candidate.CorrelationID != b.CorrelationID {
		return nil, domain.ErrWriteDenied
	}
	identity, _ := write.Candidate.Identity()
	if identity != b.CandidateIdentity {
		return nil, domain.ErrWriteDenied
	}
	branch, ok := db.GitOperations[b.BranchOperationID]
	if !ok || branch.Kind != string(domain.GitBranch) || branch.State != "APPLIED" || branch.Workspace != w || branch.Repository != r || branch.Binding.BranchName != b.BranchName || branch.Binding.ProjectID != b.ProjectID || branch.Binding.TaskID != b.TaskID || branch.Binding.CorrelationID != b.CorrelationID {
		return nil, domain.ErrWriteDenied
	}
	parent, err := gitRef(ctx, root, repo, b.BranchName)
	if err != nil || parent != b.Parent {
		return nil, domain.ErrWriteDenied
	}
	flat, err := managedGitTree(ctx, root, repo, b.Parent)
	if err != nil {
		return nil, err
	}
	files := map[string]domain.CandidateFile{}
	for _, entry := range write.Candidate.Manifest.Entries {
		current, err := candidateRead(ctx, int(root.Fd()), entry.Target, write.Policy.Limits.MaxFileBytes, true)
		approved, ok := write.Postimages[entry.Target]
		if err != nil || !ok || current.Identity != entry.Postimage || current.Mode != entry.AfterMode || domain.CandidateDigest(approved.Content) != entry.Postimage {
			return nil, domain.ErrWriteDenied
		}
		old, exists := flat[entry.Target]
		if entry.Operation == domain.WriteCreate && exists {
			return nil, domain.ErrWriteDenied
		}
		if entry.Operation == domain.WriteReplace {
			pre := write.Preimages[entry.Target]
			if !exists || old.OID != gitObjectID("blob", pre.Content) || old.Mode != gitMode(entry.BeforeMode) {
				return nil, domain.ErrWriteDenied
			}
		}
		flat[entry.Target] = gitTreeFile{gitMode(entry.AfterMode), gitObjectID("blob", approved.Content)}
		postOID := gitObjectID("blob", approved.Content)
		parent, name, openErr := candidateParent(int(repo.Fd()), "objects/"+postOID[:2]+"/"+postOID[2:], false, false)
		if openErr == nil {
			var st unix.Stat_t
			statErr := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
			parent.Close()
			if statErr == nil {
				stored, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "blob", postOID)
				if err != nil || domain.CandidateDigest(stored) != entry.Postimage {
					return nil, domain.ErrWriteDenied
				}
			} else if !errors.Is(statErr, unix.ENOENT) {
				return nil, domain.ErrWriteDenied
			}
		} else if !errors.Is(openErr, unix.ENOENT) {
			return nil, domain.ErrWriteDenied
		}
		files[entry.Target] = approved.Clone()
	}
	expected, err := gitExpectedTree(flat)
	if err != nil || expected != b.ExpectedTree {
		return nil, domain.ErrWriteDenied
	}
	return files, nil
}

// Planning is read-only: compute the expected tree in memory from parent Git
// objects plus the immutable APPLIED journal; no staging or object creation.
func (s *ManagedWorkspaceStore) PlanGitCommit(ctx context.Context, w ManagedWorkspace, r domain.ManagedRepositoryIdentity, writeID, branchID string, contextBinding domain.GitBinding, message string, author, committer domain.GitActor) (domain.GitBinding, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var result domain.GitBinding
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		root, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		defer root.Close()
		if db.Workspaces[w.WorkspaceID].Lease != nil {
			return ErrManagedWriteBlocked
		}
		repo, err := s.openRepository(db, w, r)
		if err != nil {
			return err
		}
		defer repo.Close()
		branch, ok := db.GitOperations[branchID]
		if !ok || branch.Binding != contextBinding || branch.State != "APPLIED" {
			return domain.ErrWriteDenied
		}
		write, ok := db.Journals[writeID]
		if !ok || write.State != string(domain.WriteApplied) {
			return domain.ErrWriteDenied
		}
		parent, err := gitRef(ctx, root, repo, contextBinding.BranchName)
		if err != nil || parent == "" {
			return domain.ErrWriteDenied
		}
		flat, err := managedGitTree(ctx, root, repo, parent)
		if err != nil {
			return err
		}
		for _, entry := range write.Candidate.Manifest.Entries {
			post := write.Postimages[entry.Target]
			flat[entry.Target] = gitTreeFile{gitMode(entry.AfterMode), gitObjectID("blob", post.Content)}
		}
		tree, err := gitExpectedTree(flat)
		if err != nil {
			return err
		}
		identity, _ := write.Candidate.Identity()
		b := contextBinding
		b.OperationKind = domain.GitCommit
		b.StartPoint = ""
		b.Parent = parent
		b.ExpectedTree = tree
		b.AppliedWriteTransactionID = writeID
		b.CandidateIdentity = identity
		b.BranchOperationID = branchID
		b.Message = message
		b.MessageIdentity = domain.CandidateDigest([]byte(message))
		b.Author = author
		b.Committer = committer
		b.OperationParametersIdentity = b.ParametersIdentity()
		if !validateGitBindingWorkspace(b, w, r) {
			return domain.ErrWriteDenied
		}
		if _, err = s.gitCommitPlan(ctx, db, root, repo, w, r, b); err != nil {
			return err
		}
		result = b
		return nil
	})
	return result, err
}

func (s *ManagedWorkspaceStore) MutateGit(ctx context.Context, w ManagedWorkspace, r domain.ManagedRepositoryIdentity, operationID, approvalID string, b domain.GitBinding) (GitMutationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var result GitMutationResult
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		if !validateGitBindingWorkspace(b, w, r) || b.BranchName == "main" || (domain.CandidateContext{ProjectID: w.ProjectID, TaskID: "operation", CorrelationID: operationID}).Validate() != nil {
			return domain.ErrWriteDenied
		}
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
		a, ok := db.GitApprovals[approvalID]
		if !ok || a.Approval.Binding != b {
			return domain.ErrWriteDenied
		}
		if previous, ok := db.GitOperations[operationID]; ok {
			if previous.ApprovalID != approvalID || previous.Binding != b || previous.Workspace != w || previous.Repository != r {
				return domain.ErrWriteDenied
			}
			if previous.State == "APPLIED" && gitExactState(ctx, root, repo, previous, db) {
				result = previous.Result
				return nil
			}
			if previous.State == "ABORTED" {
				result = previous.Result
				return nil
			}
			return ErrManagedWriteBlocked
		}
		if a.State != "AVAILABLE" || !a.Approval.ValidAt(time.Now().UTC()) {
			return domain.ErrWriteDenied
		}
		if db.Workspaces[w.WorkspaceID].Lease != nil {
			return ErrManagedWriteBlocked
		}
		j := gitOperationRecord{OperationID: operationID, ApprovalID: approvalID, Kind: string(b.OperationKind), Workspace: w, Repository: r, Binding: b, State: "PREPARED"}
		if b.OperationKind == domain.GitCommit {
			j.ExpectedTree = b.ExpectedTree
			j.CommitBytes = gitCommitBytes(b.ExpectedTree, b.Parent, b.Message, b.Author, b.Committer)
			j.ExpectedCommit = gitObjectID("commit", j.CommitBytes)
			j.IndexName, err = gitIndexName()
			if err != nil {
				return err
			}
		}
		a.State = "RESERVED"
		a.OperationID = operationID
		a.ReservedAt = time.Now().UTC()
		db.GitApprovals[approvalID] = a
		if err = s.gitAcquire(db, j); err != nil {
			return err
		}
		if err = s.checkpoint("git-lease", 0); err != nil {
			return err
		}
		pinned, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		pinned.Close()
		verified, err := s.openRepository(db, w, r)
		if err != nil {
			return err
		}
		verified.Close()
		deny := func() error {
			result = GitMutationResult{State: "ABORTED", BranchName: b.BranchName}
			if err := s.gitFinish(db, j, result); err != nil {
				return err
			}
			return domain.ErrWriteDenied
		}
		head, err := gitReadOID(ctx, root, repo, "HEAD")
		if err != nil || head != b.ExpectedHead {
			return deny()
		}
		old, err := gitRef(ctx, root, repo, b.BranchName)
		if err != nil {
			return deny()
		}
		var files map[string]domain.CandidateFile
		if b.OperationKind == domain.GitBranch {
			applied := false
			for _, write := range db.Journals {
				if write.State == string(domain.WriteApplied) && write.Workspace == w && write.Candidate.ProjectID == b.ProjectID && write.Candidate.TaskID == b.TaskID && write.Candidate.CorrelationID == b.CorrelationID {
					applied = true
				}
			}
			if !applied {
				return deny()
			}
			if _, err := managedGitTree(ctx, root, repo, b.StartPoint); err != nil {
				return deny()
			}
			if old != "" && old != b.StartPoint {
				return deny()
			}
		} else {
			if old != b.Parent {
				return deny()
			}
			files, err = s.gitCommitPlan(ctx, db, root, repo, w, r, b)
			if err != nil {
				return deny()
			}
		}
		j.State = "APPLYING"
		db.GitOperations[operationID] = j
		if err = s.save(db); err != nil {
			return err
		}
		if err = s.checkpoint("git-prepared", 0); err != nil {
			return err
		}
		if b.OperationKind == domain.GitBranch {
			j.ExpectedCommit = b.StartPoint
			if old == "" {
				if _, err = managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "update-ref", "--no-deref", "refs/heads/"+b.BranchName, b.StartPoint, gitZeroOID); err != nil {
					return gitUnknownError(err)
				}
			}
		} else {
			tree, err := gitBuildTree(ctx, root, repo, j.IndexName, b.Parent, files)
			if err != nil || tree != b.ExpectedTree {
				return ErrManagedWriteBlocked
			}
			out, err := managedGit(ctx, root, repo, "", []byte(b.Message), b.Author, b.Committer, "commit-tree", "--no-gpg-sign", tree, "-p", b.Parent)
			if err != nil || strings.TrimSpace(string(out)) != j.ExpectedCommit {
				return ErrManagedWriteBlocked
			}
			if err = s.checkpoint("git-object", 0); err != nil {
				return err
			}
			if !gitVerifiedCommit(ctx, root, repo, j, db) {
				return ErrManagedWriteBlocked
			}
			if _, err = managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "update-ref", "--no-deref", "refs/heads/"+b.BranchName, j.ExpectedCommit, b.Parent); err != nil {
				return gitUnknownError(err)
			}
		}
		if err = s.checkpoint("git-ref", 0); err != nil {
			return err
		}
		if !gitExactState(ctx, root, repo, j, db) || gitDurable(repo) != nil {
			return ErrManagedWriteBlocked
		}
		oid := j.ExpectedCommit
		result = GitMutationResult{State: "APPLIED", OID: oid, Tree: j.ExpectedTree, BranchName: b.BranchName}
		return s.gitFinish(db, j, result)
	})
	return result, err
}
