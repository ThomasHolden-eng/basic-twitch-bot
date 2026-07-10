package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

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

// SafeConfig is Config without fields that should not be exposed
type SafeConfig struct {
	Username      string `json:"username"`
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

const configPath = "config.json"

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

// loadOrCreateConfig reads config.json. If the file does not exist, it
// writes a placeholder and returns an error so the user can fill it in.
func LoadOrCreateConfig() (*Config, error) {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		if writeErr := os.WriteFile(configPath, []byte(placeholderConfig), 0o644); writeErr != nil {
			return nil, fmt.Errorf("write placeholder: %w", writeErr)
		}
		return nil, fmt.Errorf("created placeholder %s — fill in your credentials and run again", configPath)
	}
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}
	return &cfg, nil
}

// loadSafeConfig reads config.json. If the file does not exist, it
// returns nil.
func LoadSafeConfig() (*SafeConfig, error) {
	data, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("config is uninitialised")
	}
	if err != nil {
		return nil, err
	}

	var cfg SafeConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}
	return &cfg, nil
}
