package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"twitchbotv2/internal/disconnect"
	"twitchbotv2/internal/state"
)

const (
	port        = ":8080"                             // The port to listen on
	redirectURI = "http://localhost:8080/callback"    // The redirect URI
	tokenURL    = "https://id.twitch.tv/oauth2/token" // The token URL
)

var refreshFile *state.RefreshFile = state.NewRefreshFile()

// InitUserToken handles the OAuth2 Authorization Code Flow and returns a
// TokenManager containing the user's access token. A background goroutine
// refreshes the token before it expires.
//
// The flow opens the user's browser to the Twitch auth URL, runs a local
// HTTP server on `port` to receive the callback, validates the state
// parameter (CSRF protection), and exchanges the code for tokens. The
// function blocks until the user completes the flow, an error occurs, or
// the 2-minute timeout elapses.
func InitUserToken(clientID, clientSecret, permissions string, disconnect *disconnect.Disconnector) (*TokenManager, error) {
	if refreshToken, err := refreshFile.LoadRefreshToken(clientID); err == nil && refreshToken != nil {
		tMan := &TokenManager{token: new(string)}
		go refreshTokenFlow(
			clientID,
			clientSecret,
			*refreshToken,
			tMan, time.Minute+time.Microsecond,
			disconnect,
		)
		checkRefreshTicker := time.NewTicker(time.Second)
		ticks := 0
		for range checkRefreshTicker.C {
			if tMan.Get() != "" {
				checkRefreshTicker.Stop()
				return tMan, nil
			}
			ticks++
			if ticks > 300 {
				checkRefreshTicker.Stop()
				return nil, fmt.Errorf("timed out waiting for token refresh")
			}
		}
	}

	tokenManagerChan := make(chan *TokenManager, 1)
	errChan := make(chan error, 1)

	// Generate a secure state for CSRF protection.
	stateBytes := make([]byte, 32)
	if _, err := rand.Read(stateBytes); err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}
	state := base64.URLEncoding.EncodeToString(stateBytes)

	mux := http.NewServeMux()
	server := &http.Server{
		Addr:    port,
		Handler: mux,
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if errorMsg := r.URL.Query().Get("error"); errorMsg != "" {
			description := r.URL.Query().Get("error_description")
			http.Error(w, fmt.Sprintf("Error from Twitch: %s - %s", errorMsg, description), http.StatusBadRequest)
			errChan <- fmt.Errorf("twitch auth error: %s", description)
			return
		}

		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			http.Error(w, "Invalid state parameter. CSRF attack suspected.", http.StatusBadRequest)
			errChan <- fmt.Errorf("invalid state parameter")
			return
		}

		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Authorization code not found in request.", http.StatusBadRequest)
			errChan <- fmt.Errorf("code not found in callback request")
			return
		}

		token, err := exchangeCodeForToken(clientID, clientSecret, code, disconnect)
		if err != nil {
			http.Error(w, "Failed to exchange code for token.", http.StatusInternalServerError)
			errChan <- err
			return
		}

		tokenManagerChan <- token
		fmt.Fprint(w, callbackHTML)
	})

	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- err
		}
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	authURL := fmt.Sprintf(
		"https://id.twitch.tv/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&state=%s",
		clientID, url.QueryEscape(redirectURI), permissions, state,
	)

	err := openBrowser(authURL)
	if err != nil {
		return nil, fmt.Errorf("failed to open browser: %w", err)
	}

	// Wait for the token, an error, or a timeout.
	select {
	case tokenPtr := <-tokenManagerChan:
		return tokenPtr, nil
	case err := <-errChan:
		return nil, err
	case <-time.After(2 * time.Minute):
		return nil, fmt.Errorf("authentication timed out")
	}
}

// exchangeCodeForToken exchanges the authorization code for an access token.
func exchangeCodeForToken(clientID, clientSecret, code string, disconnect *disconnect.Disconnector) (*TokenManager, error) {
	tokenResp, err := fetchToken(url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {redirectURI},
	})
	if err != nil {
		return nil, err
	}

	log.Printf("The oauth token will expire in %d seconds", tokenResp.ExpiresIn)
	tokenManager := &TokenManager{token: &tokenResp.AccessToken}
	go refreshTokenFlow(
		clientID,
		clientSecret,
		tokenResp.RefreshToken,
		tokenManager,
		time.Duration(tokenResp.ExpiresIn*int(time.Second)),
		disconnect,
	)

	return tokenManager, nil
}

