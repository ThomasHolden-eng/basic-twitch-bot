// Command twitchbot runs the Twitch bot.
//
// On first run it creates a placeholder config.json (if missing) and
// exits with an instructive message. With a valid config it runs the
// OAuth user-token flow (which opens a browser), constructs the Helix
// API client, joins the configured channel via IRC, and dispatches chat
// commands. Ctrl-C triggers a clean shutdown.
package main

import (
	"encoding/json"
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

const configPath = "config.json"

// Config mirrors the fields documented in the README.
type Config struct {
	Username      string `json:"username"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	Channel       string `json:"channel"`
	FyrewireKey   string `json:"fyrewire_key,omitempty"`
	ValUser       string `json:"val_user,omitempty"`
	ValTag        string `json:"val_tag,omitempty"`
	ValRegion     string `json:"val_region,omitempty"`
	DiscordLink   string `json:"discord_link,omitempty"`
	TikTokLink    string `json:"tiktok_link,omitempty"`
	InstagramLink string `json:"instagram_link,omitempty"`
	TwitterLink   string `json:"twitter_link,omitempty"`
	YouTubeLink   string `json:"youtube_link,omitempty"`
	DonateLink    string `json:"donate_link,omitempty"`
	Nickname      string `json:"nickname,omitempty"`
}

// placeholderConfig is written when config.json does not exist.
const placeholderConfig = `{
  "username": "my_bot_account_name",
  "client_id": "ab1cdef23ghijk45lmnopq67rstu",
  "client_secret": "abc1d2efg45hijk67lmnopqrstuvwx",
  "channel": "the_streamer_channel_name",
  "fyrewire_key": "",
  "val_user": "",
  "val_tag": "",
  "val_region": "",
  "discord_link": "",
  "tiktok_link": "",
  "instagram_link": "",
  "twitter_link": "",
  "youtube_link": "",
  "donate_link": "",
  "nickname": ""
}
`

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfg, err := loadOrCreateConfig(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.Username == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.Channel == "" {
		return errors.New("config.json is missing required fields (username, client_id, client_secret, channel)")
	}

	store, err := state.NewKVStorage("state.db")
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	defer store.Close()

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

	handler := command.NewCommandHandler(apiClient)
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

// loadOrCreateConfig reads config.json. If the file does not exist, it
// writes a placeholder and returns an error so the user can fill it in.
func loadOrCreateConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := os.WriteFile(path, []byte(placeholderConfig), 0o644); writeErr != nil {
			return nil, fmt.Errorf("write placeholder: %w", writeErr)
		}
		return nil, fmt.Errorf("created placeholder %s — fill in your credentials and run again", path)
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}
