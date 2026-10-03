package discord

import (
	"context"
	"errors"
	"strings"
	"time"

	"dev-orchestrator/internal/ports"

	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
)

var ErrTokenRequired = errors.New("discord bot token required")

// gatewayClient keeps the SDK lifecycle private and testable without a network.
type gatewayClient interface {
	Open(context.Context) error
	Close(context.Context)
}

// Gateway adapts the DisGo Gateway transport to the orchestrator port.
type Gateway struct {
	client       gatewayClient
	queries      queryService
	continuation continueService
	approvals    approvalService
	commands     commandClient
}

var _ ports.DiscordGateway = (*Gateway)(nil)

// NewGateway constructs a transport without connecting to Discord.
func NewGateway(token string) (*Gateway, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrTokenRequired
	}
	g := &Gateway{commands: rest.New(rest.NewClient(token))}
	client := gateway.New(token,
		g.onEvent,
		gateway.WithIntents(gateway.IntentsNone),
		gateway.WithAutoReconnect(false),
	)
	g.client = client
	return g, nil
}

func (g *Gateway) onEvent(_ gateway.Gateway, eventType gateway.EventType, _ int, data gateway.EventData) {
	if eventType != gateway.EventTypeInteractionCreate {
		return
	}
	event, ok := data.(gateway.EventInteractionCreate)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = g.handleInteraction(ctx, event)
}

func (g *Gateway) Open(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return g.client.Open(ctx)
}

// Close always forwards the context so canceled shutdowns still disconnect.
// DisGo Close has no error result; only context cancellation can be reported.
func (g *Gateway) Close(ctx context.Context) error {
	g.client.Close(ctx)
	return ctx.Err()
}
