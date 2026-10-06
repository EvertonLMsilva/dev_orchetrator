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
	if flags.Parse(args) != nil || flags.NArg() != 0 || *path == "" || (*check && *register) {
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
	token, e := readToken(*tokenPath)
	if e != nil {
		return e
	}
	gateway, e := discord.NewGateway(token)
	token = ""
	if e != nil {
		return e
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
	if e = gateway.SetConversationService(ctx, service); e != nil {
		service.Shutdown(context.Background())
		return e
	}
	openCtx, cancelOpen := context.WithTimeout(ctx, time.Duration(config.RequestTimeout))
	e = gateway.Open(openCtx)
	cancelOpen()
	if e == nil {
		fmt.Fprintln(output, "READ_ONLY_SERVICE STARTED")
		select {
		case <-ctx.Done():
		case <-gateway.Failed():
		case <-service.Failed():
			e = errors.New("operational service failed")
		}
	}
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), time.Duration(config.ShutdownTimeout))
	defer cancelShutdown()
	gatewayErr := gateway.Close(shutdown)
	serviceErr := service.Shutdown(shutdown)
	if e != nil || gatewayErr != nil || serviceErr != nil {
		return errors.New("service lifecycle failed")
	}
	fmt.Fprintln(output, "READ_ONLY_SHUTDOWN PASS")
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
