// Package command contains command routing, handling, and dispatch.
//
// Wiring (constructing the chat client, the API client, the handler, and
// connecting them) lives in the application's main package, not here.
// This package only owns the registration surface, the per-user debounce,
// and the dispatch loop.
package command

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"twitchbotv2/internal/api"
	"twitchbotv2/internal/chat"
	"twitchbotv2/internal/eventsub"
	"twitchbotv2/internal/state"
)

// CommandFunc is the function signature for a command handler
type CommandFunc func(h *CommandHandler, user chat.User, args []string) (string, error)

// CommandHandler manages and executes commands
type CommandHandler struct {
	commands      map[string]CommandFunc
	redeemActions map[string]CommandFunc
	userMap       map[string]time.Time

	broadcaster *api.TwitchUser

	messagesSent int // messagesSent records the total messages sent since the last timed message

	database          *state.KVStorage
	config            *state.SafeConfig
	apiClient         *api.APIClient
	userMapMutex      sync.Mutex
	messagesSentMutex sync.Mutex
}

// NewCommandHandler creates a new command handler.
//
// It opens the SQLite state store, loads the safe config, and resolves
// the broadcaster (channel owner) so commands that need it don't have
// to. Any of these failing returns an error rather than nil, so the
// caller can fail loudly at startup instead of crashing on the first
// command.
//
// apiClient is used to send responses back to chat. It may be nil in
// tests; in that case responses are not sent (they are still computed).
func NewCommandHandler(
	apiClient *api.APIClient,
	database *state.KVStorage,
) (*CommandHandler, error) {
	cfg, err := state.LoadSafeConfig()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	var broadcaster *api.TwitchUser
	if apiClient != nil {
		broadcaster, err = apiClient.GetUserByLogin(cfg.Channel)
		if err != nil {
			return nil, fmt.Errorf("resolve broadcaster %q: %w", cfg.Channel, err)
		}
	}

	return &CommandHandler{
		commands:      make(map[string]CommandFunc),
		redeemActions: make(map[string]CommandFunc),
		userMap:       make(map[string]time.Time),
		broadcaster:   broadcaster,
		database:      database,
		config:        cfg,
		apiClient:     apiClient,
	}, nil
}

// Register adds a new command to the handler
func (h *CommandHandler) Register(name string, fn CommandFunc) {
	h.commands[name] = fn
}

// RegisterRedeem adds a new redeem based action to the handler
func (h *CommandHandler) RegisterRedeem(name string, fn CommandFunc) {
	h.redeemActions[name] = fn
}

// RemoveRecentUsers removes users with activity more than 1 second ago
func (h *CommandHandler) RemoveRecentUsers() {
	now := time.Now()
	h.userMapMutex.Lock()
	defer h.userMapMutex.Unlock()
	for user, timestamp := range h.userMap {
		if timestamp.Before(now.Add(-1 * time.Second)) {
			delete(h.userMap, user)
		}
	}
}

// Handle processes a single incoming chat message.
//
// It applies a 1-second per-user debounce, parses the command name and
// args, looks up the registered handler, and (if the handler returns a
// non-empty string) sends the result back to chat via the API client.
//
// Exported so the application's main loop can drive it directly.
func (h *CommandHandler) Handle(message chat.Message) {
	h.RemoveRecentUsers()

	user := message.User
	content := message.Text

	h.messagesSentMutex.Lock()
	h.messagesSent++
	h.messagesSentMutex.Unlock()

	h.userMapMutex.Lock()
	_, ok := h.userMap[user.Name]
	h.userMapMutex.Unlock()
	if !strings.HasPrefix(content, "!") || ok {
		return
	}

	parts := strings.Fields(content)
	commandName := strings.TrimPrefix(parts[0], "!")
	args := parts[1:]

	cmd, found := h.commands[commandName]
	if !found {
		log.Printf("command, '%s' not found", commandName)
		return
	}

	h.userMapMutex.Lock()
	h.userMap[user.Name] = time.Now()
	h.userMapMutex.Unlock()

	go h.handleSend(cmd, user, args)
}

// handleSend sends a response to chat
func (h *CommandHandler) handleSend(cmd CommandFunc, user chat.User, args []string) {
	out, err := cmd(h, user, args)
	if err != nil {
		log.Println(err)
		return
	}

	if out == "" || h.apiClient == nil {
		return
	}
	if h.broadcaster == nil {
		log.Printf("cannot send chat message: broadcaster not resolved")
		return
	}
	// Helix /chat/messages requires the channel owner's user ID as
	// broadcaster_id, not the sender's. The API client fills in the
	// bot's own UserID as sender_id internally.
	if err := h.apiClient.SendChatMessage(h.broadcaster.ID, out); err != nil {
		log.Printf("send chat message: %v", err)
	}
}

// ChannelName returns the broadcaster's login name, or empty if it has
// not been resolved yet. Command bodies should call this rather than
// reading h.broadcaster.Login directly so that nil-safety is uniform.
func (h *CommandHandler) ChannelName() string {
	if h.broadcaster == nil {
		return ""
	}
	return h.broadcaster.Login
}

// Nickname returns the configured streamer nickname, falling back to the
// broadcaster login when the nickname is empty. Commands use this in
// user-facing strings.
func (h *CommandHandler) Nickname() string {
	if h.config != nil && h.config.Nickname != "" {
		return h.config.Nickname
	}
	return h.ChannelName()
}

// HandleRedeem processes a single incoming redemption event
//
// if the handler returns a non-empty string sends the result back to
// chat via the API client.
//
// Exported so the application's main loop can drive it directly.
func (h *CommandHandler) HandleRedeem(event eventsub.RedemptionEvent) {
	h.RemoveRecentUsers()

	user := chat.User{ID: event.UserID, Name: event.UserName, Badges: make(map[string]string)}
	redeemName := event.RewardTitle

	log.Printf("%v", redeemName)

	cmd, found := h.redeemActions[redeemName]
	if !found {
		return
	}
	log.Printf("redeem command, '%s' found", redeemName)

	go h.handleSend(cmd, user, []string{})
}
