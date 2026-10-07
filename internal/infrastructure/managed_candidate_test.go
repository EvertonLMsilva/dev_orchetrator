//go:build linux

package infrastructure

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"encoding/base64"
	"encoding/json"
	"os"
	"testing"
	"time"
)

type managedGenerator struct{}

func (managedGenerator) ProveToolFree(context.Context) error { return nil }
func (managedGenerator) Generate(_ context.Context, r ports.CandidateGenerationRequest) ([]byte, error) {
	return json.Marshal(domain.StructuredProposal{SchemaVersion: 1, Edits: []domain.StructuredEdit{{Operation: domain.WriteReplace, Target: "old.txt", ExpectedPreimageIdentity: r.Inputs[0].Identity, PostimageContent: base64.StdEncoding.EncodeToString([]byte("after")), PostimageIdentity: domain.CandidateDigest([]byte("after"))}}})
}
func TestManagedCandidateActorWriteGit(t *testing.T) {
	ctx := context.Background()
	control := t.TempDir()
	os.Chmod(control, 0700)
	s, err := NewManagedWorkspaceStore(control, "instance")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.Provision(ctx, "project", map[string][]byte{"old.txt": []byte("before")})
	if err != nil {
		t.Fatal(err)
	}
	policy := mediatedPolicy()
	repo, err := s.ProvisionRepository(ctx, w, policy, "v1")
	if err != nil {
		t.Fatal(err)
	}
	factory := NewManagedCandidateWorkspaceFactory(s, w, t.TempDir())
	a, err := application.NewCandidatePipeline(managedGenerator{}, factory).Generate(ctx, application.CandidateRequest{Context: domain.CandidateContext{ProjectID: "project", TaskID: "task", CorrelationID: "corr"}, Objective: "replace old.txt", WorkspaceIdentity: w.Identity(), Policy: policy, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if a.Candidate().WorkspaceIdentity != w.Identity() {
		t.Fatal("workspace binding")
	}
	e := ports.ActorEvidence{Provider: "discord", ExternalID: "123"}
	p := ports.Principal{ID: "operator"}
	grants := []ports.Grant{}
	for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
		grants = append(grants, ports.Grant{PrincipalID: p.ID, ProjectID: "project", Operation: op})
	}
	auth, err := NewActorAuthority([]ActorMapping{{e, p}}, grants)
	if err != nil {
		t.Fatal(err)
	}
	issuer := application.NewApprovalIssuer(auth, auth, s, time.Now)
	binding, err := a.Candidate().Binding("v1", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	request := MediatedWriteRequest{Workspace: w, Policy: policy, Artifact: a, Context: binding, ApprovalID: "write-approval", TransactionID: "write-tx"}
	if _, err := issuer.IssueWrite(ctx, e, "expired", binding, binding, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("expired issuance")
	}
	if _, found, err := s.Find(ctx, "expired"); err != nil || found {
		t.Fatal("expired approval persisted")
	}
	if _, err := s.Apply(ctx, request); err == nil {
		t.Fatal("effect without approval")
	}
	bad := binding.Clone()
	bad.TaskID = "other"
	if _, err := issuer.IssueWrite(ctx, e, "bad", binding, bad, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("confirmation divergence")
	}
	wa, err := issuer.IssueWrite(ctx, e, request.ApprovalID, binding, binding, time.Now().Add(time.Minute))
	if err != nil || wa.ApproverIdentity != p.ID {
		t.Fatal(wa, err)
	}
	if _, err := application.NewWriteAuthorizationService(s, time.Now).Reserve(ctx, application.WriteAuthorizationRequest{ApprovalID: request.ApprovalID, TransactionID: request.TransactionID, Candidate: a.Candidate(), Context: binding}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Apply(ctx, request)
	if err != nil || result.State != domain.WriteApplied {
		t.Fatal(result, err)
	}
	b := domain.GitBinding{OperationKind: domain.GitBranch, ProjectID: "project", TaskID: "task", CorrelationID: "corr", WorkspaceIdentity: w.Identity(), RepositoryIdentity: repo.Identity(), ExpectedHead: repo.InitialHead, StartPoint: repo.InitialHead, BranchName: "codex/task", PolicyVersion: "v1"}
	b.OperationParametersIdentity = b.ParametersIdentity()
	if _, err := s.MutateGit(ctx, w, repo, "branch-op", "branch-approval", b); err == nil {
		t.Fatal("branch without approval")
	}
	ga, err := issuer.IssueGit(ctx, e, "branch-approval", b, b, time.Now().Add(time.Minute))
	if err != nil || ga.ApproverIdentity != p.ID {
		t.Fatal(ga, err)
	}
	branch, err := s.MutateGit(ctx, w, repo, "branch-op", ga.ApprovalID, b)
	if err != nil || branch.State != "APPLIED" {
		t.Fatal(branch, err)
	}
	actor := domain.GitActor{Name: "Operator", Email: "operator@local.invalid", UnixSeconds: time.Now().Unix()}
	cb, err := s.PlanGitCommit(ctx, w, repo, "write-tx", "branch-op", b, "pilot change\n", actor, actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MutateGit(ctx, w, repo, "commit-op", "commit-approval", cb); err == nil {
		t.Fatal("commit without approval")
	}
	if _, err := issuer.IssueGit(ctx, e, "commit-approval", cb, cb, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	commit, err := s.MutateGit(ctx, w, repo, "commit-op", "commit-approval", cb)
	if err != nil || commit.State != "APPLIED" {
		t.Fatal(commit, err)
	}
	if !domain.GitOID(commit.OID) || commit.Tree != cb.ExpectedTree {
		t.Fatal("commit receipt")
	}
	effects := s.effects
	if replay, err := s.Apply(ctx, request); err != nil || replay != result || s.effects != effects {
		t.Fatal("write replay", err)
	}
	if _, err := issuer.IssueWrite(ctx, e, request.ApprovalID, binding, binding, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("issuance replay")
	}
}
