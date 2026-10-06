package discord

import (
	"context"
	"dev-orchestrator/internal/application"
	"encoding/json"
	"errors"
	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/gorilla/websocket"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type conversationREST struct {
	commandREST
	mu        sync.Mutex
	content   string
	updated   chan struct{}
	updateErr error
}

func TestGatewayStartupNeverRedials(t *testing.T) {
	g, e := NewGateway("test-only")
	if e != nil {
		t.Fatal(e)
	}
	var attempts atomic.Int32
	dialer := &websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) {
		attempts.Add(1)
		return nil, errors.New("controlled dial failure")
	}}
	// Only the network boundary is replaced. The production startup budget and
	// the pinned SDK's actual Open/reconnect path remain in this test.
	g.client = gateway.New("test-only", g.onEvent, gateway.WithURL("ws://test.invalid"), gateway.WithDialer(dialer), gateway.WithAutoReconnect(false), gateway.WithIdentifyRateLimiter(g.identifyBudget))
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if e = g.Open(ctx); e == nil {
		t.Fatal("failed connection succeeded")
	}
	if attempts.Load() != 1 {
		t.Fatalf("dial attempts=%d", attempts.Load())
	}
	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	_ = g.Close(closeCtx)
}

func (r *conversationREST) UpdateInteractionResponse(_ snowflake.ID, _ string, m sdk.MessageUpdate, _ ...rest.RequestOpt) (*sdk.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content = *m.Content
	close(r.updated)
	return nil, r.updateErr
}

func TestConversationRegistrationAndResponseFailureFailClosed(t *testing.T) {
	r := &conversationREST{updated: make(chan struct{}), updateErr: errors.New("private SDK error")}
	h := &conversationHandler{entered: make(chan struct{}), release: make(chan struct{})}
	close(h.release)
	g := &Gateway{commands: r, client: &fakeConversationGateway{}}
	if e := g.SetConversationService(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	if len(r.commands) != 0 {
		t.Fatal("implicit registration")
	}
	if e := g.RegisterCommands(context.Background(), 456); e != nil {
		t.Fatal(e)
	}
	if len(r.commands) != 1 || r.commands[0].(sdk.SlashCommandCreate).Name != "analyze" {
		t.Fatal("effect commands registered")
	}
	if e := g.handleInteraction(context.Background(), conversationEvent(t)); e != nil {
		t.Fatal(e)
	}
	select {
	case <-g.Failed():
	case <-time.After(time.Second):
		t.Fatal("response failure ignored")
	}
	if e := g.Close(context.Background()); e == nil || e.Error() == "private SDK error" {
		t.Fatal("failed lifecycle or raw error exposed", e)
	}
	if r.responses != 1 {
		t.Fatal("retried acknowledgement")
	}
}

type conversationHandler struct {
	input   application.ConversationInput
	entered chan struct{}
	release chan struct{}
}

func (h *conversationHandler) Handle(ctx context.Context, in application.ConversationInput) application.ConversationResponse {
	h.input = in
	close(h.entered)
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return application.ConversationResponse{Status: "BLOCKED", Message: "Análise encerrada. Referência: safe"}
}
func conversationEvent(t *testing.T) gateway.EventInteractionCreate {
	t.Helper()
	var event gateway.EventInteractionCreate
	if e := json.Unmarshal([]byte(`{"id":"123","application_id":"456","guild_id":"1","channel":{"id":"2","type":0},"token":"test-only","version":1,"type":2,"data":{"id":"789","name":"analyze","type":1,"options":[{"name":"intent","type":3,"value":"human intent"}]}}`), &event); e != nil {
		t.Fatal(e)
	}
	return event
}
func TestConversationDeferredRoutingAndShutdown(t *testing.T) {
	r := &conversationREST{updated: make(chan struct{})}
	h := &conversationHandler{entered: make(chan struct{}), release: make(chan struct{})}
	g := &Gateway{commands: r, client: &fakeConversationGateway{}}
	if e := g.SetConversationService(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	if e := g.handleInteraction(context.Background(), conversationEvent(t)); e != nil {
		t.Fatal(e)
	}
	if r.response.Type != sdk.InteractionResponseTypeDeferredCreateMessage {
		t.Fatal("not deferred")
	}
	select {
	case <-h.entered:
	case <-time.After(time.Second):
		t.Fatal("worker missing")
	}
	if h.input.Source.GuildID != "1" || h.input.Source.ChannelID != "2" || h.input.Text != "human intent" {
		t.Fatal(h.input)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := g.Close(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestConversationCompletesAndRejectsOtherCommands(t *testing.T) {
	r := &conversationREST{updated: make(chan struct{})}
	h := &conversationHandler{entered: make(chan struct{}), release: make(chan struct{})}
	close(h.release)
	g := &Gateway{commands: r, client: &fakeConversationGateway{}}
	if e := g.SetConversationService(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	if e := g.handleInteraction(context.Background(), commandEvent(t, "approve", `[{"name":"approval","type":3,"value":"a"}]`)); e != nil {
		t.Fatal(e)
	}
	if r.responses != 0 {
		t.Fatal("effect command accepted")
	}
	if e := g.handleInteraction(context.Background(), conversationEvent(t)); e != nil {
		t.Fatal(e)
	}
	select {
	case <-r.updated:
	case <-time.After(time.Second):
		t.Fatal("response missing")
	}
	if r.content != "Análise encerrada. Referência: safe" {
		t.Fatal(r.content)
	}
	if e := g.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
}

type fakeConversationGateway struct{}

func (*fakeConversationGateway) Open(context.Context) error { return nil }
func (*fakeConversationGateway) Close(context.Context)      {}
