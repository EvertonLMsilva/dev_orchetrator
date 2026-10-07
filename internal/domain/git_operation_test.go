package domain

import (
	"strings"
	"testing"
	"time"
)

func TestGitOperationGatesStrict(t *testing.T) {
	b := GitBinding{OperationKind: GitBranch, ProjectID: "project", TaskID: "task", CorrelationID: "corr", WorkspaceIdentity: "workspace", RepositoryIdentity: CandidateDigest(nil), ExpectedHead: strings.Repeat("a", 40), StartPoint: strings.Repeat("a", 40), BranchName: "codex/task", PolicyVersion: "v1"}
	b.OperationParametersIdentity = b.ParametersIdentity()
	if b.Validate() != nil {
		t.Fatal("valid branch denied")
	}
	for _, name := range []string{"-option", "../escape", "a..b", "a/.git", "a.lock", "a@{x", "a b", "a;touch", "a/--flag", "/absolute"} {
		bad := b
		bad.BranchName = name
		bad.OperationParametersIdentity = bad.ParametersIdentity()
		if bad.Validate() == nil {
			t.Fatal("branch accepted", name)
		}
	}
	for _, kind := range []GitOperationKind{"WRITE_APPLY", "GIT_PUSH", "GIT_PR", "GIT_MERGE", ""} {
		bad := b
		bad.OperationKind = kind
		bad.OperationParametersIdentity = bad.ParametersIdentity()
		if bad.Validate() == nil {
			t.Fatal("kind accepted", kind)
		}
	}
	bad := b
	bad.StartPoint = strings.Repeat("b", 40)
	bad.OperationParametersIdentity = bad.ParametersIdentity()
	if bad.Validate() == nil {
		t.Fatal("startpoint mismatch")
	}
	now := time.Now().UTC()
	a := GitApproval{ApprovalID: "approval", Binding: b, ApproverIdentity: "trusted", IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	if !a.ValidAt(now) || a.ValidAt(now.Add(time.Minute)) || a.ValidAt(now.Add(-time.Second)) {
		t.Fatal("expiry window")
	}
	actor := GitActor{Name: "Trusted Author", Email: "trusted@example.invalid", UnixSeconds: 1700000000}
	b.OperationKind = GitCommit
	b.StartPoint = ""
	b.Parent = strings.Repeat("a", 40)
	b.ExpectedTree = strings.Repeat("b", 40)
	b.AppliedWriteTransactionID = "write"
	b.CandidateIdentity = CandidateDigest(nil)
	b.BranchOperationID = "branch"
	b.Author = actor
	b.Committer = actor
	b.Message = "approved\n"
	b.MessageIdentity = CandidateDigest([]byte(b.Message))
	b.OperationParametersIdentity = b.ParametersIdentity()
	if b.Validate() != nil {
		t.Fatal("valid commit denied")
	}
	bad = b
	bad.Message = "changed\n"
	bad.OperationParametersIdentity = bad.ParametersIdentity()
	if bad.Validate() == nil {
		t.Fatal("message mismatch")
	}
	bad = b
	bad.Author.Name = "evil\nactor"
	bad.OperationParametersIdentity = bad.ParametersIdentity()
	if bad.Validate() == nil {
		t.Fatal("actor injection")
	}
	bad = b
	bad.OperationParametersIdentity = CandidateDigest(nil)
	if bad.Validate() == nil {
		t.Fatal("parameters mismatch")
	}
}
