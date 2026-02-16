package relay_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/betta-lab/agentnet-relay/internal/relay"
)

// startRelayNoGates starts a relay with age gates disabled (for full lifecycle testing).
func startRelayNoGates(t *testing.T) (string, string, func()) {
	t.Helper()
	srv, err := relay.New(":0", t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("create relay: %v", err)
	}
	srv.CreateAgeGate = 0
	srv.SetCreateRateLimit(1000, time.Minute) // relaxed for tests

	ts := httptest.NewServer(srv)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws"
	apiURL := ts.URL
	return wsURL, apiURL, ts.Close
}

// ── Full Room Lifecycle ─────────────────────────────────────────────────────

func TestRoomLifecycle_CreateJoinMessageLeave(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	alice := newAgent("Alice")
	bob := newAgent("Bob")

	wsA := connect(t, wsURL, alice)
	defer wsA.Close()
	wsB := connect(t, wsURL, bob)
	defer wsB.Close()

	// Alice creates a room
	createMsg := map[string]interface{}{
		"type":      "room.create",
		"room":      "test-room",
		"topic":     "Testing",
		"tags":      []interface{}{"test", "integration"},
		"nonce":     randomNonce(),
		"timestamp": time.Now().UnixMilli(),
	}
	createMsg["signature"] = sign(alice, createMsg)
	wsA.WriteJSON(createMsg)

	// Should get pow.challenge
	respA := readMsg(t, wsA, 5*time.Second)
	if respA["type"] != "pow.challenge" {
		t.Fatalf("expected pow.challenge, got %v", respA)
	}

	// Solve PoW and resend
	challenge := respA["challenge"].(string)
	difficulty := int(respA["difficulty"].(float64))
	proof := solvePoW(challenge, difficulty)

	createMsg2 := map[string]interface{}{
		"type":  "room.create",
		"room":  "test-room",
		"topic": "Testing",
		"tags":  []interface{}{"test", "integration"},
		"pow": map[string]interface{}{
			"challenge": challenge,
			"proof":     proof,
		},
		"nonce":     randomNonce(),
		"timestamp": time.Now().UnixMilli(),
	}
	createMsg2["signature"] = sign(alice, createMsg2)
	wsA.WriteJSON(createMsg2)

	// Should get room.joined
	respA = readMsg(t, wsA, 5*time.Second)
	if respA["type"] != "room.joined" {
		t.Fatalf("expected room.joined after create, got %v", respA)
	}
	if respA["room"] != "test-room" {
		t.Fatalf("room name mismatch: %v", respA["room"])
	}
	if respA["topic"] != "Testing" {
		t.Fatalf("topic mismatch: %v", respA["topic"])
	}
	tags := respA["tags"].([]interface{})
	if len(tags) != 2 || tags[0] != "test" || tags[1] != "integration" {
		t.Fatalf("tags mismatch: %v", tags)
	}
	members := respA["members"].([]interface{})
	if len(members) != 1 {
		t.Fatalf("expected 1 member (creator), got %d", len(members))
	}

	// Bob joins
	joinMsg := map[string]interface{}{
		"type":      "room.join",
		"room":      "test-room",
		"nonce":     randomNonce(),
		"timestamp": time.Now().UnixMilli(),
	}
	joinMsg["signature"] = sign(bob, joinMsg)
	wsB.WriteJSON(joinMsg)

	// Bob gets room.joined
	respB := readMsg(t, wsB, 5*time.Second)
	if respB["type"] != "room.joined" {
		t.Fatalf("Bob expected room.joined, got %v", respB)
	}
	bobMembers := respB["members"].([]interface{})
	if len(bobMembers) != 2 {
		t.Fatalf("expected 2 members after Bob joins, got %d", len(bobMembers))
	}

	// Alice receives room.member_joined
	respA = readMsg(t, wsA, 5*time.Second)
	if respA["type"] != "room.member_joined" {
		t.Fatalf("Alice expected room.member_joined, got %v", respA)
	}
	joinedAgent := respA["agent"].(map[string]interface{})
	if joinedAgent["name"] != "Bob" {
		t.Fatalf("expected Bob, got %v", joinedAgent["name"])
	}

	// Alice sends a message
	msgSend := map[string]interface{}{
		"type": "message",
		"id":   randomUUID(),
		"room": "test-room",
		"from": alice.id,
		"content": map[string]interface{}{
			"type": "text",
			"text": "Hello from Alice!",
		},
		"timestamp": time.Now().UnixMilli(),
		"nonce":     randomNonce(),
	}
	msgSend["signature"] = sign(alice, msgSend)
	wsA.WriteJSON(msgSend)

	// Bob receives the message
	respB = readMsg(t, wsB, 5*time.Second)
	if respB["type"] != "message" {
		t.Fatalf("Bob expected message, got %v", respB)
	}
	content := respB["content"].(map[string]interface{})
	if content["text"] != "Hello from Alice!" {
		t.Fatalf("message text mismatch: %v", content["text"])
	}
	if respB["from"] != alice.id {
		t.Fatalf("from mismatch: %v", respB["from"])
	}

	// Bob leaves
	leaveMsg := map[string]interface{}{
		"type":      "room.leave",
		"room":      "test-room",
		"nonce":     randomNonce(),
		"timestamp": time.Now().UnixMilli(),
	}
	leaveMsg["signature"] = sign(bob, leaveMsg)
	wsB.WriteJSON(leaveMsg)

	// Alice receives room.member_left
	respA = readMsg(t, wsA, 5*time.Second)
	if respA["type"] != "room.member_left" {
		t.Fatalf("Alice expected room.member_left, got %v", respA)
	}
	leftAgent := respA["agent"].(map[string]interface{})
	if leftAgent["name"] != "Bob" {
		t.Fatalf("expected Bob left, got %v", leftAgent["name"])
	}
}

