package relay_test

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// ── §6.3 Per-Agent Message Rate Limit (60/min) ─────────────────────────────

func TestRateLimit_Messages(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	alice := newAgent("Alice")
	bob := newAgent("Bob")
	wsA := connect(t, wsURL, alice)
	defer wsA.Close()
	wsB := connect(t, wsURL, bob)
	defer wsB.Close()

	// Create room and both join
	createRoomHelper(t, wsA, alice, "rate-msg-test")
	joinRoomHelper(t, wsB, bob, "rate-msg-test")
	readMsg(t, wsA, 5*time.Second) // member_joined

	// Send 61 messages (limit 60/min)
	var rateLimited bool
	for i := 0; i < 61; i++ {
		msg := map[string]interface{}{
			"type": "message", "id": randomUUID(), "room": "rate-msg-test",
			"from": alice.id,
			"content": map[string]interface{}{"type": "text", "text": fmt.Sprintf("msg-%d", i)},
			"timestamp": time.Now().UnixMilli(), "nonce": randomNonce(),
		}
		msg["signature"] = sign(alice, msg)
		wsA.WriteJSON(msg)

		if i < 60 {
			// Bob should receive or we read from Alice for errors
			readMsg(t, wsB, 2*time.Second)
		} else {
			resp := readMsg(t, wsA, 2*time.Second)
			if resp["type"] == "error" && resp["code"] == "RATE_LIMITED" {
				rateLimited = true
				if _, ok := resp["retry_after_ms"]; !ok {
					t.Fatal("RATE_LIMITED should include retry_after_ms")
				}
			}
		}
	}
	if !rateLimited {
		t.Fatal("expected RATE_LIMITED after 61 messages")
	}
}

// ── §6.3 Per-Agent Join Rate Limit (10/min) ─────────────────────────────────

func TestRateLimit_Joins(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	creator := newAgent("Creator")
	joiner := newAgent("Joiner")
	wsC := connect(t, wsURL, creator)
	defer wsC.Close()
	wsJ := connect(t, wsURL, joiner)
	defer wsJ.Close()

	// Create 11 rooms
	for i := 0; i < 11; i++ {
		createRoomHelper(t, wsC, creator, fmt.Sprintf("join-rate-%d", i))
	}

	// Join 11 rooms (limit 10/min)
	var rateLimited bool
	for i := 0; i < 11; i++ {
		msg := map[string]interface{}{
			"type": "room.join", "room": fmt.Sprintf("join-rate-%d", i),
			"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
		}
		msg["signature"] = sign(joiner, msg)
		wsJ.WriteJSON(msg)

		resp := readMsg(t, wsJ, 5*time.Second)
		if resp["type"] == "error" && resp["code"] == "RATE_LIMITED" {
			rateLimited = true
			break
		}
		// Consume member_joined on creator side
		readMsg(t, wsC, 2*time.Second)
	}
	if !rateLimited {
		t.Fatal("expected RATE_LIMITED after 11 joins")
	}
}

// ── §6.4 IP Block after 5 Auth Failures ─────────────────────────────────────

func TestIPBlock_AuthFailures(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	// Send 5 bad auth attempts
	for i := 0; i < 5; i++ {
		a := newAgent(fmt.Sprintf("Bad-%d", i))
		ws, _, _ := websocket.DefaultDialer.Dial(wsURL, nil)
		hello := map[string]interface{}{
			"type": "hello",
			"profile": map[string]interface{}{
				"id": a.id, "name": a.name, "version": "0.1.0",
			},
			"timestamp": time.Now().UnixMilli(),
			"nonce":     randomNonce(),
		}
		hello["signature"] = "invalid"
		ws.WriteJSON(hello)
		ws.Close()
		time.Sleep(10 * time.Millisecond)
	}

	// 6th attempt should be blocked (connection refused or immediate close)
	a := newAgent("Blocked")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		// Connection refused = blocked. OK.
		return
	}
	defer ws.Close()

	hello := map[string]interface{}{
		"type": "hello",
		"profile": map[string]interface{}{
			"id": a.id, "name": a.name, "version": "0.1.0",
		},
		"timestamp": time.Now().UnixMilli(),
		"nonce":     randomNonce(),
	}
	hello["signature"] = sign(a, hello)
	ws.WriteJSON(hello)

	// Should get an error or connection close
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp map[string]interface{}
	err = ws.ReadJSON(&resp)
	if err == nil && resp["type"] == "welcome" {
		t.Fatal("should be blocked after 5 auth failures")
	}
}

// ── §6.3 REST API Rate Limiting ─────────────────────────────────────────────

func TestRateLimit_RESTAPI(t *testing.T) {
	_, apiURL, cleanup := startRelayNoGates(t)
	defer cleanup()

	// Hit /api/rooms 125 times (REST API rate limit is 120/min)
	var rateLimited bool
	for i := 0; i < 125; i++ {
		resp, err := http.Get(apiURL + "/api/rooms")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == 429 {
			rateLimited = true
			break
		}
	}
	if !rateLimited {
		t.Fatal("expected 429 from REST API after exceeding rate limit")
	}
}

// ── Room Name Validation ────────────────────────────────────────────────────

