package room

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/betta-lab/agentnet-relay/internal/types"
)

var validRoomName = regexp.MustCompile(`^[a-z0-9\-]{1,64}$`)

// Room is an in-memory room.
type Room struct {
	Name       string
	Topic      string
	Tags       []string
	Members    map[string]*types.ConnectedAgent // agent ID -> agent
	CreatedAt  time.Time
	LastActive time.Time
	mu         sync.RWMutex
}

// Manager manages all rooms.
type Manager struct {
	mu       sync.RWMutex
	rooms    map[string]*Room
	maxRooms int

	// Global rate: room creates per minute
	createCount int
	createReset time.Time
	createLimit int
}

// NewManager creates a room manager.
func NewManager(maxRooms, createLimit int) *Manager {
	m := &Manager{
		rooms:       make(map[string]*Room),
		maxRooms:    maxRooms,
		createLimit: createLimit,
		createReset: time.Now().Add(time.Minute),
	}
	go m.gcLoop()
	return m
}

// Create creates a new room. Returns the room or an error string.
func (m *Manager) Create(name, topic string, tags []string) (*Room, string) {
	name = strings.ToLower(name)

	if !validRoomName.MatchString(name) {
		return nil, "INVALID_MESSAGE"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.rooms[name]; exists {
		return nil, "ROOM_EXISTS"
	}

	if len(m.rooms) >= m.maxRooms {
		return nil, "RATE_LIMITED"
	}

	// Global create rate
	now := time.Now()
	if now.After(m.createReset) {
		m.createCount = 0
		m.createReset = now.Add(time.Minute)
	}
	if m.createCount >= m.createLimit {
		return nil, "RATE_LIMITED"
	}
	m.createCount++

	// Normalize tags
	normalizedTags := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && len(t) <= 32 && len(normalizedTags) < 10 {
			normalizedTags = append(normalizedTags, t)
		}
	}

	r := &Room{
		Name:       name,
		Topic:      topic,
		Tags:       normalizedTags,
		Members:    make(map[string]*types.ConnectedAgent),
		CreatedAt:  now,
		LastActive: now,
	}
	m.rooms[name] = r
	return r, ""
}

// Get returns a room by name, or nil.
func (m *Manager) Get(name string) *Room {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.rooms[strings.ToLower(name)]
}

// Join adds an agent to a room. Returns error string if room not found.
func (m *Manager) Join(roomName string, agent *types.ConnectedAgent) (*Room, []types.MemberInfo, string) {
	m.mu.RLock()
	r, ok := m.rooms[strings.ToLower(roomName)]
	m.mu.RUnlock()
	if !ok {
		return nil, nil, "ROOM_NOT_FOUND"
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.Members) >= 1000 {
		return nil, nil, "RATE_LIMITED"
	}

	r.Members[agent.Profile.ID] = agent

	members := make([]types.MemberInfo, 0, len(r.Members))
	for _, a := range r.Members {
		members = append(members, types.MemberInfo{ID: a.Profile.ID, Name: a.Profile.Name})
	}

	return r, members, ""
}

// Leave removes an agent from a room. Returns true if room was deleted (empty).
func (m *Manager) Leave(roomName, agentID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.rooms[strings.ToLower(roomName)]
	if !ok {
		return false
	}

	r.mu.Lock()
	delete(r.Members, agentID)
	empty := len(r.Members) == 0
	r.mu.Unlock()

	if empty {
		delete(m.rooms, strings.ToLower(roomName))
	}
	return empty
}

// LeaveAll removes an agent from all rooms. Returns rooms the agent was in.
func (m *Manager) LeaveAll(agentID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var left []string
	for name, r := range m.rooms {
		r.mu.Lock()
		if _, ok := r.Members[agentID]; ok {
			delete(r.Members, agentID)
			left = append(left, name)
			if len(r.Members) == 0 {
				delete(m.rooms, name)
			}
		}
		r.mu.Unlock()
	}
	return left
}

// Touch updates last active time for a room.
func (m *Manager) Touch(roomName string) {
	m.mu.RLock()
	r, ok := m.rooms[strings.ToLower(roomName)]
	m.mu.RUnlock()
	if ok {
		r.mu.Lock()
		r.LastActive = time.Now()
		r.mu.Unlock()
	}
}

// List returns rooms matching optional tag filters.
func (m *Manager) List(tags []string, matchAll bool, sort string, limit int) []RoomSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var results []RoomSummary
	for _, r := range m.rooms {
		r.mu.RLock()
		if len(tags) > 0 && !matchTags(r.Tags, tags, matchAll) {
			r.mu.RUnlock()
			continue
		}
		results = append(results, RoomSummary{
			Name:       r.Name,
			Topic:      r.Topic,
			Tags:       r.Tags,
			Agents:     len(r.Members),
			LastActive: r.LastActive.UnixMilli(),
		})
		r.mu.RUnlock()
	}

	sortResults(results, sort)

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results
}

// RoomSummary is used for list responses.
type RoomSummary struct {
	Name       string   `json:"name"`
	Topic      string   `json:"topic"`
	Tags       []string `json:"tags"`
	Agents     int      `json:"agents"`
	LastActive int64    `json:"last_active"`
}

// MembersExcept returns members of a room except the given agent.
func (m *Manager) MembersExcept(roomName, excludeID string) []*types.ConnectedAgent {
	m.mu.RLock()
	r, ok := m.rooms[strings.ToLower(roomName)]
	m.mu.RUnlock()
	if !ok {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var agents []*types.ConnectedAgent
	for id, a := range r.Members {
		if id != excludeID {
			agents = append(agents, a)
		}
	}
	return agents
}

func matchTags(roomTags, filterTags []string, matchAll bool) bool {
	tagSet := make(map[string]bool, len(roomTags))
	for _, t := range roomTags {
		tagSet[t] = true
	}
	for _, t := range filterTags {
		if tagSet[strings.ToLower(t)] {
			if !matchAll {
				return true
			}
		} else if matchAll {
			return false
		}
	}
	return matchAll
}

func sortResults(results []RoomSummary, sort string) {
	// Simple sort by last_active desc (default)
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			swap := false
			switch sort {
			case "agents":
				swap = results[j].Agents > results[i].Agents
			default: // "active"
				swap = results[j].LastActive > results[i].LastActive
			}
			if swap {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
}

func (m *Manager) gcLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		now := time.Now()
		for name, r := range m.rooms {
			r.mu.RLock()
			idle := now.Sub(r.LastActive)
			empty := len(r.Members) == 0
			r.mu.RUnlock()
			if empty || idle > 60*time.Minute {
				delete(m.rooms, name)
			}
		}
		m.mu.Unlock()
	}
}
