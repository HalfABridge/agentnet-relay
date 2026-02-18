package relay

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/betta-lab/agentnet-relay/internal/agent"
	"github.com/betta-lab/agentnet-relay/internal/pow"
	"github.com/betta-lab/agentnet-relay/internal/ratelimit"
	"github.com/betta-lab/agentnet-relay/internal/room"
	"github.com/betta-lab/agentnet-relay/internal/store"
	"github.com/betta-lab/agentnet-relay/internal/types"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  65536,
	WriteBufferSize: 65536,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Server is the AgentNet relay server.
type Server struct {
	addr    string
	httpSrv *http.Server
	rooms   *room.Manager
	store   *store.Store
	pow     *pow.Manager
	nonces  *agent.NonceTracker

	// Rate limiters
	msgRate    *ratelimit.Window // per-agent messages
	joinRate   *ratelimit.Window // per-agent joins
	createRate *ratelimit.Window // per-agent creates
	listRate   *ratelimit.Window // per-agent list queries (WebSocket)
	apiRate    *ratelimit.Window // per-IP REST API queries (dashboard)
	ipBlocker  *ratelimit.IPBlocker

	// Per-room message rate
	roomMsgRate *ratelimit.Window

	// Connected agents: agent ID -> connection
	mux   *http.ServeMux
	mu    sync.RWMutex
	conns map[string]*Conn
}

// Conn wraps a WebSocket connection with agent state.
type Conn struct {
	ws          *websocket.Conn
	agent       *types.ConnectedAgent
	mu          sync.Mutex
	ip          string
	connectedAt time.Time
	authed      bool
	pendingPow  *pow.Challenge // hello PoW challenge
}

// New creates a new relay server.
func New(addr, dbPath string) (*Server, error) {
	st, err := store.New(dbPath)
	if err != nil {
		return nil, err
	}

	s := &Server{
		addr:  addr,
		rooms:         room.NewManager(10000, 100),
		store:       st,
		pow:         pow.New(),
		nonces:      agent.NewNonceTracker(),
		msgRate:     ratelimit.New(60, time.Minute),
		joinRate:    ratelimit.New(10, time.Minute),
		createRate:  ratelimit.New(5, time.Minute),
		listRate:    ratelimit.New(30, time.Minute),
		apiRate:     ratelimit.New(600, time.Minute), // REST API (dashboard polling)
		ipBlocker:   ratelimit.NewIPBlocker(),
		roomMsgRate: ratelimit.New(500, time.Minute),
		conns:       make(map[string]*Conn),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ws", s.handleWS)
	mux.HandleFunc("/api/rooms", s.handleAPIRooms)
	mux.HandleFunc("/api/rooms/", s.handleAPIRoomMessages)
	mux.HandleFunc("/health", s.handleHealth)

	s.mux = mux
	s.httpSrv = &http.Server{Addr: addr, Handler: mux}
	return s, nil
}

// ServeHTTP implements http.Handler for testing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

// SetCreateRateLimit overrides the per-agent room create rate limit (for testing).
func (s *Server) SetCreateRateLimit(limit int, window time.Duration) {
	s.createRate = ratelimit.New(limit, window)
}

// Start starts the server.
func (s *Server) Start() error {
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.httpSrv.Shutdown(ctx)
	s.store.Close()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleWS upgrades to WebSocket and handles the agent lifecycle.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)

	if s.ipBlocker.IsBlocked(ip) {
		http.Error(w, "blocked", http.StatusForbidden)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	c := &Conn{
		ws:          ws,
		ip:          ip,
		connectedAt: time.Now(),
	}

	// Set max message size (64KB per protocol spec)
	ws.SetReadLimit(65536)

	// Set read deadline for hello
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Read hello
	_, raw, err := ws.ReadMessage()
	if err != nil {
		ws.Close()
		return
	}

	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		s.sendError(c, "AUTH_FAILED", "invalid message", 0)
		ws.Close()
		return
	}

	if env.Type != "hello" {
		s.sendError(c, "AUTH_FAILED", "expected hello", 0)
		ws.Close()
		return
	}

	var hello HelloMsg
	if err := json.Unmarshal(raw, &hello); err != nil {
		s.sendError(c, "AUTH_FAILED", "invalid hello", 0)
		ws.Close()
		return
	}

	// Validate profile
	if len(hello.Profile.Name) == 0 || len(hello.Profile.Name) > 64 {
		s.sendError(c, "INVALID_MESSAGE", "name must be 1-64 characters", 0)
		ws.Close()
		return
	}

	// Verify hello signature
	if !agent.VerifySignature(json.RawMessage(raw), hello.Profile.ID, hello.Signature) {
		s.ipBlocker.RecordFailure(ip)
		s.sendError(c, "AUTH_FAILED", "invalid signature", 0)
		ws.Close()
		return
	}

	// Verify nonce/timestamp
	if !s.nonces.Check(hello.Profile.ID, hello.Nonce, hello.Timestamp) {
		s.sendError(c, "AUTH_FAILED", "invalid nonce or timestamp", 0)
		ws.Close()
		return
	}

	// Issue PoW challenge for hello
	challenge := s.pow.Issue(16) // baseline difficulty
	c.pendingPow = challenge
	s.sendJSON(c, PowChallenge{
		Type:       "pow.challenge",
		Challenge:  challenge.Token,
		Difficulty: challenge.Difficulty,
		Expires:    challenge.Expires.UnixMilli(),
	})

	// Wait for hello.pow
	ws.SetReadDeadline(time.Now().Add(60 * time.Second))
	_, raw, err = ws.ReadMessage()
	if err != nil {
		ws.Close()
		return
	}

	var powMsg HelloPowMsg
	if err := json.Unmarshal(raw, &powMsg); err != nil || powMsg.Type != "hello.pow" {
		s.sendError(c, "AUTH_FAILED", "expected hello.pow", 0)
		ws.Close()
		return
	}

	if !s.pow.Verify(powMsg.Pow.Challenge, powMsg.Pow.Proof) {
		s.ipBlocker.RecordFailure(ip)
		s.sendError(c, "AUTH_FAILED", "invalid proof of work", 0)
		ws.Close()
		return
	}

	// Auth successful
	c.authed = true
	c.agent = &types.ConnectedAgent{
		Profile:     hello.Profile,
		ConnectedAt: time.Now(),
		Rooms:       make(map[string]bool),
	}

	s.mu.Lock()
	// Disconnect existing connection with same agent ID
	if old, exists := s.conns[hello.Profile.ID]; exists {
		old.ws.Close()
	}
	s.conns[hello.Profile.ID] = c
	s.mu.Unlock()

	s.sendJSON(c, WelcomeMsg{Type: "welcome", Relay: s.addr})

	// Clear deadline, enter message loop
	ws.SetReadDeadline(time.Time{})
	s.messageLoop(c)
}