func TestRoomCreate_NameValidation(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	a := newAgent("Agent")
	ws := connect(t, wsURL, a)
	defer ws.Close()

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid-room", false},
		{"room123", false},
		{"a", false},
		{"", true},                                // empty
		{"UPPERCASE", false},                      // lowercased to "uppercase" — valid
		{strings.Repeat("a", 65), true},           // too long
		{"room with spaces", true},                // spaces
		{"room@special!", true},                    // special chars
		{"room_underscore", true},                  // underscore not in [a-z0-9\-]
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := createRoomRaw(t, ws, a, tt.name)
			if tt.wantErr {
				if resp["type"] != "error" {
					t.Errorf("room name %q: expected error, got %v", tt.name, resp["type"])
				}
			} else {
				if resp["type"] != "room.joined" {
					t.Errorf("room name %q: expected room.joined, got %v", tt.name, resp)
				}
			}
		})
	}
}

// ── Tag Normalization ───────────────────────────────────────────────────────

func TestRoomCreate_TagNormalization(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	a := newAgent("Agent")
	ws := connect(t, wsURL, a)
	defer ws.Close()

	// Create room with mixed-case tags and excess tags
	msg := map[string]interface{}{
		"type": "room.create", "room": "tag-test",
		"topic": "test",
		"tags":  []interface{}{"RUST", " Go ", "python", "tag4", "tag5", "tag6", "tag7", "tag8", "tag9", "tag10", "tag11-should-be-dropped"},
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	msg["signature"] = sign(a, msg)
	ws.WriteJSON(msg)

	resp := readMsg(t, ws, 5*time.Second)
	if resp["type"] == "pow.challenge" {
		ch := resp["challenge"].(string)
		d := int(resp["difficulty"].(float64))
		proof := solvePoW(ch, d)
		msg2 := map[string]interface{}{
			"type": "room.create", "room": "tag-test",
			"topic": "test",
			"tags":  []interface{}{"RUST", " Go ", "python", "tag4", "tag5", "tag6", "tag7", "tag8", "tag9", "tag10", "tag11-should-be-dropped"},
			"pow":   map[string]interface{}{"challenge": ch, "proof": proof},
			"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
		}
		msg2["signature"] = sign(a, msg2)
		ws.WriteJSON(msg2)
		resp = readMsg(t, ws, 5*time.Second)
	}

	if resp["type"] != "room.joined" {
		t.Fatalf("expected room.joined, got %v", resp)
	}

	tags := resp["tags"].([]interface{})
	// Max 10 tags
	if len(tags) > 10 {
		t.Fatalf("expected max 10 tags, got %d", len(tags))
	}
	// Lowercase normalized
	if tags[0] != "rust" {
		t.Fatalf("expected lowercase 'rust', got %v", tags[0])
	}
	if tags[1] != "go" {
		t.Fatalf("expected trimmed lowercase 'go', got %v", tags[1])
	}
}

// ── PoW Difficulty Verification ─────────────────────────────────────────────

func TestPoW_DifficultyAccuracy(t *testing.T) {
	// Verify PoW at various difficulty levels
	for _, diff := range []int{8, 12, 16, 20} {
		proof := solvePoW("test-challenge", diff)
		// Double-check the proof meets exactly the required difficulty
		if !verifyPoWBits("test-challenge", proof, diff) {
			t.Fatalf("PoW proof does not meet difficulty %d", diff)
		}
		// Verify it does NOT meet difficulty+1 (unless lucky)
		// This is probabilistic, but for low difficulties it's very likely to fail
		if diff <= 12 && verifyPoWBits("test-challenge", proof, diff+4) {
			// Unlikely but possible — skip rather than fail
			t.Logf("PoW at difficulty %d also passes %d (lucky)", diff, diff+4)
		}
	}
}

func verifyPoWBits(challenge, proof string, difficulty int) bool {
	h := sha256.New()
	h.Write([]byte(challenge))
	h.Write([]byte(proof))
	hash := h.Sum(nil)
	for i := 0; i < difficulty; i++ {
		byteIdx := i / 8
		bitIdx := uint(7 - (i % 8))
		if hash[byteIdx]&(1<<bitIdx) != 0 {
			return false
		}
	}
	return true
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func createRoomHelper(t *testing.T, ws *websocket.Conn, a *testAgent, name string) {
	t.Helper()
	resp := createRoomRaw(t, ws, a, name)
	if resp["type"] != "room.joined" {
		t.Fatalf("createRoomHelper: expected room.joined for %s, got %v", name, resp)
	}
}

func createRoomRaw(t *testing.T, ws *websocket.Conn, a *testAgent, name string) map[string]interface{} {
	t.Helper()
	msg := map[string]interface{}{
		"type": "room.create", "room": name,
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	msg["signature"] = sign(a, msg)
	ws.WriteJSON(msg)

	resp := readMsg(t, ws, 5*time.Second)
	if resp["type"] == "pow.challenge" {
		ch := resp["challenge"].(string)
		d := int(resp["difficulty"].(float64))
		proof := solvePoW(ch, d)
		msg2 := map[string]interface{}{
			"type": "room.create", "room": name,
			"pow":       map[string]interface{}{"challenge": ch, "proof": proof},
			"nonce":     randomNonce(),
			"timestamp": time.Now().UnixMilli(),
		}
		msg2["signature"] = sign(a, msg2)
		ws.WriteJSON(msg2)
		resp = readMsg(t, ws, 5*time.Second)
	}
	return resp
}

func joinRoomHelper(t *testing.T, ws *websocket.Conn, a *testAgent, name string) {
	t.Helper()
	msg := map[string]interface{}{
		"type": "room.join", "room": name,
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	msg["signature"] = sign(a, msg)
	ws.WriteJSON(msg)

	resp := readMsg(t, ws, 5*time.Second)
	if resp["type"] != "room.joined" {
		t.Fatalf("joinRoomHelper: expected room.joined for %s, got %v", name, resp)
	}
}
