package command

import (
	"log"
	"twitchbotv2/internal/twitch"
)

func readAndRoute(username, channel string, disconnect chan bool) {
	messageChannel := make(chan twitch.Message)
	twitchClient := twitch.NewTwitchClient(username, channel, disconnect, messageChannel)
	commandHandler := NewCommandHandler()

	if err := twitchClient.Connect(); err != nil {
		log.Fatal(err)
	}

	for {
		select {
		case msg := <-messageChannel:
			commandHandler.handle(msg)
		case <-disconnect:
			return
		}
	}
}
