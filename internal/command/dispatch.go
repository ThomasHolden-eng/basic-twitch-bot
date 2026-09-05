package command

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/script"
)

// CommandDef is one entry in a command script file.
type CommandDef struct {
	// Name is the command's primary trigger, without a leading "!".
	Name string `json:"name"`
	// Aliases are additional triggers that run the same response.
	Aliases []string `json:"aliases,omitempty"`
	// Response is a script to run when the command is triggered.
	Response string `json:"response"`
	// Trigger is "command" (default, a chat command) or "redeem" (a
	// channel-point redemption).
	Trigger string `json:"trigger,omitempty"`
	// ModOnly restricts the command to moderators/the broadcaster.
	ModOnly bool `json:"modOnly,omitempty"`
}

// LoadCommands reads a JSON command script of {name, response-template, permissions} entries
// from path and registers every definition it contains against h. To be called once at startup.
//
// The response templates are evaluated at runtime by the internal script interpreter.
func LoadCommands(h *CommandHandler, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading command script %s: %w", path, err)
	}

	var defs []CommandDef
	if err := json.Unmarshal(data, &defs); err != nil {
		return fmt.Errorf("parsing command script %s: %w", path, err)
	}

	seen := make(map[string]bool)
	for _, def := range defs {
		if err := validateDef(def); err != nil {
			return fmt.Errorf("command %q: %w", def.Name, err)
		}
		for _, name := range append([]string{def.Name}, def.Aliases...) {
			key := strings.ToLower(name)
			if seen[key] {
				return fmt.Errorf("duplicate command trigger %q", name)
			}
			seen[key] = true
		}
		registerDef(h, def)
	}
	return nil
}

// validateDef returns an error if a command definition is malformed.
func validateDef(def CommandDef) error {
	if strings.TrimSpace(def.Name) == "" {
		return fmt.Errorf("missing name")
	}
	if strings.TrimSpace(def.Response) == "" {
		return fmt.Errorf("missing response")
	}
	if def.Trigger != "" && def.Trigger != "command" && def.Trigger != "redeem" {
		return fmt.Errorf("trigger must be \"command\" or \"redeem\", got %q", def.Trigger)
	}
	// Catch malformed templates (unbalanced "$(") at load time rather
	// than the first time a viewer runs the command.
	probe := &script.Context{Env: &script.Env{Config: map[string]string{}}}
	if _, err := script.Evaluate(def.Response, probe); err != nil && strings.Contains(err.Error(), "unclosed") {
		return err
	}
	return nil
}

// registerDef registers a single command definition.
func registerDef(h *CommandHandler, def CommandDef) {
	fn := func(h *CommandHandler, user chat.User, args []string) (string, error) {
		if def.ModOnly && !isMod(user) {
			return fmt.Sprintf("Sorry @%s, you don't have permission to use that command.", user.Name), nil
		}

		ctx := &script.Context{
			Username:    user.Name,
			UserID:      user.ID,
			Args:        args,
			IsMod:       isMod(user),
			CommandName: def.Name,
			Env:         buildEnv(h),
		}
		return script.Evaluate(def.Response, ctx)
	}

	names := append([]string{def.Name}, def.Aliases...)
	for _, name := range names {
		if def.Trigger == "redeem" {
			h.RegisterRedeem(name, fn)
		} else {
			h.Register(name, fn)
		}
	}
}

// buildEnv initialises the interpreter environment by wiring through
// the command handler and helper functions.
func buildEnv(h *CommandHandler) *script.Env {
	env := &script.Env{
		ChannelName:    h.ChannelName(),
		Nickname:       h.Nickname(),
		Config:         map[string]string{},
		FormatDuration: formatDuration,
		Pluralize:      pluralize,
	}

	if cfg, err := requireConfig(h); err == nil {
		env.Config = map[string]string{
			"discord":   cfg.DiscordLink,
			"donate":    cfg.DonateLink,
			"tiktok":    cfg.TikTokLink,
			"instagram": cfg.InstagramLink,
			"twitter":   cfg.TwitterLink,
			"youtube":   cfg.YouTubeLink,
			"valuser":   cfg.ValUser,
			"valtag":    cfg.ValTag,
			"valregion": cfg.ValRegion,
		}
	}

	if h.database != nil {
		env.Counter = func(name string, delta int) (int, error) {
			if delta != 0 {
				if err := h.database.IncrementInt(name, delta); err != nil {
					return 0, err
				}
			}
			return h.database.ReadInt(name)
		}
	}

	if h.apiClient != nil {
		env.UrlFetch = h.apiClient.UrlFetch
	}

	if h.apiClient != nil && h.broadcaster != nil {
		env.GetViewers = func() (int, error) {
			return h.apiClient.GetViewerNumber(h.ChannelName())
		}

		env.GetUptime = func() (time.Duration, bool, error) {
			stream, err := h.apiClient.GetStream(h.ChannelName())
			if err != nil {
				return 0, false, err
			}
			if stream == nil {
				return 0, false, nil
			}
			return time.Since(stream.StartedAt), true, nil
		}

		env.CreateClip = func() (string, error) {
			return h.apiClient.CreateClip(h.broadcaster.ID)
		}

		env.GetFollowers = func() (int, error) {
			return h.apiClient.GetFollowerNumber(h.broadcaster.ID)
		}

		env.ResolveUser = func(login string) (string, error) {
			u, err := h.apiClient.GetUserByLogin(login)
			if err != nil {
				return "", err
			}
			return u.ID, nil
		}

		env.GetFollowage = func(userID string) (time.Duration, bool, error) {
			follower, err := h.apiClient.GetFollowSince(h.broadcaster.ID, userID)
			if err != nil {
				return 0, false, err
			}
			if follower == nil {
				return 0, false, nil
			}
			return time.Since(follower.FollowedAt), true, nil
		}
	}

	return env
}