// ── Room Create Duplicate ───────────────────────────────────────────────────

func TestRoomCreate_Duplicate(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	a := newAgent("Agent")
	ws := connect(t, wsURL, a)
	defer ws.Close()

	// Create room
	create := func(name string) map[string]interface{} {
		msg := map[string]interface{}{
			"type": "room.create", "room": name, "topic": "T",
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
				"type": "room.create", "room": name, "topic": "T",
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

	resp1 := create("dup-room")
	if resp1["type"] != "room.joined" {
		t.Fatalf("first create should succeed, got %v", resp1)
	}

	resp2 := create("dup-room")
	if resp2["type"] != "error" || resp2["code"] != "ROOM_EXISTS" {
		t.Fatalf("duplicate create should return ROOM_EXISTS, got %v", resp2)
	}
}

// ── Room Join Non-Existent ──────────────────────────────────────────────────

func TestRoomJoin_NonExistent(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	a := newAgent("Agent")
	ws := connect(t, wsURL, a)
	defer ws.Close()

	joinMsg := map[string]interface{}{
		"type": "room.join", "room": "ghost-room",
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	joinMsg["signature"] = sign(a, joinMsg)
	ws.WriteJSON(joinMsg)

	resp := readMsg(t, ws, 5*time.Second)
	if resp["type"] != "error" || resp["code"] != "ROOM_NOT_FOUND" {
		t.Fatalf("expected ROOM_NOT_FOUND, got %v", resp)
	}
}

// ── Rooms List with Tags ────────────────────────────────────────────────────

func TestRoomsList_TagFilter(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	a := newAgent("Agent")
	ws := connect(t, wsURL, a)
	defer ws.Close()

	createRoom := func(name string, tags []interface{}) {
		msg := map[string]interface{}{
			"type": "room.create", "room": name, "topic": "",
			"tags": tags, "nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
		}
		msg["signature"] = sign(a, msg)
		ws.WriteJSON(msg)
		r := readMsg(t, ws, 5*time.Second)
		if r["type"] == "pow.challenge" {
			ch := r["challenge"].(string)
			d := int(r["difficulty"].(float64))
			proof := solvePoW(ch, d)
			msg2 := map[string]interface{}{
				"type": "room.create", "room": name, "topic": "",
				"tags": tags, "pow": map[string]interface{}{"challenge": ch, "proof": proof},
				"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
			}
			msg2["signature"] = sign(a, msg2)
			ws.WriteJSON(msg2)
			readMsg(t, ws, 5*time.Second) // room.joined
		}
	}

	createRoom("rust-agents", []interface{}{"rust", "agent"})
	createRoom("go-agents", []interface{}{"go", "agent"})
	createRoom("random-chat", []interface{}{"chat"})

	// Filter by "rust" tag
	ws.WriteJSON(map[string]interface{}{
		"type": "rooms.list", "tags": []interface{}{"rust"}, "limit": 50,
	})
	resp := readMsg(t, ws, 5*time.Second)
	rooms := resp["rooms"].([]interface{})
	if len(rooms) != 1 {
		t.Fatalf("expected 1 room with 'rust' tag, got %d", len(rooms))
	}
	room0 := rooms[0].(map[string]interface{})
	if room0["name"] != "rust-agents" {
		t.Fatalf("expected rust-agents, got %v", room0["name"])
	}

	// Filter by "agent" tag (should match 2)
	ws.WriteJSON(map[string]interface{}{
		"type": "rooms.list", "tags": []interface{}{"agent"}, "limit": 50,
	})
	resp = readMsg(t, ws, 5*time.Second)
	rooms = resp["rooms"].([]interface{})
	if len(rooms) != 2 {
		t.Fatalf("expected 2 rooms with 'agent' tag, got %d", len(rooms))
	}
}

// ── Message Delivery: Sender Does NOT Receive Own Message ───────────────────

func TestMessage_SenderDoesNotReceiveOwn(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	alice := newAgent("Alice")
	bob := newAgent("Bob")

	wsA := connect(t, wsURL, alice)
	defer wsA.Close()
	wsB := connect(t, wsURL, bob)
	defer wsB.Close()

	// Create and join
	createAndJoin := func(ws *websocket.Conn, a *testAgent, name string) {
		msg := map[string]interface{}{
			"type": "room.create", "room": name,
			"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
		}
		msg["signature"] = sign(a, msg)
		ws.WriteJSON(msg)
		r := readMsg(t, ws, 5*time.Second)
		if r["type"] == "pow.challenge" {
			ch := r["challenge"].(string)
			d := int(r["difficulty"].(float64))
			proof := solvePoW(ch, d)
			msg2 := map[string]interface{}{
				"type": "room.create", "room": name,
				"pow":       map[string]interface{}{"challenge": ch, "proof": proof},
				"nonce":     randomNonce(),
				"timestamp": time.Now().UnixMilli(),
			}
			msg2["signature"] = sign(a, msg2)
			ws.WriteJSON(msg2)
			readMsg(t, ws, 5*time.Second) // room.joined
		}
	}
	createAndJoin(wsA, alice, "echo-test")

	joinMsg := map[string]interface{}{
		"type": "room.join", "room": "echo-test",
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	joinMsg["signature"] = sign(bob, joinMsg)
	wsB.WriteJSON(joinMsg)
	readMsg(t, wsB, 5*time.Second) // room.joined
	readMsg(t, wsA, 5*time.Second) // member_joined for Bob

	// Alice sends
	msgSend := map[string]interface{}{
		"type": "message", "id": randomUUID(), "room": "echo-test",
		"from": alice.id,
		"content": map[string]interface{}{"type": "text", "text": "ping"},
		"timestamp": time.Now().UnixMilli(), "nonce": randomNonce(),
	}
	msgSend["signature"] = sign(alice, msgSend)
	wsA.WriteJSON(msgSend)

	// Bob should get it
	respB := readMsg(t, wsB, 5*time.Second)
	if respB["type"] != "message" {
		t.Fatalf("Bob expected message, got %v", respB)
	}

	// Alice should NOT get her own message back
	wsA.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var echoBack map[string]interface{}
	err := wsA.ReadJSON(&echoBack)
	if err == nil && echoBack["type"] == "message" {
		t.Fatal("sender should NOT receive their own message")
	}
}

// ── Disconnect broadcasts member_left ───────────────────────────────────────

func TestDisconnect_BroadcastsMemberLeft(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	alice := newAgent("Alice")
	bob := newAgent("Bob")

	wsA := connect(t, wsURL, alice)
	defer wsA.Close()
	wsB := connect(t, wsURL, bob)

	// Alice creates room
	msg := map[string]interface{}{
		"type": "room.create", "room": "dc-test",
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	msg["signature"] = sign(alice, msg)
	wsA.WriteJSON(msg)
	r := readMsg(t, wsA, 5*time.Second)
	if r["type"] == "pow.challenge" {
		ch := r["challenge"].(string)
		d := int(r["difficulty"].(float64))
		proof := solvePoW(ch, d)
		msg2 := map[string]interface{}{
			"type": "room.create", "room": "dc-test",
			"pow":       map[string]interface{}{"challenge": ch, "proof": proof},
			"nonce":     randomNonce(),
			"timestamp": time.Now().UnixMilli(),
		}
		msg2["signature"] = sign(alice, msg2)
		wsA.WriteJSON(msg2)
		readMsg(t, wsA, 5*time.Second)
	}

	// Bob joins
	joinMsg := map[string]interface{}{
		"type": "room.join", "room": "dc-test",
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	joinMsg["signature"] = sign(bob, joinMsg)
	wsB.WriteJSON(joinMsg)
	readMsg(t, wsB, 5*time.Second) // room.joined
	readMsg(t, wsA, 5*time.Second) // member_joined

	// Bob disconnects
	wsB.Close()

	// Alice should get room.member_left
	resp := readMsg(t, wsA, 5*time.Second)
	if resp["type"] != "room.member_left" {
		t.Fatalf("expected room.member_left on disconnect, got %v", resp)
	}
}

// ── REST API: Message History ───────────────────────────────────────────────

func TestRESTAPI_MessageHistory(t *testing.T) {
	wsURL, apiURL, cleanup := startRelayNoGates(t)
	defer cleanup()

	alice := newAgent("Alice")
	bob := newAgent("Bob")

	wsA := connect(t, wsURL, alice)
	defer wsA.Close()
	wsB := connect(t, wsURL, bob)
	defer wsB.Close()

	// Create room and join
	msg := map[string]interface{}{
		"type": "room.create", "room": "history-test",
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	msg["signature"] = sign(alice, msg)
	wsA.WriteJSON(msg)
	r := readMsg(t, wsA, 5*time.Second)
	if r["type"] == "pow.challenge" {
		ch := r["challenge"].(string)
		d := int(r["difficulty"].(float64))
		proof := solvePoW(ch, d)
		msg2 := map[string]interface{}{
			"type": "room.create", "room": "history-test",
			"pow":       map[string]interface{}{"challenge": ch, "proof": proof},
			"nonce":     randomNonce(),
			"timestamp": time.Now().UnixMilli(),
		}
		msg2["signature"] = sign(alice, msg2)
		wsA.WriteJSON(msg2)
		readMsg(t, wsA, 5*time.Second)
	}

	join := map[string]interface{}{
		"type": "room.join", "room": "history-test",
		"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
	}
	join["signature"] = sign(bob, join)
	wsB.WriteJSON(join)
	readMsg(t, wsB, 5*time.Second) // joined
	readMsg(t, wsA, 5*time.Second) // member_joined

	// Send 3 messages
	for i := 0; i < 3; i++ {
		m := map[string]interface{}{
			"type": "message", "id": randomUUID(), "room": "history-test",
			"from": alice.id,
			"content": map[string]interface{}{"type": "text", "text": fmt.Sprintf("msg-%d", i)},
			"timestamp": time.Now().UnixMilli(), "nonce": randomNonce(),
		}
		m["signature"] = sign(alice, m)
		wsA.WriteJSON(m)
		readMsg(t, wsB, 5*time.Second) // Bob receives
		time.Sleep(10 * time.Millisecond)
	}

	// Query REST API
	resp, err := http.Get(apiURL + "/api/rooms/history-test/messages?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Room     string `json:"room"`
		Messages []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &result)

	if result.Room != "history-test" {
		t.Fatalf("expected room history-test, got %v", result.Room)
	}
	if len(result.Messages) != 3 {
		t.Fatalf("expected 3 messages in history, got %d", len(result.Messages))
	}
}

// ── REST API: Rooms List ────────────────────────────────────────────────────

func TestRESTAPI_RoomsList(t *testing.T) {
	_, apiURL, cleanup := startRelayNoGates(t)
	defer cleanup()

	resp, err := http.Get(apiURL + "/api/rooms")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result struct {
		Rooms []string `json:"rooms"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	// Empty is fine for a fresh relay
}

// ── Max Rooms Per Agent ─────────────────────────────────────────────────────

func TestMaxRoomsPerAgent(t *testing.T) {
	wsURL, _, cleanup := startRelayNoGates(t)
	defer cleanup()

	a := newAgent("Greedy")
	ws := connect(t, wsURL, a)
	defer ws.Close()

	createRoom := func(name string) string {
		msg := map[string]interface{}{
			"type": "room.create", "room": name,
			"nonce": randomNonce(), "timestamp": time.Now().UnixMilli(),
		}
		msg["signature"] = sign(a, msg)
		ws.WriteJSON(msg)
		r := readMsg(t, ws, 5*time.Second)
		if r["type"] == "pow.challenge" {
			ch := r["challenge"].(string)
			d := int(r["difficulty"].(float64))
			proof := solvePoW(ch, d)
			msg2 := map[string]interface{}{
				"type": "room.create", "room": name,
				"pow":       map[string]interface{}{"challenge": ch, "proof": proof},
				"nonce":     randomNonce(),
				"timestamp": time.Now().UnixMilli(),
			}
			msg2["signature"] = sign(a, msg2)
			ws.WriteJSON(msg2)
			r = readMsg(t, ws, 5*time.Second)
		}
		return r["type"].(string)
	}

	// Create 50 rooms (the max)
	for i := 0; i < 50; i++ {
		typ := createRoom(fmt.Sprintf("room-%03d", i))
		if typ != "room.joined" {
			t.Fatalf("room %d: expected room.joined, got %s", i, typ)
		}
	}

	// 51st should fail
	typ := createRoom("room-overflow")
	if typ != "error" {
		t.Fatalf("51st room should fail, got %s", typ)
	}
}
