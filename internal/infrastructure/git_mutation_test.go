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

func TestControlledGitBaselineBranchCommit(t *testing.T) {
	ctx := context.Background()
	s, w, q := managedFixture(t)
	repo, err := s.ProvisionRepository(ctx, w, q.Policy, "policy-v1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Join(s.rootPath(w), ".git")); !os.IsNotExist(err) {
		t.Fatal("content Git metadata")
	}
	if _, err = s.Apply(ctx, q); err != nil {
		t.Fatal(err)
	}
	b := domain.GitBinding{OperationKind: domain.GitBranch, ProjectID: w.ProjectID, TaskID: "task", CorrelationID: "corr", WorkspaceIdentity: w.Identity(), RepositoryIdentity: repo.Identity(), ExpectedHead: repo.InitialHead, BranchName: "codex/task", StartPoint: repo.InitialHead, PolicyVersion: "policy-v1"}
	b.OperationParametersIdentity = b.ParametersIdentity()
	issueGit(t, s, b, "branch-approval")
	branch, err := s.MutateGit(ctx, w, repo, "branch-op", "branch-approval", b)
	if err != nil || branch.State != "APPLIED" {
		t.Fatalf("branch: %+v %v", branch, err)
	}
	actor := domain.GitActor{Name: "Test Author", Email: "test@example.invalid", UnixSeconds: 1700000000}
	commitBinding, err := s.PlanGitCommit(ctx, w, repo, q.TransactionID, "branch-op", b, "approved message\n", actor, actor)
	if err != nil {
		t.Fatal(err)
	}
	issueGit(t, s, commitBinding, "commit-approval")
	result, err := s.MutateGit(ctx, w, repo, "commit-op", "commit-approval", commitBinding)
	if err != nil || result.State != "APPLIED" || result.Tree != commitBinding.ExpectedTree {
		t.Fatalf("commit: %+v %v", result, err)
	}
	replay, err := s.MutateGit(ctx, w, repo, "commit-op", "commit-approval", commitBinding)
	if err != nil || replay != result {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	assertGitCommit(t, s, w, repo, result, commitBinding)
}
func issueGit(t *testing.T, s *ManagedWorkspaceStore, b domain.GitBinding, id string) {
	t.Helper()
	now := time.Now().UTC()
	err := s.IssueGitApproval(context.Background(), domain.GitApproval{ApprovalID: id, Binding: b, ApproverIdentity: "trusted-test", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
}
func assertGitCommit(t *testing.T, s *ManagedWorkspaceStore, w ManagedWorkspace, r domain.ManagedRepositoryIdentity, result GitMutationResult, b domain.GitBinding) {
	t.Helper()
	err := s.exclusive(context.Background(), func(db *managedDatabase) error {
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
		commit, err := managedGit(context.Background(), root, repo, "", nil, domain.GitActor{}, domain.GitActor{}, "cat-file", "commit", result.OID)
		if err != nil {
			return err
		}
		want := gitCommitBytes(b.ExpectedTree, b.Parent, b.Message, b.Author, b.Committer)
		if string(commit) != string(want) {
			t.Fatalf("commit bytes mismatch")
		}
		tree, err := managedGitTree(context.Background(), root, repo, result.OID)
		if err != nil {
			return err
		}
		if tree["old.txt"].OID != gitObjectID("blob", []byte("after")) || tree["new.txt"].OID != gitObjectID("blob", []byte("created")) {
			t.Fatal("commit tree content mismatch")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
