//go:build linux

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

type registrationOnly struct{}

func (registrationOnly) Handle(context.Context, application.ConversationInput) application.ConversationResponse {
	return application.ConversationResponse{Status: "REJECTED", Message: "Solicitação negada."}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "DEVELOPMENT STOPPED fail-closed")
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) (result error) {
	flags := flag.NewFlagSet("development", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "trusted pilot configuration")
	tokenPath := flags.String("discord-token-file", "/run/secrets/discord_token", "Discord credential")
	register := flags.String("register-commands", "", "explicit Discord application ID registration")
	check := flags.Bool("check-config", false, "offline validation")
	if flags.Parse(args) != nil || flags.NArg() != 0 || *configPath == "" {
		return errors.New("invalid pilot configuration")
	}
	c, err := composition.LoadDevelopmentConfig(*configPath)
	if err != nil {
		return err
	}
	if *check {
		fmt.Println("DEVELOPMENT_CONFIG PASS")
		return nil
	}
	f, err := os.Open(*tokenPath)
	if err != nil {
		return errors.New("Discord credential unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	f.Close()
	if err != nil || len(data) > 4096 {
		return errors.New("Discord credential unavailable")
	}
	token := strings.TrimSpace(string(data))
	clear(data)
	g, err := discord.NewGateway(token)
	token = ""
	if err != nil {
		return errors.New("Discord credential unavailable")
	}
	var service *composition.DevelopmentService
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		result = errors.Join(result, g.Close(shutdown))
		if service != nil {
			result = errors.Join(result, service.Shutdown(shutdown))
		}
	}()
	if *register != "" {
		id, err := snowflake.Parse(*register)
		if err != nil || id == 0 {
			return errors.New("invalid application ID")
		}
		if err := g.SetDevelopmentService(ctx, registrationOnly{}); err != nil {
			return err
		}
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return g.RegisterCommands(bounded, id)
	}
	service, err = composition.NewDevelopmentService(ctx, c)
	if err != nil {
		return err
	}
	if err := g.SetDevelopmentService(ctx, service); err != nil {
		return err
	}
	startup, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = g.Open(startup)
	cancel()
	if err != nil {
		return err
	}
	fmt.Println("DEVELOPMENT READY explicit confirmations required")
	select {
	case <-ctx.Done():
	case <-g.Failed():
	}
	return nil
}
