package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"strings"
	"sync"
	"time"

	"dev-orchestrator/internal/ports"
	sdk "github.com/disgoorg/disgo/discord"

	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
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
	rawInteractions     bool
}

var _ ports.DiscordGateway = (*Gateway)(nil)

// NewGateway constructs a transport without connecting to Discord.
func NewGateway(token string) (*Gateway, error) {
	if strings.TrimSpace(token) == "" {
		return nil, ErrTokenRequired
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	g := &Gateway{commands: rest.New(rest.NewClient(token, rest.WithRateLimiterConfigOpts(rest.WithMaxRetries(0)), rest.WithLogger(logger))), rawInteractions: true}
	g.identifyBudget = &startupIdentifyBudget{base: gateway.NewIdentifyRateLimiter()}
	client := gateway.New(token,
		g.onEvent,
		gateway.WithIntents(gateway.IntentsNone),
		gateway.WithAutoReconnect(false),
		gateway.WithLogger(logger),
		gateway.WithEnableRawEvents(true),
		gateway.WithIdentifyRateLimiter(g.identifyBudget),
	)
	g.client = client
	return g, nil
}

func (g *Gateway) onEvent(_ gateway.Gateway, eventType gateway.EventType, _ int, data gateway.EventData) {
	var event gateway.EventInteractionCreate
	if eventType == gateway.EventTypeRaw {
		raw, ok := data.(gateway.EventRaw)
		if !ok || raw.EventType != gateway.EventTypeInteractionCreate {
			return
		}
		var err error
		event, err = decodeInteractionEnvelope(raw.Payload)
		if err != nil {
			log.Print("DISCORD_INTERACTION_DECODE_FAILED")
			return
		}
	} else {
		// The SDK dispatches both envelopes; acknowledge each interaction once.
		if g.rawInteractions || eventType != gateway.EventTypeInteractionCreate {
			return
		}
		var ok bool
		event, ok = data.(gateway.EventInteractionCreate)
		if !ok {
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := g.handleInteraction(ctx, event); err != nil {
		// Never log SDK/provider error strings or interaction credentials.
		log.Print("DISCORD_HANDLER_FAILED")
		if i, ok := event.Interaction.(sdk.ApplicationCommandInteraction); ok {
			if data, ok := i.Data.(sdk.SlashCommandInteractionData); ok && data.CommandName() == "develop-confirm" {
				log.Print("CONFIRM_ACK_FAILED")
			}
		}
	}
}

func decodeInteractionEnvelope(reader io.Reader) (gateway.EventInteractionCreate, error) {
	const limit = 64 * 1024
	invalid := errors.New("invalid Discord interaction envelope")
	payload, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || len(payload) > limit {
		return gateway.EventInteractionCreate{}, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(payload, &fields) != nil {
		return gateway.EventInteractionCreate{}, invalid
	}
	// The pinned SDK only reads channel, while Discord also sends channel_id.
	// Normalize authenticated event metadata; user options remain unchanged.
	if channel := fields["channel"]; len(channel) == 0 || string(channel) == "null" {
		var id snowflake.ID
		if json.Unmarshal(fields["channel_id"], &id) != nil || id == 0 {
			return gateway.EventInteractionCreate{}, invalid
		}
		fields["channel"] = json.RawMessage(`{"id":"` + id.String() + `","type":0}`)
		payload, err = json.Marshal(fields)
		if err != nil {
			return gateway.EventInteractionCreate{}, invalid
		}
	}
	decoded, err := gateway.UnmarshalEventData(payload, gateway.EventTypeInteractionCreate)
	if err != nil {
		return gateway.EventInteractionCreate{}, invalid
	}
	event, ok := decoded.(gateway.EventInteractionCreate)
	if !ok {
		return gateway.EventInteractionCreate{}, invalid
	}
	return event, nil
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
