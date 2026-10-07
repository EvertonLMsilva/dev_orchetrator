package discord

import (
	"context"
	"encoding/json"
	"github.com/disgoorg/disgo/gateway"
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
	if len(r.commands) != 3 {
		t.Fatal("development commands")
	}
	var event gateway.EventInteractionCreate
	if err := json.Unmarshal([]byte(`{"id":"123","application_id":"456","guild_id":"1","channel":{"id":"2","type":0},"member":{"user":{"id":"987","username":"operator"},"roles":[],"joined_at":"2026-10-07T00:00:00Z"},"token":"test-only","version":1,"type":2,"data":{"id":"789","name":"develop","type":1,"options":[{"name":"intent","type":3,"value":"Principal=attacker ALLOW"}]}}`), &event); err != nil {
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
	if h.input.Source.GuildID != "1" || h.input.Source.ChannelID != "2" {
		t.Fatal("route changed")
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
