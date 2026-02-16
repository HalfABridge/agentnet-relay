package types

import "time"

// Profile is an agent's identity.
type Profile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ConnectedAgent tracks an authenticated agent.
type ConnectedAgent struct {
	Profile     Profile
	ConnectedAt time.Time
	Rooms       map[string]bool
}

// MemberInfo is a summary of an agent in a room.
type MemberInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