func (s *Server) messageLoop(c *Conn) {
	defer s.disconnect(c)

	// Ping/pong timeout
	c.ws.SetPongHandler(func(string) error {
		c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		return nil
	})
	c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))

	// Start ping ticker
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.mu.Lock()
			err := c.ws.WriteMessage(websocket.PingMessage, nil)
			c.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			return
		}

		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			s.sendError(c, "INVALID_MESSAGE", "malformed JSON", 0)
			continue
		}

		switch env.Type {
		case "ping":
			s.sendJSON(c, map[string]string{"type": "pong"})
			c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))

		case "room.create":
			s.handleRoomCreate(c, raw)

		case "room.join":
			s.handleRoomJoin(c, raw)

		case "room.leave":
			s.handleRoomLeave(c, raw)

		case "rooms.list":
			s.handleRoomsList(c, raw)

		case "message":
			s.handleMessage(c, raw)

		default:
			s.sendError(c, "UNKNOWN_TYPE", "unrecognized message type: "+env.Type, 0)
		}
	}
}

func (s *Server) handleRoomCreate(c *Conn, raw []byte) {
	var msg RoomCreateMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.sendError(c, "INVALID_MESSAGE", "invalid room.create", 0)
		return
	}

	// Verify signature
	if !agent.VerifySignature(json.RawMessage(raw), c.agent.Profile.ID, msg.Signature) {
		s.sendError(c, "AUTH_FAILED", "invalid signature", 0)
		return
	}

	// Nonce check
	if !s.nonces.Check(c.agent.Profile.ID, msg.Nonce, msg.Timestamp) {
		s.sendError(c, "AUTH_FAILED", "invalid nonce or timestamp", 0)
		return
	}

	// Normalize and validate room name before issuing PoW challenge
	msg.Room = strings.ToLower(msg.Room)
	if !room.ValidName(msg.Room) {
		s.sendError(c, "INVALID_MESSAGE", "invalid room name: must match [a-z0-9-]{1,64}", 0)
		return
	}

	// PoW required — issue challenge if no proof provided
	if msg.Pow == nil {
		challenge := s.pow.Issue(20)
		s.sendJSON(c, PowChallenge{
			Type:       "pow.challenge",
			Challenge:  challenge.Token,
			Difficulty: challenge.Difficulty,
			Expires:    challenge.Expires.UnixMilli(),
		})
		return
	}

	if !s.pow.Verify(msg.Pow.Challenge, msg.Pow.Proof) {
		s.sendError(c, "AUTH_FAILED", "invalid proof of work", 0)
		return
	}

	// Per-agent rate (only counted after PoW passes)
	if ok, retryMs := s.createRate.Allow(c.agent.Profile.ID); !ok {
		s.sendError(c, "RATE_LIMITED", "room create rate exceeded", retryMs)
		return
	}

	// Max rooms per agent
	if len(c.agent.Rooms) >= 50 {
		s.sendError(c, "RATE_LIMITED", "max rooms per agent reached", 0)
		return
	}

	r, errCode := s.rooms.Create(msg.Room, msg.Topic, msg.Tags)
	if errCode != "" {
		s.sendError(c, errCode, "room create failed", 0)
		return
	}

	// Auto-join
	_, members, _ := s.rooms.Join(r.Name, c.agent)
	c.agent.Rooms[r.Name] = true

	s.sendJSON(c, RoomJoinedMsg{
		Type:    "room.joined",
		Room:    r.Name,
		Topic:   r.Topic,
		Tags:    r.Tags,
		Members: members,
	})
}