// refreshTokenFlow handles the OAuth2 Refresh Token Flow.
func refreshTokenFlow(clientID, clientSecret, refresh_token string,
	oauth_tokenManager *TokenManager, expiry_d time.Duration, disconnect *disconnect.Disconnector) {

	log.Printf("Oauth token will be refreshed in %v", expiry_d-time.Minute)
	refresh_timer := time.NewTicker(expiry_d - time.Minute)
	var tokenResp *tokenResponse
	var err error

	refreshFile.SetRefreshToken(clientID, refresh_token)

	for {
		select {
		case <-disconnect.Done():
			return
		case <-refresh_timer.C:
			// proceed with execution
		}

		maxTime := time.Now().Add(time.Second * 300)
		baseDelay := 500 * time.Millisecond

		for time.Now().Before(maxTime) {
			tokenResp, err = fetchToken(url.Values{
				"client_id":     {clientID},
				"client_secret": {clientSecret},
				"grant_type":    {"refresh_token"},
				"refresh_token": {refresh_token},
			})
			if err == nil {
				break
			}
			log.Printf("Failed to refresh oauth token, retrying in %v", baseDelay)
			time.Sleep(baseDelay)
			if baseDelay < 30*time.Second {
				baseDelay *= 2
			}
		}
		if err != nil || tokenResp.ExpiresIn == -1 {
			log.Printf("error retrieving new access token: %v", err)
			refreshFile.DeleteRefreshToken(clientID)
			return
		}

		log.Printf("The new oauth token will expire in %d seconds", tokenResp.ExpiresIn)

		oauth_tokenManager.Set(tokenResp.AccessToken)

		refresh_token = tokenResp.RefreshToken
		refreshFile.SetRefreshToken(clientID, refresh_token)

		refresh_timer.Reset(time.Duration(tokenResp.ExpiresIn)*time.Second - time.Minute)

		for len(refresh_timer.C) > 0 {
			<-refresh_timer.C
		}
	}
}

// openBrowser opens the given URL in the user's default browser.
func openBrowser(rawUrl string) error {
	u, err := url.ParseRequestURI(rawUrl)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsafe URL scheme: %q", u.Scheme)
	}

	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		rawUrl = strings.ReplaceAll(rawUrl, "&", "^&")
		cmd = "cmd"
		args = []string{"/c", "start", ""}
	case "darwin":
		cmd = "open"
	default:
		cmd = "xdg-open"
	}
	args = append(args, rawUrl)
	return exec.Command(cmd, args...).Start()
}

// callbackHTML is the HTML for the OAuth callback page.
const callbackHTML = `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Twitch Bot Authentication</title>
    <style>
        @import url('https://fonts.googleapis.com/css2?family=Roboto:wght@400;700&display=swap');
        :root {
            --background-color: #18181b; --container-bg: #2a2a2e;
            --text-color: #efeff1; --success-color: #1db954;
            --border-color: #444;
        }
        html, body {
            height: 100%; margin: 0; font-family: 'Roboto', sans-serif;
            background-color: var(--background-color); color: var(--text-color);
            display: flex; justify-content: center; align-items: center; text-align: center;
        }
        .container {
            background-color: var(--container-bg); padding: 40px; border-radius: 12px;
            border: 1px solid var(--border-color); box-shadow: 0 8px 24px rgba(0, 0, 0, 0.5);
            width: 90%; max-width: 400px;
        }
        h1 { margin: 0 0 10px 0; font-size: 1.5em; color: var(--success-color); }
        p { margin: 0; color: #a9a9a9; }
    </style>
</head>
<body>
    <div class="container">
        <h1>✅ Authentication Successful!</h1>
        <p>You can now close this window. The bot has received your token securely.</p>
    </div>
</body>
</html>
`
