// Command twitchbot runs the Twitch bot.
//
// On first run it creates a placeholder config.json (if missing) and
// exits with an instructive message. With a valid config it runs the
// OAuth user-token flow (which opens a browser), constructs the Helix
// API client, joins the configured channel via IRC, and dispatches chat
// commands. Ctrl-C triggers a clean shutdown.
package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"twitchbotv2/internal/api"
	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/command"
	"twitchbotv2/internal/state"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfg, err := state.LoadOrCreateConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Username == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Channel == "" {
		return errors.New("config.json is missing required fields (username, client_id, client_secret, channel)")
	}

	apiClient, userToken, err := api.NewAPIClient(cfg.Username, cfg.ClientID, cfg.ClientSecret)
	if err != nil {
		return fmt.Errorf("api client: %w", err)
	}

	disconnect := make(chan bool)
	messageCh := make(chan chat.Message)

	twitchClient := chat.NewTwitchClient(
		cfg.Username,
		cfg.Channel,
		apiClient,
		userToken,
		disconnect,
		messageCh,
	)
	if err := twitchClient.Connect(); err != nil {
		return fmt.Errorf("connect to twitch: %w", err)
	}
	defer twitchClient.Close()

	handler, err := command.NewCommandHandler(apiClient, "state.db")
	if err != nil {
		return fmt.Errorf("command handler: %w", err)
	}
	command.RegisterCommands(handler)

	// Watch for SIGINT/SIGTERM and close the disconnect channel.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %s, shutting down", sig)
		close(disconnect)
	}()

	log.Printf("bot is running, dispatching chat messages")
	for {
		select {
		case msg := <-messageCh:
			handler.Handle(msg)
		case <-disconnect:
			return nil
		}
	}
}
