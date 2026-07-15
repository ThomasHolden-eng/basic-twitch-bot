package command

import (
	"log"
	"time"
	"twitchbotv2/internal/chat"
)

// CommandWheel is a structure to whole information necessary for looping commands
type CommandWheel struct {
	commands []CommandFunc // commands is the list of commands on rotation
	timespan time.Duration // timespan is the time between commands
	pointer  int           // pointer points to the current command to run

	handler *CommandHandler

	exit chan bool
}

var messagesSent int // messagesSent records the total messages sent since the last timed message

// NewCommandWheel creates a new wheel of timed commands
func NewCommandWheel(
	timespan time.Duration, handler *CommandHandler,
	connection chan bool, commandStrings ...string) *CommandWheel {

	commands := make([]CommandFunc, 0, len(commandStrings))
	for _, command := range commandStrings {
		commands = append(commands, handler.commands[command])
	}

	messagesSent = 0

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

	for {
		select {
		case <-ticker.C:
			if messagesSent < 3 {
				continue
			}
			messagesSent = 0

			go cw.sendTimerMessage(bot)
		case <-cw.exit:
			log.Println("Timed command runner successfully terminated.")
			return
		}
	}
}

// sendTimerMessage sends the current pointed to message to chat
func (cw *CommandWheel) sendTimerMessage(bot chat.User) {
	response, err := cw.commands[cw.pointer](cw.handler, bot, []string{})
	if err != nil {
		log.Printf("Error executing timed command: %v", err)
	}
	if response != "" {
		log.Printf("Sending timed command at pos %d", cw.pointer)
		cw.handler.handleSend(cw.commands[cw.pointer], bot, []string{})
	}
	if err != nil {
		log.Printf("Error sending timed message in chat: %v", err)
	}
	cw.pointer = (cw.pointer + 1) % len(cw.commands)
}
