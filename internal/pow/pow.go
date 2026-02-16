package pow

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"github.com/btcsuite/btcutil/base58"
)

// Challenge represents an issued PoW challenge.
type Challenge struct {
	Token      string
	Difficulty int
	Expires    time.Time
}

// Manager issues and verifies PoW challenges.
type Manager struct {
	mu       sync.Mutex
	pending  map[string]*Challenge // token -> challenge
}

// New creates a new PoW manager.
func New() *Manager {
	m := &Manager{
		pending: make(map[string]*Challenge),
	}
	go m.gcLoop()
	return m
}

// Issue creates a new challenge with the given difficulty.
func (m *Manager) Issue(difficulty int) *Challenge {
	token := make([]byte, 32)
	rand.Read(token)

	c := &Challenge{
		Token:      base58.Encode(token),
		Difficulty: difficulty,
		Expires:    time.Now().Add(60 * time.Second),
	}

	m.mu.Lock()
	m.pending[c.Token] = c
	m.mu.Unlock()

	return c
}

// Verify checks a proof against a pending challenge.
// Returns true if valid, consuming the challenge.
func (m *Manager) Verify(token, proof string) bool {
	m.mu.Lock()
	c, ok := m.pending[token]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if time.Now().After(c.Expires) {
		delete(m.pending, token)
		m.mu.Unlock()
		return false
	}
	// Consume the challenge (one-time use)
	delete(m.pending, token)
	m.mu.Unlock()

	return verifyHash(token, proof, c.Difficulty)
}

// verifyHash checks SHA-256(challenge || proof) has >= difficulty leading zero bits.
func verifyHash(challenge, proof string, difficulty int) bool {
	h := sha256.New()
	h.Write([]byte(challenge))
	h.Write([]byte(proof))
	hash := h.Sum(nil)

	// Check leading zero bits explicitly
	for i := 0; i < difficulty; i++ {
		byteIdx := i / 8
		bitIdx := uint(7 - (i % 8))
		if hash[byteIdx]&(1<<bitIdx) != 0 {
			return false
		}
	}
	return true
}

// Solve finds a proof for a challenge (for testing).
func Solve(challenge string, difficulty int) string {
	var nonce uint64
	for {
		proof := fmt.Sprintf("%d", nonce)
		if verifyHash(challenge, proof, difficulty) {
			return proof
		}
		nonce++
	}
}

func (m *Manager) gcLoop() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		for k, c := range m.pending {
			if now.After(c.Expires) {
				delete(m.pending, k)
			}
		}
		m.mu.Unlock()
	}
}

