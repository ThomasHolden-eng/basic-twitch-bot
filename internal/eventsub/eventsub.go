// Package eventsub contains a Twitch EventSub WebSocket client.
//
// It opens a raw TLS connection to Twitch's EventSub WebSocket endpoint,
// performs the WebSocket upgrade handshake by hand (no websocket library —
// same "raw net/tls + bufio" approach as the chat package), subscribes to
// channel point redemption events over the Helix API, and forwards parsed
// redemption events onto a Go channel for downstream consumption.
package eventsub

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"twitchbotv2/internal/api"
	"twitchbotv2/internal/disconnect"
)

const (
	eventSubHost = "eventsub.wss.twitch.tv:443" // Twitch's EventSub WebSocket server
	eventSubPath = "/ws"

	helixSubscriptionsURL = "https://api.twitch.tv/helix/eventsub/subscriptions"

	// GUID from RFC 6455, used to validate the server's handshake response.
	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

	// WebSocket frame opcodes (RFC 6455 section 5.2).
	opContinuation byte = 0x0
	opText         byte = 0x1
	opBinary       byte = 0x2
	opClose        byte = 0x8
	opPing         byte = 0x9
	opPong         byte = 0xA

	defaultReadDeadline = 90 * time.Second // used before the session_welcome tells us the real keepalive timeout
)

// RedemptionEvent is a parsed channel.channel_points_custom_reward_redemption.add event.
type RedemptionEvent struct {
	UserID      string // ID of the viewer who redeemed
	UserLogin   string // Login name of the viewer
	UserName    string // Display name of the viewer
	RewardID    string // ID of the custom reward
	RewardTitle string // Title of the custom reward
	RewardCost  int    // Channel point cost of the reward
	UserInput   string // Text the viewer entered, if the reward requires it
	Status      string // "unfulfilled", "fulfilled", or "canceled"
	RedeemedAt  string // RFC3339 timestamp of the redemption
}

// EventSubClient handles the connection to Twitch's EventSub WebSocket API.
type EventSubClient struct {
	conn   *tls.Conn     // The connection to the EventSub server
	reader *bufio.Reader // Buffered reader wrapping conn, reused across the handshake and frame reads

	clientID      string            // Twitch application Client-Id, required on every Helix call
	channel       string            // Broadcaster's login name
	broadcasterID string            // Resolved broadcaster user ID
	apiClient     *api.APIClient    //
	userToken     *api.TokenManager // Broadcaster token; must include channel:read:redemptions

	httpClient *http.Client

	redemptionChan chan RedemptionEvent     // The channel for incoming redemption events
	disconnect     *disconnect.Disconnector // The channel to signal disconnection

	keepaliveTimeout time.Duration // Set from the session_welcome payload
	reconnecting     bool          // state variable to track reconnects
}

// NewEventSubClient creates a new EventSub client.
//
// clientID is the bot/application's Client-Id. userToken must be an OAuth
// user token belonging to the broadcaster (or a token otherwise authorized
// for channel:read:redemptions on that channel).
func NewEventSubClient(
	clientID, channel string,
	apiClient *api.APIClient,
	userToken *api.TokenManager,
	disconnect *disconnect.Disconnector,
	redemptionChan chan RedemptionEvent,
) *EventSubClient {
	return &EventSubClient{
		clientID:       clientID,
		channel:        channel,
		apiClient:      apiClient,
		userToken:      userToken,
		disconnect:     disconnect,
		redemptionChan: redemptionChan,
		httpClient:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Connect resolves the broadcaster ID, opens the EventSub WebSocket
// connection, and starts listening for redemption events.
func (e *EventSubClient) Connect() error {
	if e.apiClient == nil || e.userToken == nil {
		return fmt.Errorf("eventsub: apiClient and userToken are required")
	}

	broadcaster, err := e.apiClient.GetUserByLogin(e.channel)
	if err != nil {
		return fmt.Errorf("eventsub: resolving broadcaster: %w", err)
	}
	e.broadcasterID = broadcaster.ID

	if err := e.dial(eventSubHost, eventSubPath); err != nil {
		return err
	}

	go e.readLoop()

	return nil
}

// Close closes the connection to the EventSub server.
func (e *EventSubClient) Close() {
	if e.conn != nil {
		e.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = writeFrame(e.conn, opClose, nil)
		e.conn.Close()
	}
}

// dial opens a TLS connection to host and performs the WebSocket upgrade
// handshake against path, storing the resulting connection and reader on e.
func (e *EventSubClient) dial(host, path string) error {
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 10 * time.Second},
		"tcp", host, &tls.Config{},
	)
	if err != nil {
		return fmt.Errorf("eventsub: dialing %s: %w", host, err)
	}

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})

	// The same bufio.Reader is used for the handshake response and all
	// subsequent frame reads, since bufio may buffer bytes past the
	// handshake response that belong to the first frame.
	reader := bufio.NewReader(conn)
	if err := performHandshake(conn, reader, host, path); err != nil {
		conn.Close()
		return fmt.Errorf("eventsub: handshake: %w", err)
	}

	e.conn = conn
	e.reader = reader

	return nil
}

