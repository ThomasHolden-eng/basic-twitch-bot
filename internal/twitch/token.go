package twitch

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// tokenManager holds the token
type tokenManager struct {
	token     *string    // Contains the raw token
	tokenLock sync.Mutex // Manage concurrent access to the token
}

// get returns the token
func (c *tokenManager) get() string {
	c.tokenLock.Lock()
	defer c.tokenLock.Unlock()
	return *c.token
}

// set sets the token
func (c *tokenManager) set(s string) {
	c.tokenLock.Lock()
	defer c.tokenLock.Unlock()
	*c.token = s
}

// fetchToken is a unified helper for all Twitch token requests.
func fetchToken(data url.Values) (*tokenResponse, error) {
	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("twitch API returned non-200 status: %s - %s", resp.Status, string(body))
	}

	var tokenResp tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}
	return &tokenResp, nil
}

// tokenResponse holds the token response from Twitch
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}
