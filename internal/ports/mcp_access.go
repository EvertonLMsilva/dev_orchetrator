package ports

import "context"

// AuthenticationEvidence is opaque authentication material from an input adapter.
// It is not a principal, channel ID or Planner/Executor credential. Implementations
// must verify it and must not log or persist its raw material in project state.
type AuthenticationEvidence struct{ Material []byte }

// Principal is the identity resolved by trusted authentication infrastructure.
// A caller-supplied ID is never accepted by the application entry boundary.
type Principal struct{ ID string }

// AuthenticationPort verifies evidence independently of channel or provider.
// Missing/invalid evidence must fail; an ID alone is not proof of identity.
type AuthenticationPort interface {
	Authenticate(context.Context, AuthenticationEvidence) (Principal, error)
}

// Grant permits exactly one READ operation on one project for one principal.
// No roles, wildcards, inheritance or implicit permissions are supported.
type Grant struct{ PrincipalID, Operation, ProjectID string }

// GrantRepository consults the trusted grant authority on every request.
// Absence returns false; errors deny access. Returned tuples are checked exactly.
type GrantRepository interface {
	FindExact(context.Context, Grant) (Grant, bool, error)
}
