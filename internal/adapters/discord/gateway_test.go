package discord

import (
	"context"
	"errors"
	"testing"

	"dev-orchestrator/internal/ports"

	"github.com/disgoorg/disgo/gateway"
)

var _ ports.DiscordGateway = (*Gateway)(nil)

func TestNewGatewayRequiresToken(t *testing.T) {
	for _, token := range []string{"", " \t\r\n"} {
		g, err := NewGateway(token)
		if g != nil || !errors.Is(err, ErrTokenRequired) {
			t.Fatalf("expected nil gateway and ErrTokenRequired, got %v", err)
		}
	}
}

func TestNewGatewayWithoutNetwork(t *testing.T) {
	g, err := NewGateway("clearly-fake-test-token")
	if err != nil || g == nil || g.client == nil {
		t.Fatalf("construction failed: %v", err)
	}
	// Construction only; never open the real client.
	client, ok := g.client.(gateway.Gateway)
	if !ok || client.Intents() != gateway.IntentsNone {
		t.Fatal("expected DisGo gateway with zero intents")
	}
	if err := g.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type fakeClient struct {
	openCalls, closeCalls int
	openCtx, closeCtx     context.Context
	openErr               error
	cancelOnClose         context.CancelFunc
}

func (c *fakeClient) Open(ctx context.Context) error {
	c.openCalls++
	c.openCtx = ctx
	return c.openErr
}

func (c *fakeClient) Close(ctx context.Context) {
	c.closeCalls++
	c.closeCtx = ctx
	if c.cancelOnClose != nil {
		c.cancelOnClose()
	}
}

func TestOpen(t *testing.T) {
	want := errors.New("open failed")
	for _, openErr := range []error{nil, want} {
		client := &fakeClient{openErr: openErr}
		g := &Gateway{client: client}
		ctx := context.Background()
		if err := g.Open(ctx); !errors.Is(err, openErr) {
			t.Fatalf("got %v, want %v", err, openErr)
		}
		if client.openCalls != 1 || client.openCtx != ctx {
			t.Fatal("open/context not forwarded")
		}
	}
}

func TestOpenCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &fakeClient{}
	g := &Gateway{client: client}
	if err := g.Open(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if client.openCalls != 0 {
		t.Fatal("canceled open reached client")
	}
}

func TestClose(t *testing.T) {
	for _, mode := range []string{"active", "already canceled", "canceled during close"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &fakeClient{}
			g := &Gateway{client: client}
			if err := g.Open(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "already canceled" {
				cancel()
			}
			if mode == "canceled during close" {
				client.cancelOnClose = cancel
			}
			if err := g.Close(ctx); !errors.Is(err, ctx.Err()) {
				t.Fatalf("got %v, want %v", err, ctx.Err())
			}
			if client.closeCalls != 1 || client.closeCtx != ctx {
				t.Fatal("close/context not forwarded")
			}
		})
	}
}
