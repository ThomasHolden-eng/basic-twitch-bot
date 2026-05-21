// Package twitch contains the Twitch API client
//
// Rate limiting logic and data structures for the connection
// are managed internally.
package twitch

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
	helixBaseURL = "https://api.twitch.tv/helix"
)

// APIClient holds the necessary information to communicate with the Twitch API
type APIClient struct {
	ClientID          string
	oauthTokenManager *tokenManager
	appTokenManager   *tokenManager
	httpClient        *http.Client
	UserID            string
}

// NewAPIClient creates a new client for the Twitch API
func NewAPIClient(clientID, clientSecret string) *APIClient {
	oAuthTokenManager, err := getOAuthTokenManager(clientID, clientSecret)
	if err != nil {
		log.Fatal(err)
	}

	appAccessToken, err := getAppAccessTokenManager(clientID, clientSecret)
	if err != nil {
		log.Fatal(err)
	}

	return &APIClient{
		ClientID:          clientID,
		oauthTokenManager: oAuthTokenManager,
		appTokenManager:   appAccessToken,
		httpClient:        &http.Client{},
	}
}

// TwitchUser represents a user object from the Twitch API
type TwitchUser struct {
	ID    string `json:"id"`
	Login string `json:"login"`
}

// Follower represents the follower relationship data from the Twitch API
type Follower struct {
	FollowedAt time.Time `json:"followed_at"`
}

// FollowerResponse is the top-level structure of the get users follows API response
type FollowerResponse struct {
	Data []Follower `json:"data"`
}

// FollowerCountResponse is the structure for the total follower count from the API
type FollowerCountResponse struct {
	Total int `json:"total"`
}

// ViewerCountResponse is the structure for the total viewer count from the API
type ViewerCountResponse struct {
	Total int `json:"total"`
}

// Stream represents a stream object from the Twitch API
type Stream struct {
	StartedAt   time.Time `json:"started_at"`
	Type        string    `json:"type"`
	ViewerCount int       `json:"viewer_count"`
}

// Clip represents a clips access points
type Clip struct {
	ID       string `json:"id"`
	Edit_Url string `json:"edit_url"`
}

// ClipStore is a container to hold a clip to prevent repeat clips
type ClipStore struct {
	Url       string
	Timestamp int64
}

// StreamResponse is the top-level structure of the get streams API response
type StreamResponse struct {
	Data []Stream `json:"data"`
}

// UserResponse is the top-level structure of the get users API response
type UserResponse struct {
	Data []TwitchUser `json:"data"`
}

// ClipResponse is the top-level structure of the create clip API response
type ClipResponse struct {
	Data []Clip `json:"data"`
}