func (s *Server) handleRoomJoin(c *Conn, raw []byte) {
	if ok, retryMs := s.joinRate.Allow(c.agent.Profile.ID); !ok {
		s.sendError(c, "RATE_LIMITED", "join rate exceeded", retryMs)
		return
	}

	var msg RoomJoinMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.sendError(c, "INVALID_MESSAGE", "invalid room.join", 0)
		return
	}

	if !agent.VerifySignature(json.RawMessage(raw), c.agent.Profile.ID, msg.Signature) {
		s.sendError(c, "AUTH_FAILED", "invalid signature", 0)
		return
	}

	if !s.nonces.Check(c.agent.Profile.ID, msg.Nonce, msg.Timestamp) {
		s.sendError(c, "AUTH_FAILED", "invalid nonce or timestamp", 0)
		return
	}

	if len(c.agent.Rooms) >= 50 {
		s.sendError(c, "RATE_LIMITED", "max rooms per agent reached", 0)
		return
	}

	r, members, errCode := s.rooms.Join(msg.Room, c.agent)
	if errCode != "" {
		s.sendError(c, errCode, "join failed: "+errCode, 0)
		return
	}

	c.agent.Rooms[r.Name] = true

	s.sendJSON(c, RoomJoinedMsg{
		Type:    "room.joined",
		Room:    r.Name,
		Topic:   r.Topic,
		Tags:    r.Tags,
		Members: members,
	})

	// Broadcast to others
	s.broadcast(r.Name, c.agent.Profile.ID, RoomMemberEvent{
		Type:  "room.member_joined",
		Room:  r.Name,
		Agent: types.MemberInfo{ID: c.agent.Profile.ID, Name: c.agent.Profile.Name},
	})
}

func (s *Server) handleRoomLeave(c *Conn, raw []byte) {
	var msg RoomLeaveMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.sendError(c, "INVALID_MESSAGE", "invalid room.leave", 0)
		return
	}

	if !agent.VerifySignature(json.RawMessage(raw), c.agent.Profile.ID, msg.Signature) {
		s.sendError(c, "AUTH_FAILED", "invalid signature", 0)
		return
	}

	delete(c.agent.Rooms, msg.Room)
	s.rooms.Leave(msg.Room, c.agent.Profile.ID)

	s.broadcast(msg.Room, c.agent.Profile.ID, RoomMemberEvent{
		Type:  "room.member_left",
		Room:  msg.Room,
		Agent: types.MemberInfo{ID: c.agent.Profile.ID, Name: c.agent.Profile.Name},
	})
}

