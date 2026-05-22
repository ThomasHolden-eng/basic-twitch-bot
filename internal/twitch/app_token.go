package twitch

import "net/url"

// GetAppAccessToken retrieves an app access token using the client credentials flow.
func getAppAccessTokenManager(clientID, clientSecret string) (*tokenManager, error) {
	tokenResp, err := fetchToken(url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"grant_type":    {"client_credentials"},
	})
	if err != nil {
		return nil, err
	}

	return &tokenManager{token: &tokenResp.AccessToken}, nil
}
