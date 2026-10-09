package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	sdk "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/gateway"
)

func TestConfirmGatewayDiagnostics(t *testing.T) {
	for _, mode := range []string{"valid", "missing", "invalid", "ack-failure", "response-failure"} {
		t.Run(mode, func(t *testing.T) {
			var logs bytes.Buffer
			writer, flags := log.Writer(), log.Flags()
			log.SetOutput(&logs)
			log.SetFlags(0)
			t.Cleanup(func() { log.SetOutput(writer); log.SetFlags(flags) })
			r := &conversationREST{updated: make(chan struct{})}
			if mode == "ack-failure" {
				r.err = errors.New("SECRET_ACK_TOKEN")
			}
			if mode == "response-failure" {
				r.updateErr = errors.New("SECRET_RESPONSE_TOKEN")
			}
			h := &conversationHandler{entered: make(chan struct{}), release: make(chan struct{})}
			close(h.release)
			g := &Gateway{commands: r, client: &fakeConversationGateway{}}
			if err := g.SetDevelopmentService(context.Background(), h); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = g.Close(context.Background()) })
			identity := strings.Repeat("a", 64)
			options := `[{"name":"identity","type":3,"value":"` + identity + `"}]`
			if mode == "missing" {
				options = `[]`
			}
			if mode == "invalid" {
				options = `[{"name":"identity","type":3,"value":"SECRET_INVALID_IDENTITY"}]`
			}
			raw := `{"id":"123","application_id":"456","guild_id":"1","channel":{"id":"2","type":0},"member":{"user":{"id":"987"}},"token":"SECRET_DISCORD_TOKEN","version":1,"type":2,"data":{"id":"789","name":"develop-confirm","type":1,"options":` + options + `}}`
			var event gateway.EventInteractionCreate
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			g.onEvent(nil, gateway.EventTypeInteractionCreate, 1, event)
			if time.Since(start) >= 3*time.Second || r.responses != 1 {
				t.Fatal("ACK deadline/attempt contract")
			}
			switch mode {
			case "valid", "response-failure":
				if r.response.Type != sdk.InteractionResponseTypeDeferredCreateMessage {
					t.Fatal("not deferred")
				}
				if mode == "response-failure" {
					select {
					case <-g.Failed():
					case <-time.After(time.Second):
						t.Fatal("response failure not fail-closed")
					}
				} else {
					select {
					case <-r.updated:
					case <-time.After(time.Second):
						t.Fatal("response missing")
					}
				}
			default:
				select {
				case <-h.entered:
					t.Fatal("effect handler entered before valid ACK/parse")
				default:
				}
			}
			got := logs.String()
			if !strings.Contains(got, "CONFIRM_RECEIVED\n") {
				t.Fatal("receipt missing")
			}
			expected := map[string]string{"missing": "CONFIRM_PARSE_DENIED", "invalid": "CONFIRM_PARSE_DENIED", "ack-failure": "CONFIRM_ACK_FAILED", "response-failure": "CONFIRM_RESPONSE_FAILED"}[mode]
			if expected != "" && !strings.Contains(got, expected+"\n") {
				t.Fatal("classification missing", expected)
			}
			if mode == "ack-failure" && !strings.Contains(got, "DISCORD_HANDLER_FAILED\n") {
				t.Fatal("handler error discarded")
			}
			if strings.Contains(got, "SECRET") || strings.Contains(got, identity) {
				t.Fatal("diagnostic leaked input/error")
			}
		})
	}
}