// performHandshake sends the HTTP Upgrade request and validates the
// server's 101 Switching Protocols response, per RFC 6455 section 4.
func performHandshake(conn *tls.Conn, reader *bufio.Reader, host, path string) error {
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		return err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)

	hostname := host
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		hostname = host[:idx]
	}

	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\n"+
			"Sec-WebSocket-Version: 13\r\n"+
			"\r\n",
		path, hostname, key,
	)

	if _, err := conn.Write([]byte(req)); err != nil {
		return err
	}

	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.Contains(statusLine, "101") {
		return fmt.Errorf("unexpected handshake status: %s", strings.TrimSpace(statusLine))
	}

	var acceptKey string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // blank line marks the end of the headers
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "Sec-WebSocket-Accept") {
			acceptKey = strings.TrimSpace(parts[1])
		}
	}

	if acceptKey != computeAcceptKey(key) {
		return fmt.Errorf("Sec-WebSocket-Accept did not match expected value")
	}

	return nil
}

// computeAcceptKey derives the expected Sec-WebSocket-Accept value for a
// given client key, per RFC 6455 section 1.3.
func computeAcceptKey(clientKey string) string {
	h := sha1.New()
	h.Write([]byte(clientKey + websocketGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// wsFrame is a single parsed WebSocket frame.
type wsFrame struct {
	opcode  byte
	fin     bool
	payload []byte
}

// readFrame reads and unmasks (if necessary) a single WebSocket frame.
func readFrame(r *bufio.Reader) (wsFrame, error) {
	head, err := readN(r, 2)
	if err != nil {
		return wsFrame{}, err
	}

	fin := head[0]&0x80 != 0
	opcode := head[0] & 0x0F
	masked := head[1]&0x80 != 0
	length := int64(head[1] & 0x7F)

	switch length {
	case 126:
		ext, err := readN(r, 2)
		if err != nil {
			return wsFrame{}, err
		}
		length = int64(binary.BigEndian.Uint16(ext))
	case 127:
		ext, err := readN(r, 8)
		if err != nil {
			return wsFrame{}, err
		}
		length = int64(binary.BigEndian.Uint64(ext))
	}

	var maskKey []byte
	if masked {
		maskKey, err = readN(r, 4)
		if err != nil {
			return wsFrame{}, err
		}
	}

	payload, err := readN(r, int(length))
	if err != nil {
		return wsFrame{}, err
	}

	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	return wsFrame{opcode: opcode, fin: fin, payload: payload}, nil
}

// writeFrame writes a single, masked WebSocket frame. Per RFC 6455, every
// frame sent from client to server MUST be masked. A write deadline is
// applied so a stalled connection (e.g. after the machine sleeps) fails
// fast instead of blocking forever.
func writeFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode} // FIN=1, RSV=0

	maskKey := make([]byte, 4)
	if _, err := rand.Read(maskKey); err != nil {
		return err
	}

	length := len(payload)
	switch {
	case length <= 125:
		header = append(header, 0x80|byte(length))
	case length <= 65535:
		ext := make([]byte, 2)
		binary.BigEndian.PutUint16(ext, uint16(length))
		header = append(header, 0x80|126)
		header = append(header, ext...)
	default:
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(length))
		header = append(header, 0x80|127)
		header = append(header, ext...)
	}
	header = append(header, maskKey...)

	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = payload[i] ^ maskKey[i%4]
	}

	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	defer conn.SetWriteDeadline(time.Time{}) // clear the deadline once done, so it doesn't affect unrelated future writes

	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(masked)
	return err
}

