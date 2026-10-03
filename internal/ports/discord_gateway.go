package ports

import "context"

// DiscordGateway defines the Discord transport lifecycle.
type DiscordGateway interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
}
