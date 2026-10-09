package discord

import (
	"context"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/ports"
	"encoding/json"
	"errors"
	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"log"
	"strings"
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

// Development is selected only by the trusted pilot composition.
func (g *Gateway) SetDevelopmentService(ctx context.Context, service conversationService) error {
	if err := g.SetConversationService(ctx, service); err != nil {
		return err
	}
	g.development = true
	return nil
}
func developmentDefinitions() []sdk.ApplicationCommandCreate {
	max := 1024
	return []sdk.ApplicationCommandCreate{
		sdk.SlashCommandCreate{Name: "develop", Description: "Propose a controlled pilot file change", Options: []sdk.ApplicationCommandOption{sdk.ApplicationCommandOptionString{Name: "intent", Description: "Requested file change", Required: true, MaxLength: &max}, sdk.ApplicationCommandOptionString{Name: "targets", Description: `Requested paths as JSON array, e.g. ["note.txt"]`, Required: true, MaxLength: &max}}},
		sdk.SlashCommandCreate{Name: "develop-confirm", Description: "Confirm the exact displayed operation", Options: []sdk.ApplicationCommandOption{sdk.ApplicationCommandOptionString{Name: "identity", Description: "Displayed operation identity", Required: true}}},
		sdk.SlashCommandCreate{Name: "develop-cancel", Description: "Block the current pilot task"},
		sdk.SlashCommandCreate{Name: "develop-status", Description: "Consult a persisted development cycle", Options: []sdk.ApplicationCommandOption{sdk.ApplicationCommandOptionString{Name: "cycle", Description: "Trusted cycle reference", Required: true}}},
	}
}
func conversationDefinitions() []sdk.ApplicationCommandCreate {
	max := 1024
	return []sdk.ApplicationCommandCreate{sdk.SlashCommandCreate{Name: "analyze", Description: "Analyze with read-only evidence", Options: []sdk.ApplicationCommandOption{sdk.ApplicationCommandOptionString{Name: "intent", Description: "Declarative intent", Required: true, MaxLength: &max}}}}
}
func (g *Gateway) handleConversation(ack context.Context, i sdk.ApplicationCommandInteraction, data sdk.SlashCommandInteractionData) error {
	action, optionName := "", "intent"
	if g.development {
		switch data.CommandName() {
		case "develop":
			action = "begin"
		case "develop-confirm":
			action = "confirm"
			optionName = "identity"
			log.Print("CONFIRM_RECEIVED")
		case "develop-cancel":
			action = "cancel"
		case "develop-status":
			action = "status"
			optionName = "cycle"
		default:
			return nil
		}
	} else if data.CommandName() != "analyze" {
		return nil
	}
	guildID := i.GuildID()
	if guild := i.PartialGuild(); guild != nil {
		if guildID != nil && *guildID != guild.ID {
			return g.rejectConversation(ack, i, "ROUTE_DENIED")
		}
		guildID = &guild.ID
	}
	if guildID == nil || *guildID == 0 || i.Channel().MessageChannel == nil || i.Channel().ID() == 0 {
		return g.rejectConversation(ack, i, "ROUTE_DENIED")
	}
	option, present := data.Option(optionName)
	var text string
	if action != "cancel" && (!present || option.Type != sdk.ApplicationCommandOptionTypeString || json.Unmarshal(option.Value, &text) != nil) {
		if action == "confirm" {
			return g.rejectConversation(ack, i, "CONFIRM_PARSE_DENIED")
		}
		return g.rejectConversation(ack, i, "INTENT_DENIED")
	}
	if action == "confirm" && !validConfirmationIdentity(text) {
		return g.rejectConversation(ack, i, "CONFIRM_PARSE_DENIED")
	}
	input := application.ConversationInput{Source: application.ConversationSource{GuildID: guildID.String(), ChannelID: i.Channel().ID().String()}, Text: text}
	if g.development {
		if i.User().ID == 0 {
			return g.rejectConversation(ack, i, "AUTHENTICATION_DENIED")
		}
		input.Actor = ports.ActorEvidence{Provider: "discord", ExternalID: i.User().ID.String()}
		input.DevelopmentAction = action
		if action == "begin" {
			option, present := data.Option("targets")
			var reason string
			input.RequestedWriteTargets, reason = parseDevelopmentTargets(option, present)
			if reason != "" {
				return g.rejectConversation(ack, i, "TARGET_PARSE_DENIED_"+reason)
			}
		}
		if action == "confirm" || action == "status" {
			input.Confirmation = text
			input.Text = ""
		}
	}
	g.conversationMu.Lock()
	if g.conversationStopped || g.conversationCtx.Err() != nil {
		g.conversationMu.Unlock()
		return g.rejectConversation(ack, i, "RUNTIME_DENIED")
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
	if action == "confirm" {
		log.Print("CONFIRM_ACKED")
	}
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
			if action == "confirm" {
				log.Print("CONFIRM_RESPONSE_FAILED")
			}
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

func validConfirmationIdentity(identity string) bool {
	if len(identity) != 64 {
		return false
	}
	for _, char := range identity {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func parseDevelopmentTargets(option sdk.SlashCommandOption, present bool) ([]string, string) {
	if !present {
		return nil, "MISSING"
	}
	if option.Type != sdk.ApplicationCommandOptionTypeString {
		return nil, "WRONG_TYPE"
	}
	var encoded string
	if json.Unmarshal(option.Value, &encoded) != nil {
		return nil, "VALUE_DECODE"
	}
	if len(encoded) > 1024 {
		return nil, "TOO_LARGE"
	}
	var targets []string
	if json.Unmarshal([]byte(encoded), &targets) != nil {
		return nil, "ARRAY_DECODE"
	}
	if len(targets) == 0 {
		return nil, "EMPTY_LIST"
	}
	if len(targets) > 1024 {
		return nil, "TOO_MANY"
	}
	for _, target := range targets {
		if strings.TrimSpace(target) == "" {
			return nil, "EMPTY_ELEMENT"
		}
	}
	return targets, ""
}

func (g *Gateway) rejectConversation(ctx context.Context, i sdk.ApplicationCommandInteraction, stage string) error {
	// Fixed internal classifications only; never interaction/provider contents.
	log.Print(stage)
	return g.commands.CreateInteractionResponse(i.ID(), i.Token(), sdk.InteractionResponse{Type: sdk.InteractionResponseTypeCreateMessage, Data: sdk.MessageCreate{Content: "Solicitação negada.", Flags: sdk.MessageFlagEphemeral}}, rest.WithCtx(ctx))
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
