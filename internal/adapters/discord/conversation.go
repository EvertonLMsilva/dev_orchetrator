package discord

import (
	"context"
	"dev-orchestrator/internal/application"
	"encoding/json"
	"errors"
	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"time"
)

type conversationService interface {
	Handle(context.Context, application.ConversationInput) application.ConversationResponse
}
type conversationResponseClient interface {
	UpdateInteractionResponse(snowflake.ID, string, sdk.MessageUpdate, ...rest.RequestOpt) (*sdk.Message, error)
}

func (g *Gateway) SetConversationService(ctx context.Context, service conversationService) error {
	if service == nil || ctx.Err() != nil {
		return errors.New("conversation service required")
	}
	if _, ok := g.commands.(conversationResponseClient); !ok {
		return errors.New("conversation response transport required")
	}
	g.conversationMu.Lock()
	defer g.conversationMu.Unlock()
	if g.conversation != nil {
		return errors.New("conversation already configured")
	}
	g.conversation = service
	g.conversationFailed = make(chan struct{})
	g.conversationCtx, g.conversationCancel = context.WithCancel(ctx)
	return nil
}
func conversationDefinitions() []sdk.ApplicationCommandCreate {
	max := 1024
	return []sdk.ApplicationCommandCreate{sdk.SlashCommandCreate{Name: "analyze", Description: "Analyze with read-only evidence", Options: []sdk.ApplicationCommandOption{sdk.ApplicationCommandOptionString{Name: "intent", Description: "Declarative intent", Required: true, MaxLength: &max}}}}
}
func (g *Gateway) handleConversation(ack context.Context, i sdk.ApplicationCommandInteraction, data sdk.SlashCommandInteractionData) error {
	if data.CommandName() != "analyze" {
		return nil
	}
	if i.GuildID() == nil || i.Channel().ID() == 0 {
		return nil
	}
	option, present := data.Option("intent")
	var text string
	if !present || option.Type != sdk.ApplicationCommandOptionTypeString || json.Unmarshal(option.Value, &text) != nil {
		return nil
	}
	input := application.ConversationInput{Source: application.ConversationSource{GuildID: i.GuildID().String(), ChannelID: i.Channel().ID().String()}, Text: text}
	g.conversationMu.Lock()
	if g.conversationStopped || g.conversationCtx.Err() != nil {
		g.conversationMu.Unlock()
		return nil
	}
	if g.conversationBusy {
		g.conversationMu.Unlock()
		return g.commands.CreateInteractionResponse(i.ID(), i.Token(), sdk.InteractionResponse{Type: sdk.InteractionResponseTypeCreateMessage, Data: sdk.MessageCreate{Content: "Uma solicitação está em andamento.", Flags: sdk.MessageFlagEphemeral}}, rest.WithCtx(ack))
	}
	if e := g.commands.CreateInteractionResponse(i.ID(), i.Token(), sdk.InteractionResponse{Type: sdk.InteractionResponseTypeDeferredCreateMessage, Data: sdk.MessageCreate{Flags: sdk.MessageFlagEphemeral}}, rest.WithCtx(ack)); e != nil {
		g.conversationMu.Unlock()
		return e
	}
	g.conversationBusy = true
	g.conversationWorkers.Add(1)
	g.conversationMu.Unlock()
	go func() {
		defer g.conversationWorkers.Done()
		defer func() { g.conversationMu.Lock(); g.conversationBusy = false; g.conversationMu.Unlock() }()
		response := g.conversation.Handle(g.conversationCtx, input)
		// Service emits fixed public messages and a random correlation reference.
		ctx, cancel := context.WithTimeout(g.conversationCtx, 3*time.Second)
		defer cancel()
		_, err := g.commands.(conversationResponseClient).UpdateInteractionResponse(i.ApplicationID(), i.Token(), sdk.NewMessageUpdate().WithContent(response.Message).ClearAllowedMentions(), rest.WithCtx(ctx))
		if err != nil && g.conversationCtx.Err() == nil {
			g.conversationMu.Lock()
			if g.conversationFailure == nil {
				g.conversationFailure = errors.New("conversation response failed")
				g.conversationStopped = true
				close(g.conversationFailed)
			}
			g.conversationMu.Unlock()
			g.conversationCancel()
		}
	}()
	return nil
}
func (g *Gateway) stopConversation(ctx context.Context) error {
	g.conversationMu.Lock()
	g.conversationStopped = true
	if g.conversationCancel != nil {
		g.conversationCancel()
	}
	g.conversationMu.Unlock()
	done := make(chan struct{})
	go func() { g.conversationWorkers.Wait(); close(done) }()
	select {
	case <-done:
		g.conversationMu.Lock()
		err := g.conversationFailure
		g.conversationMu.Unlock()
		if err != nil {
			return err
		}
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Failed closes on an unexpected response failure; it exposes no SDK error.
func (g *Gateway) Failed() <-chan struct{} { return g.conversationFailed }
