// Package api contains the Twitch Helix API client and supporting
// authentication (app access token, user OAuth, token refresh, rate
// limiting). It does not know about IRC or chat messages.
package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	helixBaseURL = "https://api.twitch.tv/helix" // The base URL for the Helix API
)

// APIClient holds the necessary information to communicate with the Twitch API
type APIClient struct {
	UserID          string
	clientID        string
	appTokenManager *TokenManager
	userToken       *TokenManager
	rateLimit       *tokenBucket
	httpClient      *http.Client
	tempClip        *clipStore
	clipLock        sync.Mutex
}

// NewAPIClient runs the OAuth user-token flow, sets up the app access
// token, resolves the bot's user ID, and starts the chat rate-limit
// token bucket. It returns the constructed client together with the user
// token so the caller can attach it to other components (e.g. the IRC
// client, which needs the user token for its PASS command).
func NewAPIClient(username, clientID, clientSecret string) (*APIClient, *TokenManager, error) {
	userToken, err := InitUserToken(clientID, clientSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("user OAuth: %w", err)
	}

	appToken, err := getAppAccessTokenManager(clientID, clientSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("app access token: %w", err)
	}

	client := &APIClient{
		clientID:        clientID,
		appTokenManager: appToken,
		userToken:       userToken,
		rateLimit:       &tokenBucket{},
		httpClient:      &http.Client{},
	}
	if err := client.setUserID(username); err != nil {
		return nil, nil, fmt.Errorf("resolve bot user id: %w", err)
	}
	go client.rateLimit.startTokenTimer()

	return client, userToken, nil
}

// TwitchUser represents a user object from the Twitch API
type TwitchUser struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

// follower represents the follower relationship data from the Twitch API
type follower struct {
	FollowedAt time.Time `json:"followed_at"`
}

// followerResponse is the top-level structure of the get users follows API response
type followerResponse struct {
	Data []follower `json:"data"`
}

// followerCountResponse is the structure for the total follower count from the API
type followerCountResponse struct {
	Total int `json:"total"`
}

// viewerCountResponse is the structure for the total viewer count from the API
type viewerCountResponse struct {
	Total int `json:"total"`
}

// stream represents a stream object from the Twitch API
type stream struct {
	StartedAt   time.Time `json:"started_at"`
	Type        string    `json:"type"`
	ViewerCount int       `json:"viewer_count"`
}

// clip represents a clips access points
type clip struct {
	ID       string `json:"id"`
	Edit_Url string `json:"edit_url"`
}

// clipStore is a container to hold a clip to prevent repeat clips
type clipStore struct {
	Url       string
	Timestamp int64
}

// streamResponse is the top-level structure of the get streams API response
type streamResponse struct {
	Data []stream `json:"data"`
}

// userResponse is the top-level structure of the get users API response
type userResponse struct {
	Data []TwitchUser `json:"data"`
}

// clipResponse is the top-level structure of the create clip API response
type clipResponse struct {
	Data []clip `json:"data"`
}

// twitchChannelUpdateRequest is the structure of a request to change stream details
type twitchChannelUpdateRequest struct {
	GameID string `json:"game_id"`
	Title  string `json:"title"`
}

// sendChatMessageRequest is the request body for sending a chat message
type sendChatMessageRequest struct {
	BroadcasterID string `json:"broadcaster_id"`
	SenderID      string `json:"sender_id"`
	Message       string `json:"message"`
}

// doRequestWithRetry performs an HTTP request with a retry mechanism.
func (c *APIClient) doRequestWithRetry(req *http.Request, maxRetriesOpt ...int) (*http.Response, error) {
	maxRetries := 4
	if len(maxRetriesOpt) > 0 {
		maxRetries = maxRetriesOpt[0]
	}

	var resp *http.Response
	var err error
	baseDelay := 500 * time.Millisecond

	for i := 0; i < maxRetries; i++ {
		resp, err = c.httpClient.Do(req)
		if err == nil {
			return resp, nil
		}

		delay := baseDelay * time.Duration(math.Pow(2, float64(i)))
		time.Sleep(delay)
	}

	return nil, fmt.Errorf("request failed after %d retries: %w", maxRetries, err)
}

