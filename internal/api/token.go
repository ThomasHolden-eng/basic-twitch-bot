// Package api contains the Twitch Helix API client and supporting
// authentication (app access token, user OAuth, token refresh, rate
// limiting). It does not know about IRC or chat messages.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
)

// TokenManager holds a thread-safe OAuth or app access token.
type TokenManager struct {
	token     *string    // Contains the raw token
	tokenLock sync.Mutex // Manage concurrent access to the token
}

// Get returns the token.
func (c *TokenManager) Get() string {
	c.tokenLock.Lock()
	defer c.tokenLock.Unlock()
	return *c.token
}

// Set sets the token.
func (c *TokenManager) Set(s string) {
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
