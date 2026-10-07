package application

import (
	"context"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"time"
)

type ContextualApprovalStore interface {
	Issue(context.Context, domain.WriteApproval) error
	IssueGitApproval(context.Context, domain.GitApproval) error
}

// ApprovalIssuer is called only by the trusted application after an explicit
// confirmation of the displayed binding. Neither model nor Channel gets this API.
// It persists approval only; it cannot execute WRITE or Git.
type ApprovalIssuer struct {
	auth   ports.ActorAuthenticationPort
	grants ports.ActorGrantRepository
	store  ContextualApprovalStore
	now    func() time.Time
}

func NewApprovalIssuer(a ports.ActorAuthenticationPort, g ports.ActorGrantRepository, s ContextualApprovalStore, now func() time.Time) *ApprovalIssuer {
	return &ApprovalIssuer{a, g, s, now}
}
func (s *ApprovalIssuer) principal(ctx context.Context, e ports.ActorEvidence, project domain.ProjectID, op string) (ports.Principal, error) {
	if s == nil || s.auth == nil || s.grants == nil || s.store == nil || s.now == nil {
		return ports.Principal{}, domain.ErrWriteDenied
	}
	p, err := s.auth.AuthenticateActor(ctx, e)
	if err != nil {
		return ports.Principal{}, err
	}
	if p.ID == "" {
		return ports.Principal{}, domain.ErrWriteDenied
	}
	q := ports.Grant{PrincipalID: p.ID, ProjectID: string(project), Operation: op}
	actual, found, err := s.grants.FindActorGrant(ctx, q)
	if err != nil {
		return ports.Principal{}, err
	}
	if !found || actual != q {
		return ports.Principal{}, domain.ErrWriteDenied
	}
	return p, nil
}
func (s *ApprovalIssuer) IssueWrite(ctx context.Context, e ports.ActorEvidence, id domain.ApprovalID, b domain.WriteBinding, confirmed domain.WriteBinding, expires time.Time) (domain.WriteApproval, error) {
	if b.Validate() != nil || !b.Equal(confirmed) {
		return domain.WriteApproval{}, domain.ErrWriteDenied
	}
	p, err := s.principal(ctx, e, b.ProjectID, string(b.OperationKind))
	if err != nil {
		return domain.WriteApproval{}, err
	}
	a := domain.WriteApproval{SchemaVersion: domain.WriteSchemaVersion, ApprovalID: id, WriteBinding: b.Clone(), ApproverIdentity: p.ID, IssuedAt: s.now(), ExpiresAt: expires}
	if !a.ValidAt(a.IssuedAt) {
		return domain.WriteApproval{}, domain.ErrWriteDenied
	}
	if err := s.store.Issue(ctx, a); err != nil {
		return domain.WriteApproval{}, err
	}
	return a, nil
}
func (s *ApprovalIssuer) IssueGit(ctx context.Context, e ports.ActorEvidence, id string, b domain.GitBinding, confirmed domain.GitBinding, expires time.Time) (domain.GitApproval, error) {
	if b.Validate() != nil || b != confirmed {
		return domain.GitApproval{}, domain.ErrWriteDenied
	}
	p, err := s.principal(ctx, e, b.ProjectID, string(b.OperationKind))
	if err != nil {
		return domain.GitApproval{}, err
	}
	a := domain.GitApproval{ApprovalID: id, Binding: b, ApproverIdentity: p.ID, IssuedAt: s.now(), ExpiresAt: expires}
	if !a.ValidAt(a.IssuedAt) {
		return domain.GitApproval{}, domain.ErrWriteDenied
	}
	if err := s.store.IssueGitApproval(ctx, a); err != nil {
		return domain.GitApproval{}, err
	}
	return a, nil
}
