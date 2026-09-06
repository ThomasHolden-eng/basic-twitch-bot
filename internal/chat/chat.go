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
	"twitchbotv2/internal/disconnect"
)

const (
	twitchIRCServer = "irc.chat.twitch.tv:6697" // The Twitch IRC server

	initialReconnectBackoff = time.Second
	maxReconnectBackoff     = 30 * time.Second
)

// User holds information about a user sending a message
type User struct {
	ID     string            // User ID
	Name   string            // Display name
	Badges map[string]string // Badge information
	IsMod  bool              // True if the user is a moderator (or the channel broadcaster)
}

// Message is a chat message routed to the dispatcher.
type Message struct {
	User User   // User who sent the message
	Text string // Message text
}

// TwitchClient handles the connection to Twitch IRC.
type TwitchClient struct {
	conn           net.Conn                 // The connection to the Twitch IRC server
	username       string                   // The username of the bot
	channel        string                   // The channel to join
	apiClient      *api.APIClient           //
	userToken      *api.TokenManager        //
	disconnect     *disconnect.Disconnector // The channel to signal disconnection
	messageChannel chan Message             // The channel for incoming messages
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
	disconnect *disconnect.Disconnector,
	messageChannel chan Message,
) *TwitchClient {
	return &TwitchClient{
		username: sanitizeInput(username),
		// Twitch IRC channel names are always lowercase. JOIN and the
		// PRIVMSG channel Twitch echoes back are both lowercase, so
		// normalizing here keeps later comparisons correct regardless
		// of how the caller supplied the channel name.
		channel:        strings.ToLower(sanitizeInput(channel)),
		apiClient:      apiClient,
		userToken:      userToken,
		disconnect:     disconnect,
		messageChannel: messageChannel,
	}
}

// Connect establishes a connection to the Twitch IRC server.
func (c *TwitchClient) Connect() error {
	if err := c.dialAndJoin(); err != nil {
		return err
	}

	channelName := c.channel
	if c.apiClient != nil {
		broadcaster, err := c.apiClient.GetUserByLogin(c.channel)
		if err != nil {
			return err
		}
		channelName = strings.ToLower(broadcaster.Login)
	}

	go c.readMessages(channelName)
	go c.heartbeat()

	return nil
}

// dialAndJoin opens the TLS connection, requests capabilities, logs in,
// and joins the configured channel. It is used both by the initial
// Connect and by readMessages when recovering from a transient
// connection loss.
func (c *TwitchClient) dialAndJoin() error {
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

// heartbeat sends a client-initiated PING every 30 seconds.
//
// This is not just a courtesy: readMessages resets its read deadline to
// 50 seconds on every loop iteration, and that deadline is only pushed
// forward by receiving *some* line from the server. Twitch's own
// idle-client PING arrives roughly every 5 minutes, which is longer
// than our read deadline. Sending our own PING (and receiving Twitch's
// PONG in reply) generates the traffic needed to keep the read deadline
// from firing during quiet channels. The resulting PONG line doesn't
// match "PING" or "PRIVMSG" in readMessages' loop, so it's read (which
// is what matters) and then harmlessly ignored.
func (c *TwitchClient) heartbeat() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.sendCommand("PING :tmi.twitch.tv\r\n")
		case <-c.disconnect.Done():
			return
		}
	}
}

// readMessages continuously reads messages from the IRC server.
//
// On a transient read error (dropped connection, timeout, etc.) it
// attempts to reconnect and rejoin with exponential backoff, up to
// maxReconnectBackoff between attempts, rather than giving up
// immediately. An externally requested shutdown (c.disconnect closed
// by the caller) always takes priority over reconnecting.
func (c *TwitchClient) readMessages(channelName string) {
	if c.conn == nil {
		log.Println("Connection is not initialised.")
		c.triggerDisconnect()
		return
	}

	reader := bufio.NewReader(c.conn)
	backoff := initialReconnectBackoff

	for {
		if c.conn == nil {
			log.Println("Connection is not initialised.")
			c.triggerDisconnect()
			return
		}

		c.conn.SetReadDeadline(time.Now().Add(time.Second * 50))
		line, err := reader.ReadString('\n')
		if err != nil {
			// If the caller already asked us to shut down, honor that
			// instead of trying to reconnect.
			select {
			case <-c.disconnect.Done():
				return
			default:
			}

			log.Printf("chat: read error, attempting reconnect: %v", err)
			c.Close()

			if rerr := c.dialAndJoin(); rerr != nil {
				log.Printf("chat: reconnect attempt failed: %v", rerr)
				select {
				case <-time.After(backoff):
				case <-c.disconnect.Done():
					return
				}
				if backoff < maxReconnectBackoff {
					backoff *= 2
					if backoff > maxReconnectBackoff {
						backoff = maxReconnectBackoff
					}
				}
				continue
			}

			log.Println("chat: reconnected to Twitch IRC")
			reader = bufio.NewReader(c.conn)
			backoff = initialReconnectBackoff
			continue
		}

		backoff = initialReconnectBackoff
		line = strings.TrimSpace(line)

		// Handle PING PONG to keep connection alive
		if strings.HasPrefix(line, "PING") {
			c.sendCommand("PONG :tmi.twitch.tv\r\n")
			continue
		}

		if strings.Contains(line, "PRIVMSG") {
			user, message, channel := parseMessage(line, c.username)
			if message == "" {
				continue
			}
			if channelName == channel {
				select {
				case c.messageChannel <- Message{User: user, Text: message}:
				case <-c.disconnect.Done():
					log.Printf("disconnecting: clean exit")
					c.triggerDisconnect()
					return
				}
			}
		}
	}
}