// doAuthenticatedRequest builds, executes, and reads an authenticated HTTP request.
// It sets the standard Client-ID and Bearer token headers, retries on failure,
// and returns the raw response body.
func (c *APIClient) doAuthenticatedRequest(method, url string, body io.Reader, token string) ([]byte, int, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, err
	}

	if token != "" {
		req.Header.Add("Client-ID", c.clientID)
		req.Header.Add("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Add("Content-Type", "application/json")
	}

	resp, err := c.doRequestWithRetry(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, err
}

// setUserID validates the bot's token and stores its own User ID
func (c *APIClient) setUserID(login string) error {
	botUser, err := c.GetUserByLogin(login)
	if err != nil {
		log.Printf("Error setting bot id with string, '%s': %v", login, err)
		return err
	}

	c.UserID = botUser.ID
	return nil
}

// GetUserByLogin fetches user information by their login name
func (c *APIClient) GetUserByLogin(login string) (*TwitchUser, error) {
	login = url.QueryEscape(login)

	url := fmt.Sprintf("%s/users?login=%s", helixBaseURL, login)
	body, _, err := c.doAuthenticatedRequest("GET", url, nil, c.userToken.Get())
	if err != nil {
		return nil, err
	}

	var userResp userResponse
	if err := json.Unmarshal(body, &userResp); err != nil {
		return nil, err
	}

	if len(userResp.Data) == 0 {
		return nil, fmt.Errorf("user not found: %s", login)
	}

	return &userResp.Data[0], nil
}

// GetFollowSince fetches how long a user has followed a channel
func (c *APIClient) GetFollowSince(broadcasterID, userID string) (*follower, error) {
	broadcasterID = url.QueryEscape(broadcasterID)
	userID = url.QueryEscape(userID)

	url := fmt.Sprintf("%s/channels/followers?broadcaster_id=%s&user_id=%s", helixBaseURL, broadcasterID, userID)
	body, _, err := c.doAuthenticatedRequest("GET", url, nil, c.userToken.Get())
	if err != nil {
		return nil, err
	}

	var followResp followerResponse
	if err := json.Unmarshal(body, &followResp); err != nil {
		return nil, err
	}

	if len(followResp.Data) == 0 {
		return nil, nil
	}

	return &followResp.Data[0], nil
}

// GetFollowerNumber fetches the number of followers the streamer has
func (c *APIClient) GetFollowerNumber(broadcasterID string) (int, error) {
	broadcasterID = url.QueryEscape(broadcasterID)

	url := fmt.Sprintf("%s/channels/followers?broadcaster_id=%s", helixBaseURL, broadcasterID)
	body, _, err := c.doAuthenticatedRequest("GET", url, nil, c.userToken.Get())
	if err != nil {
		return 0, err
	}

	var totalFollows followerCountResponse
	if err := json.Unmarshal(body, &totalFollows); err != nil {
		return 0, err
	}

	return totalFollows.Total, nil
}

// GetViewerNumber gets the number of viewers currently watching the channel
func (c *APIClient) GetViewerNumber(broadcasterLogin string) (int, error) {
	stream, err := c.GetStream(broadcasterLogin)
	if err != nil {
		return 0, err
	}

	if stream == nil {
		log.Printf("Streamer is offline")
		return 0, nil
	}

	return stream.ViewerCount, nil
}

// GetStream fetches stream information for a channel
func (c *APIClient) GetStream(broadcasterLogin string) (*stream, error) {
	broadcasterLogin = url.QueryEscape(broadcasterLogin)

	url := fmt.Sprintf("%s/streams?user_login=%s", helixBaseURL, broadcasterLogin)
	body, _, err := c.doAuthenticatedRequest("GET", url, nil, c.userToken.Get())
	if err != nil {
		return nil, err
	}

	var streamResp streamResponse
	if err := json.Unmarshal(body, &streamResp); err != nil {
		return nil, err
	}

	if len(streamResp.Data) == 0 {
		return nil, nil
	}

	return &streamResp.Data[0], nil
}

// GetStreamElementsWatchtime fetches a user's watch time from the StreamElements API.
func (c *APIClient) GetStreamElementsWatchtime(channelLogin, userLogin string) (time.Duration, error) {
	channelLogin = url.QueryEscape(channelLogin)
	userLogin = url.QueryEscape(userLogin)

	url := fmt.Sprintf("https://api.streamelements.com/kappa/v2/points/%s/%s", channelLogin, userLogin)
	body, statusCode, err := c.doAuthenticatedRequest("GET", url, nil, "")
	if err != nil {
		return 0, err
	}

	if statusCode == 404 {
		return 0, fmt.Errorf("this user has no watch time recorded by StreamElements in this channel")
	}
	if statusCode != 200 {
		return 0, fmt.Errorf("streamelements API returned status: %d", statusCode)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return 0, err
	}

	points, ok := result["points"].(float64)
	if !ok {
		return 0, fmt.Errorf("could not parse points from StreamElements response")
	}

	minutes := int(points)
	duration := time.Duration(minutes) * time.Minute

	return duration, nil
}

// CreateClip creates a clip and sends its url to chat
func (c *APIClient) CreateClip(broadcasterID string) (string, error) {
	c.clipLock.Lock()
	defer c.clipLock.Unlock()
	if c.tempClip != nil && c.tempClip.Timestamp+15 > time.Now().Unix() {
		return "", fmt.Errorf("clip called too recently: %ds ago", time.Now().Unix()-c.tempClip.Timestamp)
	}

	broadcasterID = url.QueryEscape(broadcasterID)

	url := fmt.Sprintf("https://api.twitch.tv/helix/clips?broadcaster_id=%s", broadcasterID)
	body, _, err := c.doAuthenticatedRequest("POST", url, nil, c.userToken.Get())
	if err != nil {
		return "", err
	}

	var clip_response clipResponse
	if err := json.Unmarshal(body, &clip_response); err != nil {
		return "", err
	}

	if len(clip_response.Data) == 0 {
		return "", fmt.Errorf("clip creation failed, no data returned in response: %s", string(body))
	}

	c.tempClip = new(clipStore)
	c.tempClip.Url = ("https://clips.twitch.tv/" + clip_response.Data[0].ID)
	c.tempClip.Timestamp = time.Now().Unix()

	clip_creation_timer := time.NewTimer(3 * time.Second)

	go c.prepClip(c.tempClip.Url + "/edit")
	<-clip_creation_timer.C

	return c.tempClip.Url, nil
}

// prepClip is designed to access c.tempClip.Url/edit to generate a thumbnail
func (c *APIClient) prepClip(url string) {
	var logMessage string

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for range 3 {
		<-ticker.C
		_, statusCode, err := c.doAuthenticatedRequest("GET", url, nil, "")
		if statusCode == http.StatusOK && err == nil {
			return
		}
		logMessage = fmt.Sprintf("error: %v, status: %d", err, statusCode)
	}
	log.Printf("Timed out clip access @(%s) after 15 seconds: (%s)", url, logMessage)
}

// SendChatMessage sends a message to a Twitch channel using the Helix API.
func (c *APIClient) SendChatMessage(broadcasterID, message string) error {
	url := helixBaseURL + "/chat/messages"

	msgRequest := sendChatMessageRequest{
		BroadcasterID: broadcasterID,
		SenderID:      c.UserID,
		Message:       message,
	}

	body, err := json.Marshal(msgRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal chat message request: %w", err)
	}

	if err := c.rateLimit.takeToken(); err != nil {
		return fmt.Errorf("rate limit reached: %w", err)
	}
	respBody, statusCode, err := c.doAuthenticatedRequest(
		"POST", url, bytes.NewBuffer(body), c.appTokenManager.Get(),
	)
	if err != nil {
		return err
	}

	if statusCode != http.StatusOK {
		return fmt.Errorf("failed to send chat message, status: %d, body: %s",
			statusCode, string(respBody),
		)
	}

	return nil
}

// UrlFetch makes a simple API call and returns the string returned
func (c *APIClient) UrlFetch(url string) (string, error) {
	body, statusCode, err := c.doAuthenticatedRequest("GET", url, nil, "")
	if err != nil {
		return "", err
	}

	if statusCode == 404 {
		return "", fmt.Errorf("requested page could not be found")
	}
	if statusCode != 200 {
		return "", fmt.Errorf("error processing request")
	}

	return string(body), nil
}
