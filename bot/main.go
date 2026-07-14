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
	"time"

	"twitchbotv2/internal/api"
	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/command"
	"twitchbotv2/internal/state"
)

var backoffTime = 500 * time.Millisecond

func main() {
	reset := false

	for {
		if err := run(reset); err != nil {
			log.Printf("fatal: %v; retrying", err)
			reset = true
		}
	}
}

func run(backoff bool) error {
	if backoff {
		log.Printf("Waiting %v milliseconds before retry", backoffTime)
		time.Sleep(backoffTime)
		backoffTime *= 2
		backoffTime = min(backoffTime, 32*time.Second)
	}
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

	store, err := state.NewKVStorage("state.db")
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer store.Close()
	handler, err := command.NewCommandHandler(apiClient, store)
	if err != nil {
		return fmt.Errorf("command handler: %w", err)
	}
	command.RegisterCommands(handler)

	timedRunner := command.NewCommandWheel(time.Minute*10, handler, disconnect,
		"donate", "follow", "socials", "today")
	go timedRunner.StartTimedCommands()

	log.Printf("bot is running, dispatching chat messages")
	backoffTime = 500 * time.Millisecond
	for {
		select {
		case msg := <-messageCh:
			handler.Handle(msg)
		case <-disconnect:
			return fmt.Errorf("client unexpectedly disconnected")
		}
	}
}
