// Package command contains command routing, handling, and dispatch.
package command

import (
	"log"
	"strings"
	"sync"
	"time"
	"twitchbotv2/internal/twitch"
)

// CommandFunc is the function signature for a command handler
type CommandFunc func(user twitch.User, args []string) (string, error)

// CommandHandler manages and executes commands
type CommandHandler struct {
	commands     map[string]CommandFunc
	userMap      map[string]time.Time
	apiClient    *twitch.APIClient
	userMapMutex sync.Mutex
}

// NewCommandHandler creates a new command handler
func NewCommandHandler() *CommandHandler {
	apiClient := twitch.GetApiClient()
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

func (h *CommandHandler) handle(message twitch.Message) {
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

	if out != "" {
		h.apiClient.SendChatMessage(user.ID, out)
	}
}
