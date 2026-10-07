//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/domain"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type GitMutationResult struct {
	State      string
	OID        string
	Tree       string
	BranchName string
}
type gitApprovalRecord struct {
	Approval    domain.GitApproval
	State       string
	OperationID string
	ReservedAt  time.Time
}
type gitOperationRecord struct {
	OperationID    string
	ApprovalID     string
	Kind           string
	Workspace      ManagedWorkspace
	Repository     domain.ManagedRepositoryIdentity
	Binding        domain.GitBinding
	Policy         domain.CandidatePolicy
	State          string
	ExpectedCommit string
	ExpectedTree   string
	CommitBytes    []byte
	IndexName      string
	Result         GitMutationResult
}

func gitMaps(db *managedDatabase) {
	if db.Repositories == nil {
		db.Repositories = map[string]domain.ManagedRepositoryIdentity{}
	}
	if db.GitApprovals == nil {
		db.GitApprovals = map[string]gitApprovalRecord{}
	}
	if db.GitOperations == nil {
		db.GitOperations = map[string]gitOperationRecord{}
	}
}
func gitRepoName(w ManagedWorkspace) string {
	return "git-" + w.WorkspaceID + "-" + fmtGeneration(w.Generation) + ".git"
}
func validateGitDatabase(db managedDatabase) error {
	for id, r := range db.Repositories {
		w, ok := db.Workspaces[id]
		if !ok || r.Validate() != nil || r.WorkspaceID != id || r.RepositoryID != gitRepoName(w.Workspace) || r.WorkspaceGeneration != w.Workspace.Generation {
			return ErrManagedWriteBlocked
		}
	}
	for id, a := range db.GitApprovals {
		if id != a.Approval.ApprovalID || a.Approval.Validate() != nil {
			return ErrManagedWriteBlocked
		}
		switch a.State {
		case "AVAILABLE":
			if a.OperationID != "" || !a.ReservedAt.IsZero() {
				return ErrManagedWriteBlocked
			}
		case "RESERVED", "APPLIED", "ABORTED", "RECOVERY_REQUIRED":
			op, ok := db.GitOperations[a.OperationID]
			if !ok || op.ApprovalID != id || op.Binding != a.Approval.Binding || !a.Approval.ValidAt(a.ReservedAt) {
				return ErrManagedWriteBlocked
			}
		default:
			return ErrManagedWriteBlocked
		}
	}
	for id, j := range db.GitOperations {
		w, ok := db.Workspaces[j.Workspace.WorkspaceID]
		if id != j.OperationID || !ok || w.Workspace != j.Workspace || j.Repository.RepositoryID != gitRepoName(j.Workspace) || j.Repository.WorkspaceID != j.Workspace.WorkspaceID || j.Repository.WorkspaceGeneration != j.Workspace.Generation {
			return ErrManagedWriteBlocked
		}
		if j.Kind != "GIT_PROVISION" {
			a, ok := db.GitApprovals[j.ApprovalID]
			if !ok || a.OperationID != id || j.Binding != a.Approval.Binding || j.Binding.Validate() != nil || j.Kind != string(j.Binding.OperationKind) || j.Repository.Validate() != nil || j.Repository != db.Repositories[j.Workspace.WorkspaceID] {
				return ErrManagedWriteBlocked
			}
		}
		if j.Kind == "GIT_PROVISION" {
			if j.Policy.Validate() != nil || j.ApprovalID != "" || j.Binding != (domain.GitBinding{}) {
				return ErrManagedWriteBlocked
			}
			if j.State != "PREPARED" && (j.Repository.Validate() != nil || j.Repository.InitialTree != j.ExpectedTree || j.Repository.InitialHead != j.ExpectedCommit) {
				return ErrManagedWriteBlocked
			}
		}
		if j.Kind == string(domain.GitCommit) && (j.ExpectedTree != j.Binding.ExpectedTree || string(j.CommitBytes) != string(gitCommitBytes(j.Binding.ExpectedTree, j.Binding.Parent, j.Binding.Message, j.Binding.Author, j.Binding.Committer)) || j.ExpectedCommit != gitObjectID("commit", j.CommitBytes)) {
			return ErrManagedWriteBlocked
		}
		if j.Kind == string(domain.GitBranch) && j.ExpectedCommit != "" && j.ExpectedCommit != j.Binding.StartPoint {
			return ErrManagedWriteBlocked
		}
		if j.ExpectedCommit != "" && j.Kind != string(domain.GitBranch) && (!domain.GitOID(j.ExpectedTree) || gitObjectID("commit", j.CommitBytes) != j.ExpectedCommit) {
			return ErrManagedWriteBlocked
		}
		switch j.State {
		case "PREPARED", "APPLYING", "RECOVERY_REQUIRED":
			if w.Lease == nil || w.Lease.TransactionID != id || w.Lease.Kind != j.Kind {
				return ErrManagedWriteBlocked
			}
		case "APPLIED", "ABORTED":
			if j.Result.State != j.State {
				return ErrManagedWriteBlocked
			}
			if j.State == "APPLIED" {
				expected := j.ExpectedCommit
				if j.Kind == string(domain.GitBranch) {
					expected = j.Binding.StartPoint
				}
				if j.Result.OID != expected || j.Result.Tree != j.ExpectedTree || j.Result.BranchName != j.Binding.BranchName {
					return ErrManagedWriteBlocked
				}
			}
		default:
			return ErrManagedWriteBlocked
		}
		if j.Kind != "GIT_PROVISION" {
			a := db.GitApprovals[j.ApprovalID]
			want := j.State
			if want == "PREPARED" || want == "APPLYING" {
				want = "RESERVED"
			}
			if a.State != want {
				return ErrManagedWriteBlocked
			}
		}
	}
	for _, w := range db.Workspaces {
		if w.Lease != nil && w.Lease.Kind != "" {
			j, ok := db.GitOperations[w.Lease.TransactionID]
			if !ok || j.Kind != w.Lease.Kind || j.Workspace != w.Workspace {
				return ErrManagedWriteBlocked
			}
		}
	}
	return nil
}
func (s *ManagedWorkspaceStore) gitAcquire(db *managedDatabase, j gitOperationRecord) error {
	w := db.Workspaces[j.Workspace.WorkspaceID]
	if w.Lease != nil {
		return ErrManagedWriteBlocked
	}
	w.Lease = &managedLease{Kind: j.Kind, WorkspaceID: j.Workspace.WorkspaceID, Generation: j.Workspace.Generation, TransactionID: j.OperationID, OwnerInstanceID: s.instance, AcquiredAt: time.Now().UTC(), State: "HELD", Version: 1}
	db.Workspaces[j.Workspace.WorkspaceID] = w
	db.GitOperations[j.OperationID] = j
	return s.save(db)
}
func (s *ManagedWorkspaceStore) gitFinish(db *managedDatabase, j gitOperationRecord, result GitMutationResult) error {
	j.State = result.State
	j.Result = result
	db.GitOperations[j.OperationID] = j
	if j.Kind != "GIT_PROVISION" {
		a := db.GitApprovals[j.ApprovalID]
		a.State = result.State
		db.GitApprovals[j.ApprovalID] = a
	}
	if err := s.save(db); err != nil {
		return err
	}
	if err := s.checkpoint("git-terminal", 0); err != nil {
		return err
	}
	if result.State != "RECOVERY_REQUIRED" {
		w := db.Workspaces[j.Workspace.WorkspaceID]
		w.Lease = nil
		db.Workspaces[j.Workspace.WorkspaceID] = w
		return s.save(db)
	}
	return nil
}
func (s *ManagedWorkspaceStore) IssueGitApproval(ctx context.Context, a domain.GitApproval) error {
	if a.Validate() != nil {
		return domain.ErrWriteDenied
	}
	return s.exclusive(ctx, func(db *managedDatabase) error {
		gitMaps(db)
		if _, ok := db.GitApprovals[a.ApprovalID]; ok {
			return domain.ErrWriteDenied
		}
		db.GitApprovals[a.ApprovalID] = gitApprovalRecord{Approval: a, State: "AVAILABLE"}
		return s.save(db)
	})
}
func gitDurable(repo *os.File) error {
	var walk func(int, int) error
	count := 0
	walk = func(fd, depth int) error {
		if depth > 16 {
			return ErrManagedWriteBlocked
		}
		dir, err := candidateDirectory(fd, ".")
		if err != nil {
			return err
		}
		defer dir.Close()
		entries, err := dir.ReadDir(8193)
		if err != nil && err != io.EOF {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 8192 {
				return ErrManagedWriteBlocked
			}
			if entry.IsDir() {
				child, err := candidateDirectory(fd, entry.Name())
				if err != nil {
					return err
				}
				err = walk(int(child.Fd()), depth+1)
				child.Close()
				if err != nil {
					return err
				}
			} else {
				f, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
				if err != nil {
					return err
				}
				var st unix.Stat_t
				err = unix.Fstat(f, &st)
				if err == nil && (st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1) {
					err = ErrManagedWriteBlocked
				}
				if err == nil {
					err = unix.Fsync(f)
				}
				unix.Close(f)
				if err != nil {
					return err
				}
			}
		}
		return dir.Sync()
	}
	return walk(int(repo.Fd()), 0)
}
func gitVerifiedCommit(ctx context.Context, root, repo *os.File, j gitOperationRecord, db *managedDatabase) bool {
	bytes, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "commit", j.ExpectedCommit)
	if err != nil || string(bytes) != string(j.CommitBytes) {
		return false
	}
	files, err := managedGitTree(ctx, root, repo, j.ExpectedCommit)
	if err != nil {
		return false
	}
	if j.Kind == string(domain.GitCommit) {
		write, ok := db.Journals[j.Binding.AppliedWriteTransactionID]
		if !ok {
			return false
		}
		identity, _ := write.Candidate.Identity()
		if identity != j.Binding.CandidateIdentity || write.State != string(domain.WriteApplied) {
			return false
		}
		for _, entry := range write.Candidate.Manifest.Entries {
			file, ok := files[entry.Target]
			if !ok || file.Mode != gitMode(entry.AfterMode) {
				return false
			}
			blob, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "blob", file.OID)
			if err != nil || domain.CandidateDigest(blob) != entry.Postimage {
				return false
			}
		}
	} else if j.Kind == "GIT_PROVISION" {
		snapshot, err := managedSnapshot(ctx, int(root.Fd()), j.Policy)
		if err != nil || candidateStateIdentity(j.Policy, snapshot) != j.Repository.InitialBaseIdentity || len(snapshot) != len(files) {
			return false
		}
		for path, file := range snapshot {
			treeFile, ok := files[path]
			if !ok || treeFile.Mode != gitMode(file.Mode) {
				return false
			}
			blob, err := managedGit(ctx, root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "blob", treeFile.OID)
			if err != nil || domain.CandidateDigest(blob) != file.Identity {
				return false
			}
		}
	}
	return true
}
func gitExactState(ctx context.Context, root, repo *os.File, j gitOperationRecord, db *managedDatabase) bool {
	if managedGitMetadata(repo, true) != nil {
		return false
	}
	branch := j.Binding.BranchName
	head := j.Binding.ExpectedHead
	expected := j.ExpectedCommit
	if j.Kind == "GIT_PROVISION" {
		branch = "main"
		head = j.Repository.InitialHead
	}
	if j.Kind == string(domain.GitBranch) {
		expected = j.Binding.StartPoint
	}
	actualHead, err := gitReadOID(ctx, root, repo, "HEAD")
	if err != nil || actualHead != head {
		return false
	}
	ref, err := gitRef(ctx, root, repo, branch)
	if err != nil || ref != expected {
		return false
	}
	if j.Kind != string(domain.GitBranch) && !gitVerifiedCommit(ctx, root, repo, j, db) {
		return false
	}
	return true
}

