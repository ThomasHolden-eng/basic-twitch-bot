package state

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

const refreshFile = "refresh.json"

// RefreshFile holds a thread-safe map of refresh tokens.
type RefreshFile struct {
	mu sync.Mutex // protects the map from concurrent access
}

// NewRefreshFile returns a new RefreshFile.
func NewRefreshFile() *RefreshFile {
	return &RefreshFile{}
}

// load reads the token map, treating a missing file as an empty map.
func (r *RefreshFile) load() (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(refreshFile)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	tokens := map[string]string{}
	if err := json.Unmarshal(data, &tokens); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", refreshFile, err)
	}
	return tokens, nil
}

// save writes the map atomically via a temp file + rename.
func (r *RefreshFile) save(tokens map[string]string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := json.MarshalIndent(tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp := refreshFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, refreshFile)
}

// LoadRefreshToken returns the refresh token for the given client ID.
func (r *RefreshFile) LoadRefreshToken(clientID string) (*string, error) {
	tokens, err := r.load()
	if err != nil {
		return nil, err
	}
	token, ok := tokens[clientID]
	if !ok {
		return nil, fmt.Errorf("no token listed under %v", clientID)
	}
	return &token, nil
}

// SetRefreshToken updates the refresh token for the given client ID.
func (r *RefreshFile) SetRefreshToken(clientID, token string) error {
	tokens, err := r.load()
	if err != nil {
		return err
	}
	tokens[clientID] = token
	return r.save(tokens)
}

// DeleteRefreshToken deletes the refresh token for the given client ID.
func (r *RefreshFile) DeleteRefreshToken(clientID string) error {
	tokens, err := r.load()
	if err != nil {
		return err
	}
	delete(tokens, clientID)
	return r.save(tokens)
}
