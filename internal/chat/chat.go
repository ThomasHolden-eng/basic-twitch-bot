// Package chat contains the Twitch IRC chat client.
//
// It opens a TLS connection to the Twitch IRC server, joins a single
// channel, and forwards parsed PRIVMSG lines onto a Go channel for
// downstream consumption (typically by a command dispatcher).
package chat

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"time"
	"twitchbotv2/internal/api"
)

const (
	twitchIRCServer = "irc.chat.twitch.tv:6697" // The Twitch IRC server
)

// User holds information about a user sending a message
type User struct {
	ID     string            // User ID
	Name   string            // Display name
	Badges map[string]string // Badge information
}

// Message is a chat message routed to the dispatcher.
type Message struct {
	User User   // User who sent the message
	Text string // Message text
}

// TwitchClient handles the connection to Twitch IRC.
type TwitchClient struct {
	conn           net.Conn          // The connection to the Twitch IRC server
	username       string            // The username of the bot
	channel        string            // The channel to join
	apiClient      *api.APIClient    //
	userToken      *api.TokenManager //
	messageChannel chan Message      // The channel for incoming messages
	disconnect     chan bool         // The channel to signal disconnection
}

// NewTwitchClient creates a new Twitch client.
//
// userToken must be the OAuth user token used to authenticate the IRC
// connection (sent as PASS oauth:...). Pass nil to skip the IRC login
// step (useful in tests that only exercise parseMessage).
func NewTwitchClient(
	username, channel string,
	apiClient *api.APIClient,
	userToken *api.TokenManager,
	disconnect chan bool,
	messageChannel chan Message,
) *TwitchClient {
	return &TwitchClient{
		username:       sanitizeInput(username),
		channel:        sanitizeInput(channel),
		apiClient:      apiClient,
		userToken:      userToken,
		disconnect:     disconnect,
		messageChannel: messageChannel,
	}
}

// Connect establishes a connection to the Twitch IRC server.
func (c *TwitchClient) Connect() error {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp", twitchIRCServer, &tls.Config{},
	)
	if err != nil {
		return err
	}
	c.conn = conn

	// Request IRCv3 tags, which include badge information
	c.sendCommand("CAP REQ :twitch.tv/tags\r\n")

	if c.userToken == nil {
		return fmt.Errorf("chat: cannot connect without a user token")
	}
	passCmd := fmt.Sprintf("PASS %s\r\n", "oauth:"+c.userToken.Get())
	nickCmd := fmt.Sprintf("NICK %s\r\n", c.username)
	c.sendCommand(passCmd)
	c.sendCommand(nickCmd)

	joinCmd := fmt.Sprintf("JOIN %s\r\n", "#"+c.channel)
	c.sendCommand(joinCmd)

	if c.apiClient != nil {
		broadcaster, err := c.apiClient.GetUserByLogin(c.channel)
		if err != nil {
			return err
		}
		go c.readMessages(broadcaster.Login)
	} else {
		go c.readMessages(c.channel)
	}

	go c.heartbeat()

	return nil
}

// Close closes the connection to the Twitch IRC server
func (c *TwitchClient) Close() {
	if c.conn != nil {
		c.conn.Close()
	}
}

// sendCommand sends a raw command to the IRC server
func (c *TwitchClient) sendCommand(command string) {
	if c.conn == nil {
		return
	}
	c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := fmt.Fprint(c.conn, command)
	if err != nil {
		log.Printf("Error sending command, '%s', to the IRC server: %v", command, err)
	}
}

// heartbeat ensures the connection is still alive using ping pong to twitch
func (c *TwitchClient) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.sendCommand("PING :tmi.twitch.tv\r\n")
		case <-c.disconnect:
			return
		}
	}
}

// readMessages continuously reads messages from the IRC server
func (c *TwitchClient) readMessages(channelName string) {
	reader := bufio.NewReader(c.conn)
	for {
		if c.conn == nil {
			log.Println("Connection is not initialised.")
			c.triggerDisconnect()
			return
		}

		c.conn.SetReadDeadline(time.Now().Add(time.Second * 50))
		line, err := reader.ReadString('\n')
		if err != nil {
			log.Printf("Error reading from line: %v", err)
			c.triggerDisconnect()
			return
		}
		line = strings.TrimSpace(line)

		// Handle PING PONG to keep connection alive
		if strings.HasPrefix(line, "PING") {
			c.sendCommand("PONG :tmi.twitch.tv\r\n")
			continue
		}

		if strings.Contains(line, "PRIVMSG") {
			user, message, channel := parseMessage(line)
			if channelName == channel {
				select {
				case c.messageChannel <- Message{User: user, Text: message}:
				case <-c.disconnect:
					log.Printf("disconnecting: clean exit")
					c.triggerDisconnect()
					return
				}
			}
		}
	}
}

func (c *TwitchClient) triggerDisconnect() {
	func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("Recovered from race in TwitchClient")
			}
		}()

		select {
		case <-c.disconnect:
		default:
			close(c.disconnect)
		}
	}()

	c.Close()
}

// parseMessage extracts user info and the message from a raw IRC line
func parseMessage(line string) (User, string, string) {
	user := User{Badges: make(map[string]string)}
	var message string
	var channel string

	parts := strings.SplitN(line, " ", 4)

	if len(parts) < 4 {
		return User{}, "", ""
	}
	// parts[0] = tags, parts[1] = :user!user@user.tmi.twitch.tv, parts[2] = PRIVMSG, parts[3] = #channel :message

	if strings.HasPrefix(parts[0], "@") {
		tagsRaw := strings.TrimPrefix(parts[0], "@")
		tags := strings.Split(tagsRaw, ";")
		for _, tag := range tags {
			tagParts := strings.SplitN(tag, "=", 2)
			if len(tagParts) == 2 {
				switch tagParts[0] {
				case "user-id":
					user.ID = tagParts[1]
				case "display-name":
					user.Name = tagParts[1]
				case "badges":
					// badges look like: broadcaster/1,moderator/1
					badgeParts := strings.Split(tagParts[1], ",")
					for _, badge := range badgeParts {
						b := strings.SplitN(badge, "/", 2)
						if len(b) == 2 {
							user.Badges[b[0]] = b[1]
						}
					}
				}
			}
		}
	}

	if user.Name == "" {
		nameParts := strings.Split(parts[1], "!")
		user.Name = strings.TrimPrefix(nameParts[0], ":")
	}

	messageParts := strings.SplitN(parts[3], ":", 2)
	if len(messageParts) > 1 {
		channel = strings.TrimSpace(messageParts[0])
		channel = strings.TrimPrefix(channel, "#")

		message = messageParts[1]
	}

	return user, message, channel
}

// sanitizeInput removes newline characters to prevent CRLF injection.
func sanitizeInput(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