// Recovery observes only; it never retries a mutation or moves a ref.
func (s *ManagedWorkspaceStore) RecoverGit(ctx context.Context, w ManagedWorkspace, operationID string) (GitMutationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var result GitMutationResult
	err := s.exclusive(ctx, func(db *managedDatabase) error {
		gitMaps(db)
		j, ok := db.GitOperations[operationID]
		if !ok || j.Workspace != w {
			return domain.ErrWriteDenied
		}
		root, err := s.openWorkspace(db, w)
		if err != nil {
			return err
		}
		defer root.Close()
		rec := db.Workspaces[w.WorkspaceID]
		if j.State == "RECOVERY_REQUIRED" {
			return ErrManagedWriteBlocked
		}
		if rec.Lease == nil || rec.Lease.TransactionID != operationID {
			return domain.ErrWriteDenied
		}
		repo, err := candidateDirectory(int(s.control.Fd()), j.Repository.RepositoryID)
		if err != nil {
			return ErrManagedWriteBlocked
		}
		defer repo.Close()
		var st unix.Stat_t
		if unix.Fstat(int(repo.Fd()), &st) != nil || managedRootIdentity(st) != j.Repository.GitDirIdentity {
			return ErrManagedWriteBlocked
		}
		if !gitExactState(ctx, root, repo, j, db) || gitDurable(repo) != nil {
			result = GitMutationResult{State: "RECOVERY_REQUIRED", Tree: j.ExpectedTree, BranchName: j.Binding.BranchName}
			if err = s.gitFinish(db, j, result); err != nil {
				return err
			}
			return ErrManagedWriteBlocked
		}
		if j.Kind == "GIT_PROVISION" {
			snapshot, err := managedSnapshot(ctx, int(root.Fd()), j.Policy)
			if err != nil || candidateStateIdentity(j.Policy, snapshot) != j.Repository.InitialBaseIdentity {
				return ErrManagedWriteBlocked
			}
			db.Repositories[w.WorkspaceID] = j.Repository
		}
		result = GitMutationResult{State: "APPLIED", OID: j.ExpectedCommit, Tree: j.ExpectedTree, BranchName: j.Binding.BranchName}
		if j.Kind == string(domain.GitBranch) {
			result.OID = j.Binding.StartPoint
		}
		return s.gitFinish(db, j, result)
	})
	return result, err
}

func validateGitBindingWorkspace(b domain.GitBinding, w ManagedWorkspace, r domain.ManagedRepositoryIdentity) bool {
	return b.Validate() == nil && b.ProjectID == w.ProjectID && b.WorkspaceIdentity == w.Identity() && b.RepositoryIdentity == r.Identity() && b.PolicyVersion == r.PolicyVersion
}
func gitUnknownError(err error) error {
	if err == nil {
		return nil
	}
	return errors.Join(ErrManagedWriteBlocked, err)
}
func gitIndexName() (string, error) {
	name, err := candidateNewName()
	return "index-" + strings.TrimPrefix(name, "candidate-"), err
}