func readN(r *bufio.Reader, n int) ([]byte, error) {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

// readLoop continuously reads frames from the EventSub connection,
// reassembles fragmented messages, answers pings, and dispatches complete
// text messages to handleMessage. It runs entirely on a single goroutine,
// including during a session_reconnect swap, so no locking is needed
// around e.conn / e.reader.
func (e *EventSubClient) readLoop() {
	var messageOpcode byte
	var buffer []byte

	for {
		if e.conn == nil {
			log.Println("eventsub: connection is not initialised.")
			e.triggerDisconnect()
			return
		}

		deadline := defaultReadDeadline
		if e.keepaliveTimeout > 0 {
			deadline = e.keepaliveTimeout*2 + 5*time.Second
		}
		e.conn.SetReadDeadline(time.Now().Add(deadline))

		frame, err := readFrame(e.reader)
		if err != nil {
			log.Printf("eventsub: error reading frame: %v", err)
			e.triggerDisconnect()
			return
		}

		switch frame.opcode {
		case opPing:
			if err := writeFrame(e.conn, opPong, frame.payload); err != nil {
				log.Printf("eventsub: error sending pong: %v", err)
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = writeFrame(e.conn, opClose, nil)
			e.triggerDisconnect()
			return
		case opContinuation:
			buffer = append(buffer, frame.payload...)
		case opText, opBinary:
			messageOpcode = frame.opcode
			buffer = append([]byte{}, frame.payload...)
		default:
			continue
		}

		if !frame.fin {
			continue // more fragments to come
		}

		if messageOpcode == opText {
			e.handleMessage(buffer)
		}
		buffer = nil
	}
}

func (e *EventSubClient) triggerDisconnect() {
	e.disconnect.Trigger()

	e.Close()
}

// wsEnvelope is the outer shape shared by every EventSub WebSocket message.
type wsEnvelope struct {
	Metadata struct {
		MessageType string `json:"message_type"`
	} `json:"metadata"`
	Payload json.RawMessage `json:"payload"`
}

func (e *EventSubClient) handleMessage(data []byte) {
	var envelope wsEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		log.Printf("eventsub: error parsing message: %v", err)
		return
	}

	switch envelope.Metadata.MessageType {
	case "session_welcome":
		e.handleWelcome(envelope.Payload)
	case "session_keepalive":
		// No-op: just receiving this resets the read deadline via the loop above.
	case "notification":
		e.handleNotification(envelope.Payload)
	case "session_reconnect":
		e.handleReconnect(envelope.Payload)
	case "revocation":
		log.Printf("eventsub: subscription revoked: %s", string(envelope.Payload))
	default:
		log.Printf("eventsub: unhandled message type %q", envelope.Metadata.MessageType)
	}
}

func (e *EventSubClient) handleWelcome(payload json.RawMessage) {
	var p struct {
		Session struct {
			ID                      string `json:"id"`
			KeepaliveTimeoutSeconds int    `json:"keepalive_timeout_seconds"`
		} `json:"session"`
	}

	if e.reconnecting {
		e.reconnecting = false
		log.Printf("eventsub: reconnection successful")
		return // subscription already migrated by Twitch; nothing to do
	}

	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("eventsub: error parsing welcome payload: %v", err)
		return
	}

	e.keepaliveTimeout = time.Duration(p.Session.KeepaliveTimeoutSeconds) * time.Second

	if err := e.subscribeToRedemptions(p.Session.ID); err != nil {
		log.Printf("eventsub: error subscribing to redemptions: %v", err)
	}
}

