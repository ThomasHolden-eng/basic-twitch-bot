package twitch

import (
	"fmt"
	"sync"
	"time"
)

// TokenBucket is a simple rate limiting manager for the twitch API
type TokenBucket struct {
	tokensLeft int
	mutex      sync.Mutex
}

// TakeToken returns an error if the bucket is empty, removes a token otherwise
func (t *TokenBucket) TakeToken() error {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.tokensLeft > 0 {
		t.tokensLeft--
		return nil
	}
	return fmt.Errorf("token bucket empty")
}

/*
 * StartTokenTimer begins the timer to add a token to the token bucket
 * This is the direct implementation for rate limiting the chatbot (100 messages/30s)
 * Refer to https://dev.twitch.tv/docs/chat/#twitch-chat-rate-limits for more info
 */
func (t *TokenBucket) StartTokenTimer() {
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
