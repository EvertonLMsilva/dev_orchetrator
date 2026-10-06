package application

import (
	"context"
	"crypto/sha256"
	"dev-orchestrator/internal/adapters/memory"
	"dev-orchestrator/internal/domain"
	"dev-orchestrator/internal/ports"
	"errors"
	"fmt"
	"testing"
	"time"
)

type writeFaultStore struct {
	ports.WriteApprovalStore
	findErr, errorOnReserve error
	reserveCalls            int
	corruptRecord           bool
}

func (s *writeFaultStore) Find(ctx context.Context, id domain.ApprovalID) (domain.WriteApprovalRecord, bool, error) {
	if s.findErr != nil {
		return domain.WriteApprovalRecord{}, false, s.findErr
	}
	r, found, err := s.WriteApprovalStore.Find(ctx, id)
	if s.corruptRecord {
		r.State = domain.WriteApplied
	}
	return r, found, err
}
func (s *writeFaultStore) Reserve(ctx context.Context, a domain.WriteApproval, tx string, now time.Time) (ports.WriteReservation, error) {
	s.reserveCalls++
	return ports.WriteReservation{}, s.errorOnReserve
}
func TestWriteAuthorizationBoundaryFailures(t *testing.T) {
	_, q, store, now := authorizationFixture(t)
	ctx := context.Background()
	failure := errors.New("store boundary failed")
	for _, fault := range []*writeFaultStore{
		{WriteApprovalStore: store, findErr: failure},
		{WriteApprovalStore: store, errorOnReserve: failure},
		{WriteApprovalStore: store, corruptRecord: true},
	} {
		r, err := NewWriteAuthorizationService(fault, func() time.Time { return *now }).Reserve(ctx, q)
		if err == nil || r.Granted {
			t.Fatal("failure granted", r, err)
		}
		if fault.findErr != nil || fault.corruptRecord {
			if fault.reserveCalls != 0 {
				t.Fatal("invalid read reached reserve")
			}
		}
	}
	for _, s := range []*WriteAuthorizationService{nil, NewWriteAuthorizationService(nil, time.Now), NewWriteAuthorizationService(store, nil)} {
		if r, err := s.Reserve(ctx, q); err == nil || r.Granted {
			t.Fatal("invalid dependencies granted")
		}
	}
	q.ApprovalID = "missing"
	if _, err := NewWriteAuthorizationService(store, func() time.Time { return *now }).Reserve(ctx, q); err != ports.ErrWriteApprovalNotFound {
		t.Fatal(err)
	}
}

func authorizationFixture(t *testing.T) (*WriteAuthorizationService, WriteAuthorizationRequest, *memory.WriteApprovalStore, *time.Time) {
	t.Helper()
	h := func(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	c := domain.WriteCandidate{SchemaVersion: 1, ArtifactID: "artifact", ProjectID: "p", TaskID: "t", CorrelationID: "c", WorkspaceIdentity: "w", BaseIdentity: h("base"), ExpectedPostIdentity: h("post"), OperationParametersIdentity: h("parameters"), Manifest: domain.WriteManifest{SchemaVersion: 1, Entries: []domain.WriteManifestEntry{{Target: "a", Operation: domain.WriteCreate, FileType: "regular", Postimage: h("content"), AfterMode: 0644}}}}
	b, err := c.Binding("policy", "nonce")
	if err != nil {
		t.Fatal(err)
	}
	a := domain.WriteApproval{SchemaVersion: 1, ApprovalID: "approval", WriteBinding: b, ApproverIdentity: "actor", IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
	s := memory.NewWriteApprovalStore()
	if err := s.Issue(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return NewWriteAuthorizationService(s, func() time.Time { return now }), WriteAuthorizationRequest{ApprovalID: a.ApprovalID, TransactionID: "tx", Candidate: c, Context: b}, s, &now
}
func TestWriteAuthorizationReservationAndReplay(t *testing.T) {
	s, q, store, now := authorizationFixture(t)
	ctx := context.Background()
	r, err := s.Reserve(ctx, q)
	if err != nil || !r.Granted {
		t.Fatal(r, err)
	}
	*now = now.Add(time.Hour)
	q.TransactionID = "other"
	r, err = s.Reserve(ctx, q)
	if err != nil || r.Granted || r.Record.TransactionID != "tx" {
		t.Fatal(r, err)
	}
	if _, err := store.Finish(ctx, q.ApprovalID, "tx", domain.WriteTerminalResult{State: domain.WriteApplied, ResultIdentity: q.Context.ExpectedPostIdentity}); err != nil {
		t.Fatal(err)
	}
	r, err = s.Reserve(ctx, q)
	if err != nil || r.Granted || r.Record.State != domain.WriteApplied {
		t.Fatal(r, err)
	}
	q.Context.WorkspaceIdentity = "wrong"
	if _, err = s.Reserve(ctx, q); err == nil {
		t.Fatal("replay context bypass")
	}
}
func TestWriteAuthorizationDenyDoesNotConsume(t *testing.T) {
	mutations := map[string]func(*WriteAuthorizationRequest){
		"workspace":   func(q *WriteAuthorizationRequest) { q.Context.WorkspaceIdentity = "other" },
		"nonce":       func(q *WriteAuthorizationRequest) { q.Context.Nonce = "other" },
		"policy":      func(q *WriteAuthorizationRequest) { q.Context.PolicyVersion = "other" },
		"content":     func(q *WriteAuthorizationRequest) { q.Candidate.Manifest.Entries[0].Postimage = q.Context.BaseIdentity },
		"target":      func(q *WriteAuthorizationRequest) { q.Candidate.Manifest.Entries[0].Target = "outside" },
		"transaction": func(q *WriteAuthorizationRequest) { q.TransactionID = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s, q, store, _ := authorizationFixture(t)
			mutate(&q)
			if _, err := s.Reserve(context.Background(), q); err == nil {
				t.Fatal("accepted")
			}
			r, _, _ := store.Find(context.Background(), q.ApprovalID)
			if r.State != domain.WriteAvailable {
				t.Fatal("denial consumed")
			}
		})
	}
	s, q, store, now := authorizationFixture(t)
	*now = now.Add(time.Hour)
	if _, err := s.Reserve(context.Background(), q); err == nil {
		t.Fatal("expired")
	}
	r, _, _ := store.Find(context.Background(), q.ApprovalID)
	if r.State != domain.WriteAvailable {
		t.Fatal("expiry consumed")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Reserve(canceled, q); err != context.Canceled {
		t.Fatal(err)
	}
}
