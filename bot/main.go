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
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"twitchbotv2/internal/api"
	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/command"
	"twitchbotv2/internal/eventsub"
	"twitchbotv2/internal/state"
)

var backoffTime = 500 * time.Millisecond

func main() {
	reset := false
	logFileClose, err := setupLogging()
	defer logFileClose()
	if err != nil {
		log.Fatal(err)
	}

	for {
		if err := run(reset); err != nil {
			log.Printf("fatal: %v; retrying", err)
			reset = true
		} else {
			return
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
		log.Printf("load config: %v", err)
		return nil
	}
	if cfg.Username == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Channel == "" {
		return errors.New("config.json is missing required fields (username, client_id, client_secret, channel)")
	}

	disconnect := make(chan bool)

	userToken, err := api.InitUserToken(cfg.ClientID, cfg.ClientSecret,
		"chat:read+chat:edit+user:write:chat+user:bot+channel:manage:broadcast+"+
			"moderator:read:followers+moderator:read:chatters+clips:edit",
		disconnect,
	)
	if err != nil {
		return fmt.Errorf("user OAuth: %w", err)
	}
	appToken, err := api.GetAppAccessTokenManager(cfg.ClientID, cfg.ClientSecret)
	if err != nil {
		return fmt.Errorf("app access token: %w", err)
	}

	apiClient, err := api.NewAPIClient(cfg.Username, cfg.ClientID, userToken, appToken)
	if err != nil {
		return fmt.Errorf("api client: %w", err)
	}

	messageCh := make(chan chat.Message, 100)
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
	defer runWithTimeout(
		"twitchClient.Close()",
		twitchClient.Close,
		time.Second*5,
	)

	broadcasterToken, err := api.InitUserToken(cfg.BroadcasterID, cfg.BroadcasterSecret,
		"channel:read:redemptions", disconnect)
	if err != nil {
		return fmt.Errorf("broadcaster OAuth: %w", err)
	}
	eventCh := make(chan eventsub.RedemptionEvent, 20)
	eventClient := eventsub.NewEventSubClient(
		cfg.BroadcasterID,
		cfg.Channel,
		apiClient,
		broadcasterToken,
		disconnect,
		eventCh,
	)
	eventClient.Connect()
	defer runWithTimeout(
		"eventClient.Close()",
		eventClient.Close,
		time.Second*5,
	)

	store, err := state.NewKVStorage("state.db")
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer store.Close()
	handler, err := command.NewCommandHandler(apiClient, store)
	if err != nil {
		return fmt.Errorf("command handler: %w", err)
	}
	command.LoadCommands(handler, "commands.json")

	timedRunner := command.NewCommandWheel(time.Minute*10, handler, disconnect,
		"donate", "follow", "socials", "today")
	go timedRunner.StartTimedCommands()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	log.Printf("bot is running, dispatching chat messages")
	backoffTime = 500 * time.Millisecond
	for {
		select {
		case msg := <-messageCh:
			go handler.Handle(msg)
		case event := <-eventCh:
			go handler.HandleRedeem(event)
		case <-disconnect:
			return fmt.Errorf("client unexpectedly disconnected")
		case sig := <-sigCh:
			log.Printf("received %v, shutting down", sig)
			return nil
		}
	}
}

func setupLogging() (func(), error) {
	if err := os.Rename("log.txt", "old_log.txt"); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("rotate log file: %w", err)
	}

	logFile, err := os.OpenFile("log.txt", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	log.SetOutput(io.MultiWriter(os.Stdout, logFile))
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	return func() { logFile.Close() }, nil
}

// runWithTimeout runs closeFn in a goroutine and waits up to timeout for
// it to finish. If it doesn't finish in time, runWithTimeout returns
// anyway and logs a warning. This aims to ensure a goroutine cannot
// block shutdown. Intended for use with defer.
func runWithTimeout(name string, closeFn func(), timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		closeFn()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("%s: close did not finish within %v, abandoning", name, timeout)
	}
}