// TwitchChannelUpdateRequest is the structure of a request to change stream details
type TwitchChannelUpdateRequest struct {
	GameID string `json:"game_id"`
	Title  string `json:"title"`
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

// SetUserID validates the bot's token and stores its own User ID
func (c *APIClient) SetUserID(login string) error {
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
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Client-ID", c.ClientID)

	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.doRequestWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var userResp UserResponse
	if err := json.Unmarshal(body, &userResp); err != nil {
		return nil, err
	}

	if len(userResp.Data) == 0 {
		return nil, fmt.Errorf("user not found: %s", login)
	}

	return &userResp.Data[0], nil
}

// GetFollowSince fetches how long a user has followed a channel
func (c *APIClient) GetFollowSince(broadcasterID, userID string) (*Follower, error) {
	broadcasterID = url.QueryEscape(broadcasterID)
	userID = url.QueryEscape(userID)

	url := fmt.Sprintf("%s/channels/followers?broadcaster_id=%s&user_id=%s", helixBaseURL, broadcasterID, userID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.doRequestWithRetry(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var followResp FollowerResponse
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
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.doRequestWithRetry(req, 2)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}

	var totalFollows FollowerCountResponse
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
func (c *APIClient) GetStream(broadcasterLogin string) (*Stream, error) {
	broadcasterLogin = url.QueryEscape(broadcasterLogin)

	url := fmt.Sprintf("%s/streams?user_login=%s", helixBaseURL, broadcasterLogin)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var streamResp StreamResponse
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

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return 0, fmt.Errorf("This user has no watch time recorded by StreamElements in this channel.")
	}
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("streamelements API returned status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
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

var tempClip *ClipStore = nil
var clipLock sync.Mutex

// CreateClip creates a clip and sends its url to chat
func (c *APIClient) CreateClip(broadcasterID string) (string, error) {
	clipLock.Lock()
	defer clipLock.Unlock()
	if tempClip != nil && tempClip.Timestamp+15 > time.Now().Unix() {
		return "", fmt.Errorf("clip called too recently: %ds ago", time.Now().Unix()-tempClip.Timestamp)
	}

	broadcasterID = url.QueryEscape(broadcasterID)

	url := fmt.Sprintf("https://api.twitch.tv/helix/clips?broadcaster_id=%s", broadcasterID)
	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		return "", err
	}

	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.doRequestWithRetry(req, 2)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var clip_response ClipResponse
	if err := json.Unmarshal(body, &clip_response); err != nil {
		return "", err
	}

	if len(clip_response.Data) == 0 {
		return "", fmt.Errorf("clip creation failed, no data returned in response: %s", string(body))
	}

	tempClip = new(ClipStore)
	tempClip.Url = ("https://clips.twitch.tv/" + clip_response.Data[0].ID)
	tempClip.Timestamp = time.Now().Unix()

	clip_creation_timer := time.NewTimer(1 * time.Second)

	go c.prepClip(tempClip.Url + "/edit")
	<-clip_creation_timer.C

	return tempClip.Url, nil
}

// prepClip is designed to access tempClip.Url/edit to generate a thumbnail
func (c *APIClient) prepClip(url string) {
	timeoutTimer := time.NewTimer(15 * time.Second)
	var logMessage string

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				logMessage = fmt.Sprintf("Failed to form new http request: %v", err)
				continue
			}

			req.Header.Add("Client-ID", c.ClientID)
			req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

			resp, err := c.doRequestWithRetry(req)
			if err != nil {
				logMessage = fmt.Sprintf("Error accessing clip: %v", err)
				continue
			}

			if resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				return
			}

			resp.Body.Close()
		case <-timeoutTimer.C:
			log.Printf("Timed out clip access @(%v) after 15 seconds: %v", url, logMessage)
			return
		}
	}
}

// SetTitle (theoretically) changes the users title
func (c *APIClient) SetTitle(broadcasterID, title string) error {
	broadcasterID = url.QueryEscape(broadcasterID)

	data := TwitchChannelUpdateRequest{Title: title}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/channels?broadcaster_id=%s", helixBaseURL, broadcasterID)
	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.doRequestWithRetry(req, 2)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("twitch API error: %s (%d) - %s",
			resp.Status,
			resp.StatusCode,
			string(body),
		)
	}

	return nil
}

// SetTitle (theoretically) changes the users game
func (c *APIClient) SetGame(broadcasterID, game string) error {
	broadcasterID = url.QueryEscape(broadcasterID)

	data := TwitchChannelUpdateRequest{GameID: game}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/channels?broadcaster_id=%s", helixBaseURL, broadcasterID)
	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+c.oauthTokenManager.get())

	resp, err := c.doRequestWithRetry(req, 2)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	return nil
}

// SendChatMessage sends a message to a Twitch channel using the Helix API.
func (c *APIClient) SendChatMessage(broadcasterID, appAccessToken, message string) error {

	// SendChatMessageRequest is the request body for sending a chat message
	type SendChatMessageRequest struct {
		BroadcasterID string `json:"broadcaster_id"`
		SenderID      string `json:"sender_id"`
		Message       string `json:"message"`
	}

	url := helixBaseURL + "/chat/messages"

	msgRequest := SendChatMessageRequest{
		BroadcasterID: broadcasterID,
		SenderID:      c.UserID,
		Message:       message,
	}

	body, err := json.Marshal(msgRequest)
	if err != nil {
		return fmt.Errorf("failed to marshal chat message request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}

	req.Header.Add("Client-ID", c.ClientID)
	req.Header.Add("Authorization", "Bearer "+appAccessToken)
	req.Header.Add("Content-Type", "application/json")

	resp, err := c.doRequestWithRetry(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to send chat message, status: %s, body: %s", resp.Status, string(respBody))
	}

	return nil
}

// urlFetch makes a simple API call and returns the string returned
func (c *APIClient) urlFetch(urlString string) (string, error) {
	req, err := http.NewRequest("GET", urlString, nil)
	if err != nil {
		return "Error generating api request.", err
	}

	resp, err := c.doRequestWithRetry(req, 2)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return "Requested page could not be found.", err
	}
	if resp.StatusCode != 200 {
		return "Error processing your request.", err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "Response formatted incorrectly.", err
	}

	return string(body[:]), nil
}
