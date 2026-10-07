package domain

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"
)

type ManagedRepositoryIdentity struct {
	RepositoryID        string
	WorkspaceID         string
	WorkspaceGeneration uint64
	GitDirIdentity      string
	InitialHead         string
	InitialTree         string
	InitialBaseIdentity string
	PolicyVersion       string
}

func (r ManagedRepositoryIdentity) Identity() string {
	b, _ := json.Marshal(r)
	return CandidateDigest(b)
}
func GitOID(s string) bool {
	if len(s) != 40 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
func (r ManagedRepositoryIdentity) Validate() error {
	if !writeLabel(r.RepositoryID) || !writeLabel(r.WorkspaceID) || r.WorkspaceGeneration == 0 || !validWriteDigest(r.GitDirIdentity) || !validWriteDigest(r.InitialBaseIdentity) || !GitOID(r.InitialHead) || !GitOID(r.InitialTree) || !writeLabel(r.PolicyVersion) {
		return ErrWriteDenied
	}
	return nil
}

type GitOperationKind string

const (
	GitBranch GitOperationKind = "GIT_BRANCH"
	GitCommit GitOperationKind = "GIT_COMMIT"
)

type GitActor struct {
	Name        string
	Email       string
	UnixSeconds int64
}

func (a GitActor) Validate() bool {
	return writeLabel(a.Name) && writeLabel(a.Email) && !strings.ContainsAny(a.Name, "<>") && !strings.ContainsAny(a.Email, " <>\t") && strings.Contains(a.Email, "@") && a.UnixSeconds > 0 && a.UnixSeconds < 253402300800
}
func GitBranchName(s string) bool {
	if !validWriteTarget(s) || strings.Contains(s, "..") || strings.Contains(s, "@{") || strings.HasSuffix(s, ".lock") {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "-") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

type GitBinding struct {
	OperationKind               GitOperationKind
	ProjectID                   ProjectID
	TaskID                      TaskID
	CorrelationID               string
	WorkspaceIdentity           string
	RepositoryIdentity          string
	ExpectedHead                string
	BranchName                  string
	StartPoint                  string
	Parent                      string
	ExpectedTree                string
	AppliedWriteTransactionID   string
	CandidateIdentity           string
	BranchOperationID           string
	Author                      GitActor
	Committer                   GitActor
	Message                     string
	MessageIdentity             string
	OperationParametersIdentity string
	PolicyVersion               string
}

func (b GitBinding) ParametersIdentity() string {
	b.OperationParametersIdentity = ""
	data, _ := json.Marshal(b)
	return CandidateDigest(data)
}
func (b GitBinding) Identity() string { data, _ := json.Marshal(b); return CandidateDigest(data) }
func (b GitBinding) Validate() error {
	for _, s := range []string{string(b.ProjectID), string(b.TaskID), b.CorrelationID, b.WorkspaceIdentity, b.PolicyVersion} {
		if !writeLabel(s) {
			return ErrWriteDenied
		}
	}
	if !validWriteDigest(b.RepositoryIdentity) || !GitOID(b.ExpectedHead) || !GitBranchName(b.BranchName) || b.OperationParametersIdentity != b.ParametersIdentity() {
		return ErrWriteDenied
	}
	switch b.OperationKind {
	case GitBranch:
		if b.StartPoint != b.ExpectedHead || b.Parent != "" || b.ExpectedTree != "" || b.AppliedWriteTransactionID != "" || b.CandidateIdentity != "" || b.BranchOperationID != "" || b.Author != (GitActor{}) || b.Committer != (GitActor{}) || b.Message != "" || b.MessageIdentity != "" {
			return ErrWriteDenied
		}
	case GitCommit:
		if b.StartPoint != "" || !GitOID(b.Parent) || !GitOID(b.ExpectedTree) || !writeLabel(b.AppliedWriteTransactionID) || !validWriteDigest(b.CandidateIdentity) || !writeLabel(b.BranchOperationID) || !b.Author.Validate() || !b.Committer.Validate() || len(b.Message) == 0 || len(b.Message) > 4096 || !utf8.ValidString(b.Message) || strings.ContainsRune(b.Message, 0) || !strings.HasSuffix(b.Message, "\n") || b.MessageIdentity != CandidateDigest([]byte(b.Message)) {
			return ErrWriteDenied
		}
	default:
		return ErrWriteDenied
	}
	return nil
}

type GitApproval struct {
	ApprovalID       string
	Binding          GitBinding
	ApproverIdentity string
	IssuedAt         time.Time
	ExpiresAt        time.Time
}

func (a GitApproval) Validate() error {
	if !writeLabel(a.ApprovalID) || !writeLabel(a.ApproverIdentity) || a.Binding.Validate() != nil || a.IssuedAt.IsZero() || !a.ExpiresAt.After(a.IssuedAt) {
		return ErrWriteDenied
	}
	return nil
}
func (a GitApproval) ValidAt(t time.Time) bool {
	return a.Validate() == nil && !t.Before(a.IssuedAt) && t.Before(a.ExpiresAt)
}
