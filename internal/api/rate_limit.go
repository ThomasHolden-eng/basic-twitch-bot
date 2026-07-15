package api

import (
	"errors"
	"sync"
	"time"
)

var tokenBucketSingleton *tokenBucket = nil

// tokenBucket is a simple rate limiting manager for the twitch API
type tokenBucket struct {
	tokensLeft int
	mutex      sync.Mutex
}

func newTokenBucket() *tokenBucket {
	if tokenBucketSingleton == nil {
		tokenBucketSingleton = new(tokenBucket)
	}

	return tokenBucketSingleton
}

// takeToken returns an error if the bucket is empty, removes a token otherwise
func (t *tokenBucket) takeToken() error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.tokensLeft > 0 {
		t.tokensLeft--
		return nil
	}
	return errors.New("token bucket empty")
}

// startTokenTimer begins the timer to add a token to the token bucket.
// This is the direct implementation for rate limiting the chatbot (100
// messages/30s). See https://dev.twitch.tv/docs/chat/#twitch-chat-rate-limits
func (t *tokenBucket) startTokenTimer() {
	t.mutex.Lock()
	t.tokensLeft = 50
	t.mutex.Unlock()
	timer1 := time.NewTicker(time.Millisecond * 600)

	for {
		<-timer1.C
		t.mutex.Lock()
		if t.tokensLeft < 50 {
			t.tokensLeft++
		}
		t.mutex.Unlock()
	}
}
