package ratelimit

import (
	"sync"
	"time"
)

// Window tracks event counts in a sliding window.
type Window struct {
	mu      sync.Mutex
	counts  map[string]*bucket
	limit   int
	window  time.Duration
}

type bucket struct {
	count   int
	resetAt time.Time
}

// New creates a rate limiter with the given limit per window.
func New(limit int, window time.Duration) *Window {
	w := &Window{
		counts: make(map[string]*bucket),
		limit:  limit,
		window: window,
	}
	go w.gcLoop()
	return w
}

// Allow checks if the key is within its limit. Returns (allowed, retryAfterMs).
func (w *Window) Allow(key string) (bool, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()

	now := time.Now()
	b, ok := w.counts[key]
	if !ok || now.After(b.resetAt) {
		w.counts[key] = &bucket{count: 1, resetAt: now.Add(w.window)}
		return true, 0
	}

	if b.count >= w.limit {
		retryMs := b.resetAt.Sub(now).Milliseconds()
		if retryMs < 0 {
			retryMs = 0
		}
		return false, retryMs
	}

	b.count++
	return true, 0
}

func (w *Window) gcLoop() {
	ticker := time.NewTicker(w.window)
	defer ticker.Stop()
	for range ticker.C {
		w.mu.Lock()
		now := time.Now()
		for k, b := range w.counts {
			if now.After(b.resetAt) {
				delete(w.counts, k)
			}
		}
		w.mu.Unlock()
	}
}

// IPBlocker tracks failed auth attempts and blocks IPs.
type IPBlocker struct {
	mu       sync.Mutex
	failures map[string]*ipRecord
}

type ipRecord struct {
	count     int
	windowEnd time.Time
	blockedUntil time.Time
}

// NewIPBlocker creates an IP blocker (5 failures/min -> 5 min block).
func NewIPBlocker() *IPBlocker {
	b := &IPBlocker{failures: make(map[string]*ipRecord)}
	go b.gcLoop()
	return b
}

// RecordFailure records a failed auth from an IP. Returns true if now blocked.
func (b *IPBlocker) RecordFailure(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	r, ok := b.failures[ip]
	if !ok || now.After(r.windowEnd) {
		b.failures[ip] = &ipRecord{count: 1, windowEnd: now.Add(time.Minute)}
		return false
	}

	r.count++
	if r.count >= 5 {
		r.blockedUntil = now.Add(5 * time.Minute)
		return true
	}
	return false
}

// IsBlocked checks if an IP is currently blocked.
func (b *IPBlocker) IsBlocked(ip string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	r, ok := b.failures[ip]
	if !ok {
		return false
	}
	return time.Now().Before(r.blockedUntil)
}

func (b *IPBlocker) gcLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		b.mu.Lock()
		now := time.Now()
		for k, r := range b.failures {
			if now.After(r.windowEnd) && now.After(r.blockedUntil) {
				delete(b.failures, k)
			}
		}
		b.mu.Unlock()
	}
}
