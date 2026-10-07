package infrastructure

import (
	"context"
	"dev-orchestrator/internal/ports"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

var ErrActorDenied = errors.New("actor authority denied")

type ActorMapping struct {
	Evidence  ports.ActorEvidence
	Principal ports.Principal
}

// ActorAuthority freezes trusted configuration. It has no request-driven update,
// wildcard, inheritance, or fallback to Channel routing / MCP READ grants.
type ActorAuthority struct {
	identities map[ports.ActorEvidence]ports.Principal
	grants     map[ports.Grant]bool
}

func actorLabel(s string) bool {
	if s == "" || len(s) > 256 || !utf8.ValidString(s) || strings.TrimSpace(s) != s || s == "*" {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}
func NewActorAuthority(mappings []ActorMapping, grants []ports.Grant) (*ActorAuthority, error) {
	a := &ActorAuthority{identities: map[ports.ActorEvidence]ports.Principal{}, grants: map[ports.Grant]bool{}}
	principals := map[string]bool{}
	for _, m := range mappings {
		if !actorLabel(m.Evidence.Provider) || !actorLabel(m.Evidence.ExternalID) || !actorLabel(m.Principal.ID) {
			return nil, ErrActorDenied
		}
		if _, ok := a.identities[m.Evidence]; ok {
			return nil, ErrActorDenied
		}
		a.identities[m.Evidence] = m.Principal
		principals[m.Principal.ID] = true
	}
	for _, g := range grants {
		if !principals[g.PrincipalID] || !actorLabel(g.ProjectID) || a.grants[g] {
			return nil, ErrActorDenied
		}
		switch g.Operation {
		case "WRITE_APPLY", "GIT_BRANCH", "GIT_COMMIT":
		default:
			return nil, ErrActorDenied
		}
		a.grants[g] = true
	}
	return a, nil
}
func (a *ActorAuthority) AuthenticateActor(ctx context.Context, e ports.ActorEvidence) (ports.Principal, error) {
	if err := ctx.Err(); err != nil {
		return ports.Principal{}, err
	}
	if a == nil {
		return ports.Principal{}, ErrActorDenied
	}
	p, ok := a.identities[e]
	if !ok {
		return ports.Principal{}, ErrActorDenied
	}
	return p, nil
}
func (a *ActorAuthority) FindActorGrant(ctx context.Context, g ports.Grant) (ports.Grant, bool, error) {
	if err := ctx.Err(); err != nil {
		return ports.Grant{}, false, err
	}
	if a == nil {
		return ports.Grant{}, false, ErrActorDenied
	}
	if !a.grants[g] {
		return ports.Grant{}, false, nil
	}
	return g, true, nil
}
