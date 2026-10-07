package main

import (
	"context"
	"dev-orchestrator/internal/adapters/discord"
	"dev-orchestrator/internal/application"
	"dev-orchestrator/internal/composition"
	"errors"
	"flag"
	"fmt"
	"github.com/disgoorg/snowflake/v2"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := run(ctx, os.Args[1:], os.Stderr); e != nil {
		fmt.Fprintln(os.Stderr, "Orchestrator encerrado sem sucesso.")
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("orchestrator", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("config", "", "read-only config JSON")
	tokenPath := flags.String("discord-token-file", "/run/secrets/discord_token", "provisioned Discord credential")
	check := flags.Bool("check-config", false, "validate configuration without external connections")
	register := flags.Bool("register-commands", false, "explicitly provision the read-only Discord command")
	health := flags.String("mcp-health", "", "check local MCP health/readiness without starting runtime")
	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return composition.ErrConfig
	}
	if *health != "" {
		if *path != "" || *check || *register {
			return composition.ErrConfig
		}
		return mcpHealth(ctx, *health)
	}
	if *path == "" || (*check && *register) {
		return composition.ErrConfig
	}
	config, e := composition.LoadConfig(*path)
	if e != nil {
		return e
	}
	if *check {
		fmt.Fprintln(output, "READ_ONLY_CONFIG PASS")
		return nil
	}
	if *register && config.DisableDiscord {
		return composition.ErrConfig
	}
	var gateway *discord.Gateway
	if !config.DisableDiscord {
		token, err := readToken(*tokenPath)
		if err != nil {
			return err
		}
		gateway, e = discord.NewGateway(token)
		token = ""
		if e != nil {
			return e
		}
	}
	if *register {
		// Registration is a distinct, explicitly selected external operation.
		id, e := snowflake.Parse(config.ApplicationID)
		if e != nil || id == 0 {
			return composition.ErrConfig
		}
		if e = gateway.SetConversationService(ctx, registrationOnly{}); e != nil {
			return e
		}
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return gateway.RegisterCommands(bounded, id)
	}
	service, e := composition.NewService(ctx, config)
	if e != nil {
		return e
	}
	if gateway != nil {
		if e = gateway.SetConversationService(ctx, service); e == nil {
			openCtx, cancelOpen := context.WithTimeout(ctx, time.Duration(config.RequestTimeout))
			e = gateway.Open(openCtx)
			cancelOpen()
		}
	}
	if e == nil && config.MCP != nil {
		e = service.StartMCP()
		if e == nil && !service.MCPReady() {
			e = errors.New("MCP not ready")
		}
	}
	if e == nil {
		fmt.Fprintln(output, "READ_ONLY_SERVICE STARTED")
		if config.MCP != nil {
			fmt.Fprintln(output, "MCP_READ READY")
		}
		var gatewayFailed <-chan struct{}
		if gateway != nil {
			gatewayFailed = gateway.Failed()
		}
		select {
		case <-ctx.Done():
		case <-gatewayFailed:
		case <-service.Failed():
			e = errors.New("operational service failed")
		}
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Duration(config.ShutdownTimeout))
	defer cancelShutdown()
	var gatewayErr error
	if gateway != nil {
		gatewayErr = gateway.Close(shutdown)
	}
	serviceErr := service.Shutdown(shutdown)
	if e != nil || gatewayErr != nil || serviceErr != nil {
		return errors.New("service lifecycle failed")
	}
	fmt.Fprintln(output, "READ_ONLY_SHUTDOWN PASS")
	return nil
}

func mcpHealth(ctx context.Context, endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Hostname() != "127.0.0.1" && u.Hostname() != "::1" && u.Hostname() != "localhost") || (u.Path != "/readyz" && u.Path != "/healthz") {
		return composition.ErrConfig
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return composition.ErrConfig
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return errors.New("MCP not ready")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("MCP not ready")
	}
	return nil
}
func readToken(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", errors.New("Discord credential unavailable")
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 4097))
	if e != nil || len(data) > 4096 {
		return "", errors.New("Discord credential unavailable")
	}
	token := strings.TrimSpace(string(data))
	clear(data)
	if token == "" {
		return "", discord.ErrTokenRequired
	}
	return token, nil
}

type registrationOnly struct{}

func (registrationOnly) Handle(context.Context, application.ConversationInput) application.ConversationResponse {
	return application.ConversationResponse{Status: "REJECTED", Message: "Solicitação não processada."}
}