// subscribeToRedemptions registers a channel.channel_points_custom_reward_redemption.add
// subscription over Helix, tied to this WebSocket session.
func (e *EventSubClient) subscribeToRedemptions(sessionID string) error {
	body := map[string]interface{}{
		"type":    "channel.channel_points_custom_reward_redemption.add",
		"version": "1",
		"condition": map[string]string{
			"broadcaster_user_id": e.broadcasterID,
		},
		"transport": map[string]string{
			"method":     "websocket",
			"session_id": sessionID,
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, helixSubscriptionsURL, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Client-Id", e.clientID)
	req.Header.Set("Authorization", "Bearer "+e.userToken.Get())
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("helix returned %s: %s", resp.Status, string(respBody))
	}

	return nil
}

func (e *EventSubClient) handleNotification(payload json.RawMessage) {
	var p struct {
		Subscription struct {
			Type string `json:"type"`
		} `json:"subscription"`
		Event struct {
			UserID     string `json:"user_id"`
			UserLogin  string `json:"user_login"`
			UserName   string `json:"user_name"`
			UserInput  string `json:"user_input"`
			Status     string `json:"status"`
			RedeemedAt string `json:"redeemed_at"`
			Reward     struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Cost  int    `json:"cost"`
			} `json:"reward"`
		} `json:"event"`
	}

	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("eventsub: error parsing notification: %v", err)
		return
	}

	if p.Subscription.Type != "channel.channel_points_custom_reward_redemption.add" {
		return // ignore subscription types we didn't ask for
	}

	select {
	case e.redemptionChan <- RedemptionEvent{
		UserID:      p.Event.UserID,
		UserLogin:   p.Event.UserLogin,
		UserName:    p.Event.UserName,
		RewardID:    p.Event.Reward.ID,
		RewardTitle: p.Event.Reward.Title,
		RewardCost:  p.Event.Reward.Cost,
		UserInput:   p.Event.UserInput,
		Status:      p.Event.Status,
		RedeemedAt:  p.Event.RedeemedAt,
	}:
	case <-e.disconnect.Done():
		log.Printf("disconnecting: dropped event")
		e.triggerDisconnect()
		return
	}
}

// handleReconnect follows a server-initiated reconnect: dial the new URL,
// swap it in for the active connection, then close the old one. Existing
// subscriptions carry over to the new session automatically, so there's no
// need to call subscribeToRedemptions again here.
func (e *EventSubClient) handleReconnect(payload json.RawMessage) {
	var p struct {
		Session struct {
			ReconnectURL string `json:"reconnect_url"`
		} `json:"session"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		log.Printf("eventsub: error parsing reconnect payload: %v", err)
		return
	}

	log.Printf("eventsub: server requested reconnect to %s", p.Session.ReconnectURL)

	host, path, err := parseWSURL(p.Session.ReconnectURL)
	if err != nil {
		log.Printf("eventsub: error parsing reconnect URL: %v", err)
		return
	}

	e.reconnecting = true
	oldConn := e.conn

	if err := e.dial(host, path); err != nil {
		log.Printf("eventsub: error reconnecting: %v", err)
		return
	}

	if oldConn != nil {
		oldConn.Close()
	}
}

// parseWSURL splits a "wss://host[:port]/path" URL into a dial-able
// host:port and a path, since we're not using net/url's full HTTP client.
func parseWSURL(raw string) (host, path string, err error) {
	trimmed := strings.TrimPrefix(raw, "wss://")
	if trimmed == raw {
		return "", "", fmt.Errorf("unsupported URL scheme: %s", raw)
	}

	idx := strings.Index(trimmed, "/")
	if idx == -1 {
		return trimmed + ":443", "/", nil
	}

	hostPart := trimmed[:idx]
	pathPart := trimmed[idx:]

	if !strings.Contains(hostPart, ":") {
		hostPart += ":443"
	}

	return hostPart, pathPart, nil
}
