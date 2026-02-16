package agent

import (
	"sync"
	"time"
)

// NonceTracker detects replay attacks by tracking (agent_id, nonce) pairs.
type NonceTracker struct {
	mu    sync.Mutex
	seen  map[string]time.Time // "agentID:nonce" -> expiry
}

// NewNonceTracker creates a nonce tracker.
func NewNonceTracker() *NonceTracker {
	t := &NonceTracker{seen: make(map[string]time.Time)}
	go t.gcLoop()
	return t
}

// Check returns true if the nonce is new (not replayed).
// Rejects if timestamp is older than 60 seconds.
func (t *NonceTracker) Check(agentID, nonce string, timestamp int64) bool {
	now := time.Now()
	msgTime := time.UnixMilli(timestamp)

	if now.Sub(msgTime) > 60*time.Second || msgTime.After(now.Add(5*time.Second)) {
		return false
	}

	key := agentID + ":" + nonce

	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.seen[key]; exists {
		return false
	}

	t.seen[key] = now.Add(60 * time.Second)
	return true
}

func (t *NonceTracker) gcLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		t.mu.Lock()
		now := time.Now()
		for k, exp := range t.seen {
			if now.After(exp) {
				delete(t.seen, k)
			}
		}
		t.mu.Unlock()
	}
}
