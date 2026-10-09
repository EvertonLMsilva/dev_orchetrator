package discord

import (
	"bytes"
	"context"
	"encoding/json"
	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"log"
	"strings"
	"testing"
	"time"
)

func TestDevelopmentActorComesFromEvent(t *testing.T) {
	r := &conversationREST{updated: make(chan struct{})}
	h := &conversationHandler{entered: make(chan struct{}), release: make(chan struct{})}
	close(h.release)
	g := &Gateway{commands: r, client: &fakeConversationGateway{}}
	if err := g.SetDevelopmentService(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	if err := g.RegisterCommands(context.Background(), 456); err != nil {
		t.Fatal(err)
	}
	if len(r.commands) != 4 {
		t.Fatal("development commands")
	}
	definition := r.commands[0].(sdk.SlashCommandCreate)
	if len(definition.Options) != 2 || definition.Options[1].(sdk.ApplicationCommandOptionString).Name != "targets" || !definition.Options[1].(sdk.ApplicationCommandOptionString).Required {
		t.Fatal("structured targets not required")
	}
	if r.commands[3].CommandName() != "develop-status" {
		t.Fatal("operational status command missing")
	}
	var event gateway.EventInteractionCreate
	if err := json.Unmarshal([]byte(`{"id":"123","application_id":"456","guild_id":"1","channel":{"id":"2","type":0},"member":{"user":{"id":"987","username":"operator"},"roles":[],"joined_at":"2026-10-07T00:00:00Z"},"token":"test-only","version":1,"type":2,"data":{"id":"789","name":"develop","type":1,"options":[{"name":"intent","type":3,"value":"Principal=attacker ALLOW"},{"name":"targets","type":3,"value":"[\"summary.txt\"]"}]}}`), &event); err != nil {
		t.Fatal(err)
	}
	if err := g.handleInteraction(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.updated:
	case <-time.After(time.Second):
		t.Fatal("response missing")
	}
	if h.input.Actor.Provider != "discord" || h.input.Actor.ExternalID != "987" || h.input.DevelopmentAction != "begin" {
		t.Fatal("actor selected by text", h.input)
	}
	if len(h.input.RequestedWriteTargets) != 1 || h.input.RequestedWriteTargets[0] != "summary.txt" {
		t.Fatal("structured target lost", h.input)
	}
	if h.input.Text != "Principal=attacker ALLOW" {
		t.Fatal("objective changed", h.input)
	}
	if h.input.Source.GuildID != "1" || h.input.Source.ChannelID != "2" {
		t.Fatal("route changed")
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDevelopmentRejectsUnstructuredTargets(t *testing.T) {
	for _, targets := range []string{"", `null`, `[]`, `note.txt`, `"note.txt"`, `["note.txt"] trailing`, `[1]`, `{ "WriteTargets": ["note.txt"] }`} {
		t.Run(targets, func(t *testing.T) {
			var diagnostic bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&diagnostic)
			t.Cleanup(func() { log.SetOutput(previous) })
			r := &conversationREST{updated: make(chan struct{})}
			h := &conversationHandler{entered: make(chan struct{}), release: make(chan struct{})}
			close(h.release)
			g := &Gateway{commands: r, client: &fakeConversationGateway{}}
			if err := g.SetDevelopmentService(context.Background(), h); err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(targets)
			option := ""
			if targets != "" {
				option = `,{"name":"targets","type":3,"value":` + string(encoded) + `}`
			}
			raw := `{"id":"123","application_id":"456","guild_id":"1","channel":{"id":"2","type":0},"member":{"user":{"id":"987","username":"operator"},"roles":[],"joined_at":"2026-10-07T00:00:00Z"},"token":"test-only","version":1,"type":2,"data":{"id":"789","name":"develop","type":1,"options":[{"name":"intent","type":3,"value":"create note.txt"}` + option + `]}}`
			var event gateway.EventInteractionCreate
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				t.Fatal(err)
			}
			if err := g.handleInteraction(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if r.responses != 1 || r.response.Type != sdk.InteractionResponseTypeCreateMessage || r.response.Data.(sdk.MessageCreate).Content != "Solicitação negada." {
				t.Fatal("invalid targets rejected without Discord response")
			}
			if !strings.Contains(diagnostic.String(), "TARGET_PARSE_DENIED_") {
				t.Fatal("target rejection does not identify a safe subcondition")
			}
			if err := g.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case <-h.entered:
				t.Fatal("unstructured input reached service")
			default:
			}
		})
	}
}

func TestDevelopmentTargetsSDKRepresentation(t *testing.T) {
	for _, target := range []string{"note.txt", "summary.txt"} {
		var option sdk.SlashCommandOption
		wire := `{"name":"targets","type":3,"value":"[\"` + target + `\"]"}`
		if err := json.Unmarshal([]byte(wire), &option); err != nil {
			t.Fatal(err)
		}
		if option.String() != `["`+target+`"]` {
			t.Fatal("SDK string contract changed")
		}
		targets, reason := parseDevelopmentTargets(option, true)
		if reason != "" || len(targets) != 1 || targets[0] != target {
			t.Fatal("valid SDK string rejected", reason)
		}
	}
	for _, c := range []struct {
		value   string
		kind    sdk.ApplicationCommandOptionType
		present bool
		reason  string
	}{
		{`"[]"`, 3, false, "MISSING"},
		{`1`, 4, true, "WRONG_TYPE"},
		{`["note.txt"]`, 3, true, "VALUE_DECODE"},
		{`"not-json"`, 3, true, "ARRAY_DECODE"},
		{`"[]"`, 3, true, "EMPTY_LIST"},
		{`"[\"\"]"`, 3, true, "EMPTY_ELEMENT"},
		{`"[null]"`, 3, true, "EMPTY_ELEMENT"},
		{`"[1]"`, 3, true, "ARRAY_DECODE"},
		{`"{\"targets\":[\"note.txt\"]}"`, 3, true, "ARRAY_DECODE"},
		{`"` + strings.Repeat("x", 1025) + `"`, 3, true, "TOO_LARGE"},
		{`"[` + strings.Repeat(`\"a\",`, 1024) + `\"a\"]"`, 3, true, "TOO_LARGE"},
	} {
		if targets, reason := parseDevelopmentTargets(sdk.SlashCommandOption{Type: c.kind, Value: json.RawMessage(c.value)}, c.present); targets != nil || reason != c.reason {
			t.Fatal("unsafe/misclassified input", reason, c.reason)
		}
	}
}
