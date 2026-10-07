package ports

import "context"

// ActorEvidence is identity evidence supplied exclusively by a trusted input
// adapter after verifying its external event. It is not a permission or Principal.
type ActorEvidence struct{ Provider, ExternalID string }
type ActorAuthenticationPort interface {
	AuthenticateActor(context.Context, ActorEvidence) (Principal, error)
}
type ActorGrantRepository interface {
	FindActorGrant(context.Context, Grant) (Grant, bool, error)
}
