// Package command contains command routing, handling, and dispatch.
//
// Wiring (constructing the chat client, the API client, the handler, and
// connecting them) lives in the application's main package, not here.
// This package only owns the registration surface, the per-user debounce,
// and the dispatch loop.
package command

import (
	"log"
	"strings"
	"sync"
	"time"

	"twitchbotv2/internal/api"
	"twitchbotv2/internal/chat"
)

// CommandFunc is the function signature for a command handler
type CommandFunc func(user chat.User, args []string) (string, error)

// CommandHandler manages and executes commands
type CommandHandler struct {
	commands     map[string]CommandFunc
	userMap      map[string]time.Time
	apiClient    *api.APIClient
	userMapMutex sync.Mutex
}

// NewCommandHandler creates a new command handler.
//
// apiClient is used to send responses back to chat. It may be nil in
// tests; in that case responses are not sent (they are still computed).
func NewCommandHandler(apiClient *api.APIClient) *CommandHandler {
	return &CommandHandler{
		commands:  make(map[string]CommandFunc),
		userMap:   make(map[string]time.Time),
		apiClient: apiClient,
	}
}

// Register adds a new command to the handler
func (h *CommandHandler) Register(name string, fn CommandFunc) {
	h.commands[name] = fn
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

	_, ok := h.userMap[user.Name]
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

	out, err := cmd(user, args)
	if err != nil {
		log.Println(err)
		return
	}

	if out != "" && h.apiClient != nil {
		if err := h.apiClient.SendChatMessage(user.ID, out); err != nil {
			log.Printf("send chat message: %v", err)
		}
	}
}
