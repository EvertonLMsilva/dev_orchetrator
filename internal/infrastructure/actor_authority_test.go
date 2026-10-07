package infrastructure

import (
	"context"
	"dev-orchestrator/internal/ports"
	"testing"
)

func TestActorAuthorityExact(t *testing.T) {
	ctx := context.Background()
	e := ports.ActorEvidence{Provider: "discord", ExternalID: "123"}
	g := ports.Grant{PrincipalID: "operator", ProjectID: "pilot", Operation: "WRITE_APPLY"}
	a, err := NewActorAuthority([]ActorMapping{{Evidence: e, Principal: ports.Principal{ID: "operator"}}}, []ports.Grant{g})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.AuthenticateActor(ctx, e)
	if err != nil || p.ID != "operator" {
		t.Fatal(p, err)
	}
	for _, bad := range []ports.ActorEvidence{{}, {Provider: "discord", ExternalID: "operator"}, {Provider: "discord", ExternalID: "124"}, {Provider: "other", ExternalID: "123"}} {
		if _, err := a.AuthenticateActor(ctx, bad); err == nil {
			t.Fatal("unknown identity accepted")
		}
	}
	for _, op := range []string{"WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT"} {
		q := g
		q.Operation = op
		_, ok, err := a.FindActorGrant(ctx, q)
		if err != nil || ok != (op == "WRITE_APPLY") {
			t.Fatal("operation authority leaked", op)
		}
	}
	q := g
	q.ProjectID = "other"
	if _, ok, _ := a.FindActorGrant(ctx, q); ok {
		t.Fatal("project authority leaked")
	}
	if _, err := NewActorAuthority([]ActorMapping{{e, p}, {e, p}}, []ports.Grant{g}); err == nil {
		t.Fatal("duplicate mapping")
	}
	if _, err := NewActorAuthority([]ActorMapping{{e, p}}, []ports.Grant{g, g}); err == nil {
		t.Fatal("duplicate grant")
	}
	q = g
	q.Operation = "*"
	if _, err := NewActorAuthority([]ActorMapping{{e, p}}, []ports.Grant{q}); err == nil {
		t.Fatal("wildcard")
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := a.AuthenticateActor(cancelCtx, e); err == nil {
		t.Fatal("cancellation")
	}
}
