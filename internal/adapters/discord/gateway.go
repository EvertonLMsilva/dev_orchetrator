package discord

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
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
	client              gatewayClient
	queries             queryService
	continuation        continueService
	approvals           approvalService
	commands            commandClient
	conversation        conversationService
	development         bool
	conversationCtx     context.Context
	conversationCancel  context.CancelFunc
	conversationMu      sync.Mutex
	conversationWorkers sync.WaitGroup
	conversationBusy    bool
	conversationStopped bool
	conversationFailure error
	conversationFailed  chan struct{}
	identifyBudget      *startupIdentifyBudget
}

var _ ports.DiscordGateway = (*Gateway)(nil)

// NewGateway constructs a transport without connecting to Discord.
func NewGateway(token string) (*Gateway, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrTokenRequired
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := &Gateway{commands: rest.New(rest.NewClient(token, rest.WithRateLimiterConfigOpts(rest.WithMaxRetries(0)), rest.WithLogger(logger)))}
	g.identifyBudget = &startupIdentifyBudget{base: gateway.NewIdentifyRateLimiter()}
	client := gateway.New(token,
		g.onEvent,
		gateway.WithIntents(gateway.IntentsNone),
		gateway.WithAutoReconnect(false),
		gateway.WithLogger(logger),
		gateway.WithIdentifyRateLimiter(g.identifyBudget),
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
	if g.identifyBudget != nil {
		bounded, cancel := context.WithCancel(ctx)
		defer cancel()
		g.identifyBudget.setCancel(cancel)
		return g.client.Open(bounded)
	}
	return g.client.Open(ctx)
}

// Close always forwards the context so canceled shutdowns still disconnect.
// DisGo Close has no error result; only context cancellation can be reported.
func (g *Gateway) Close(ctx context.Context) error {
	err := g.stopConversation(ctx)
	g.client.Close(ctx)
	if err != nil {
		return err
	}
	return ctx.Err()
}

// The pinned SDK retries initial Open regardless of WithAutoReconnect(false).
// The identify gate preserves the SDK limiter on the first attempt and cancels
// startup before a second attempt can reach its dialer. Production has no resume
// configuration; successful Open completes before any later connection loss.
type startupIdentifyBudget struct {
	mu     sync.Mutex
	base   gateway.IdentifyRateLimiter
	used   bool
	cancel context.CancelFunc
}

func (b *startupIdentifyBudget) setCancel(cancel context.CancelFunc) {
	b.mu.Lock()
	b.cancel = cancel
	b.mu.Unlock()
}
func (b *startupIdentifyBudget) Wait(ctx context.Context, shard int) error {
	b.mu.Lock()
	if b.used {
		cancel := b.cancel
		b.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return errors.New("Discord startup attempt exhausted")
	}
	b.used = true
	b.mu.Unlock()
	return b.base.Wait(ctx, shard)
}
func (b *startupIdentifyBudget) Unlock(shard int)          { b.base.Unlock(shard) }
func (b *startupIdentifyBudget) Close(ctx context.Context) { b.base.Close(ctx) }
