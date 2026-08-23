package command

import (
	"log"
	"sync"
	"time"
	"twitchbotv2/internal/chat"
)

// CommandWheel is a structure to whole information necessary for looping commands
type CommandWheel struct {
	commands    []CommandFunc // commands is the list of commands on rotation
	timespan    time.Duration // timespan is the time between commands
	pointer     int           // pointer points to the current command to run
	pointerLock sync.Mutex    // pointerLock is a mutex for the pointer

	handler *CommandHandler // handler is the command handler

	exit chan bool // exit is the channel to exit the loop
}

// NewCommandWheel creates a new wheel of timed commands
func NewCommandWheel(
	timespan time.Duration, handler *CommandHandler,
	connection chan bool, commandStrings ...string) *CommandWheel {

	commands := make([]CommandFunc, 0, len(commandStrings))
	for _, command := range commandStrings {
		cmdFunc, ok := handler.commands[command]
		if ok {
			commands = append(commands, cmdFunc)
		}
	}

	handler.messagesSent = 0

	return &CommandWheel{
		commands: commands,
		timespan: timespan,
		pointer:  0,

		handler: handler,
		exit:    connection}
}

// startTimedCommands starts the process to send timed commands to chat
func (cw *CommandWheel) StartTimedCommands() {
	bot := chat.User{
		ID:     cw.handler.apiClient.UserID,
		Name:   cw.handler.config.Username,
		Badges: map[string]string{}}

	if cw.timespan < time.Second || len(cw.commands) <= 0 {
		log.Println("No timed commands to run.")
		return
	}

	ticker := time.NewTicker(cw.timespan)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			cw.handler.messagesSentMutex.Lock()
			if cw.handler.messagesSent < 3 {
				cw.handler.messagesSentMutex.Unlock()
				continue
			}
			cw.handler.messagesSent = 0
			cw.handler.messagesSentMutex.Unlock()

			go cw.sendTimerMessage(bot)
		case <-cw.exit:
			log.Println("Timed command runner successfully terminated.")
			return
		}
	}
}

// sendTimerMessage sends the current pointed to message to chat
func (cw *CommandWheel) sendTimerMessage(bot chat.User) {
	cw.pointerLock.Lock()
	defer cw.pointerLock.Unlock()
	log.Printf("Sending timed command at pos %d", cw.pointer)
	cw.handler.handleSend(cw.commands[cw.pointer], bot, []string{})

	cw.pointer = (cw.pointer + 1) % len(cw.commands)
}