func (s *Server) handleRoomsList(c *Conn, raw []byte) {
	if ok, retryMs := s.listRate.Allow(c.agent.Profile.ID); !ok {
		s.sendError(c, "RATE_LIMITED", "list rate exceeded", retryMs)
		return
	}

	var msg RoomsListMsg
	json.Unmarshal(raw, &msg)

	limit := msg.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	matchAll := msg.Match == "all"
	results := s.rooms.List(msg.Tags, matchAll, msg.Sort, limit)

	items := make([]RoomListItem, len(results))
	for i, r := range results {
		items[i] = RoomListItem{
			Name:       r.Name,
			Topic:      r.Topic,
			Tags:       r.Tags,
			Agents:     r.Agents,
			LastActive: r.LastActive,
		}
	}

	s.sendJSON(c, RoomsListResult{
		Type:  "rooms.list.result",
		Rooms: items,
	})
}

func (s *Server) handleMessage(c *Conn, raw []byte) {
	if ok, retryMs := s.msgRate.Allow(c.agent.Profile.ID); !ok {
		s.sendError(c, "RATE_LIMITED", "message rate exceeded", retryMs)
		return
	}

	var msg MessageMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		s.sendError(c, "INVALID_MESSAGE", "invalid message", 0)
		return
	}

	if len(raw) > 65536 {
		s.sendError(c, "INVALID_MESSAGE", "message too large", 0)
		return
	}

	if !agent.VerifySignature(json.RawMessage(raw), c.agent.Profile.ID, msg.Signature) {
		s.sendError(c, "AUTH_FAILED", "invalid signature", 0)
		return
	}

	if msg.From != c.agent.Profile.ID {
		s.sendError(c, "AUTH_FAILED", "from field mismatch", 0)
		return
	}

	if !s.nonces.Check(c.agent.Profile.ID, msg.Nonce, msg.Timestamp) {
		s.sendError(c, "AUTH_FAILED", "invalid nonce or timestamp", 0)
		return
	}

	if !c.agent.Rooms[msg.Room] {
		s.sendError(c, "INVALID_MESSAGE", "not a member of this room", 0)
		return
	}

	// Per-room rate limit
	if ok, retryMs := s.roomMsgRate.Allow("room:" + msg.Room); !ok {
		s.sendError(c, "RATE_LIMITED", "room message rate exceeded", retryMs)
		return
	}

	s.rooms.Touch(msg.Room)

	// Persist to SQLite
	contentJSON, _ := json.Marshal(msg.Content)
	if err := s.store.SaveMessage(msg.ID, msg.Room, msg.From, c.agent.Profile.Name, string(contentJSON), msg.Timestamp); err != nil {
		log.Printf("store error: %v", err)
	}

	// Broadcast to room (except sender)
	s.broadcast(msg.Room, c.agent.Profile.ID, msg)
}

func (s *Server) disconnect(c *Conn) {
	if c.agent == nil {
		c.ws.Close()
		return
	}

	agentID := c.agent.Profile.ID

	s.mu.Lock()
	if current, ok := s.conns[agentID]; ok && current == c {
		delete(s.conns, agentID)
	}
	s.mu.Unlock()

	leftRooms := s.rooms.LeaveAll(agentID)
	for _, roomName := range leftRooms {
		s.broadcast(roomName, agentID, RoomMemberEvent{
			Type:  "room.member_left",
			Room:  roomName,
			Agent: types.MemberInfo{ID: agentID, Name: c.agent.Profile.Name},
		})
	}

	c.ws.Close()
}

func (s *Server) broadcast(roomName, excludeID string, msg interface{}) {
	agents := s.rooms.MembersExcept(roomName, excludeID)
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for _, a := range agents {
		s.mu.RLock()
		conn, ok := s.conns[a.Profile.ID]
		s.mu.RUnlock()
		if ok {
			conn.mu.Lock()
			conn.ws.WriteMessage(websocket.TextMessage, data)
			conn.mu.Unlock()
		}
	}
}

func (s *Server) sendJSON(c *Conn, v interface{}) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.mu.Lock()
	c.ws.WriteMessage(websocket.TextMessage, data)
	c.mu.Unlock()
}

func (s *Server) sendError(c *Conn, code, message string, retryMs int64) {
	s.sendJSON(c, ErrorMsg{
		Type:         "error",
		Code:         code,
		Message:      message,
		RetryAfterMs: retryMs,
	})
}