// triggerDisconnect closes the connection to the Twitch IRC server
func (c *TwitchClient) triggerDisconnect() {
	c.disconnect.Trigger()

	c.Close()
}

// parseMessage extracts user info and the message from a raw IRC line.
//
// It tolerates lines both with and without a leading IRCv3 tags segment
// (Twitch always sends tags on PRIVMSG once twitch.tv/tags is
// requested, but this keeps the parser correct for any other line
// shape it might be handed, e.g. in tests). Tag values are unescaped
// per the IRCv3 spec so that display names, badge lists, etc. containing
// escaped spaces, semicolons, or backslashes are decoded correctly
// rather than corrupting the split.
func parseMessage(line, botname string) (User, string, string) {
	remainder := line

	var tags map[string]string
	if strings.HasPrefix(remainder, "@") {
		sp := strings.IndexByte(remainder, ' ')
		if sp == -1 {
			return User{}, "", ""
		}
		tags = parseIRCTags(remainder[1:sp])
		remainder = remainder[sp+1:]
	}

	// prefix = :user!user@user.tmi.twitch.tv, command = PRIVMSG,
	// rest = "#channel :message"
	parts := strings.SplitN(remainder, " ", 3)
	if len(parts) < 3 {
		return User{}, "", ""
	}
	prefix, command, rest := parts[0], parts[1], parts[2]

	if !strings.EqualFold(command, "PRIVMSG") {
		return User{}, "", ""
	}

	sender := strings.TrimPrefix(prefix, ":")
	if bang := strings.IndexByte(sender, '!'); bang != -1 {
		sender = sender[:bang]
	}
	if botname != "" && strings.EqualFold(sender, botname) {
		return User{}, "", ""
	}

	user := User{Badges: make(map[string]string)}
	if tags != nil {
		user.ID = tags["user-id"]
		user.Name = tags["display-name"]
		// Twitch sets this to "1" for BOTH regular mods and lead mods.
		user.IsMod = tags["mod"] == "1"

		if badgesRaw, ok := tags["badges"]; ok && badgesRaw != "" {
			// badges look like: broadcaster/1,moderator/1
			for _, badge := range strings.Split(badgesRaw, ",") {
				b := strings.SplitN(badge, "/", 2)
				if len(b) == 2 {
					user.Badges[b[0]] = b[1]
				}
			}
		}
	}

	if user.Name == "" {
		user.Name = sender
	}

	// The channel broadcaster doesn't get mod="1" on their own channel.
	if _, isBroadcaster := user.Badges["broadcaster"]; isBroadcaster {
		user.IsMod = true
	}

	messageParts := strings.SplitN(rest, ":", 2)
	if len(messageParts) < 2 {
		return User{}, "", ""
	}
	channel := strings.TrimSpace(messageParts[0])
	channel = strings.TrimPrefix(channel, "#")
	channel = strings.ToLower(channel)
	message := messageParts[1]

	log.Println(user, message, channel)
	return user, message, channel
}

// parseIRCTags parses the raw IRCv3 tags segment (without the leading
// '@') into a key/value map, unescaping values per the IRCv3 spec.
func parseIRCTags(raw string) map[string]string {
	tags := make(map[string]string)
	for _, tag := range strings.Split(raw, ";") {
		if tag == "" {
			continue
		}
		kv := strings.SplitN(tag, "=", 2)
		key := kv[0]
		var val string
		if len(kv) == 2 {
			val = unescapeIRCTagValue(kv[1])
		}
		tags[key] = val
	}
	return tags
}

// unescapeIRCTagValue decodes the backslash escapes IRCv3/Twitch use in
// tag values: \s (space), \: (semicolon), \\ (backslash), \r, \n. Any
// other escaped character is passed through literally, and a trailing
// unmatched backslash is dropped, per spec.
func unescapeIRCTagValue(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 's':
				b.WriteByte(' ')
			case ':':
				b.WriteByte(';')
			case '\\':
				b.WriteByte('\\')
			case 'r':
				b.WriteByte('\r')
			case 'n':
				b.WriteByte('\n')
			default:
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// sanitizeInput removes newline characters to prevent CRLF injection.
func sanitizeInput(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}
