package relay

import "github.com/betta-lab/agentnet-relay/internal/types"

// Envelope is the base structure for all protocol messages.
type Envelope struct {
	Type      string `json:"type"`
	Room      string `json:"room,omitempty"`
	Nonce     string `json:"nonce,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Signature string `json:"signature,omitempty"`
}

// HelloMsg is sent by agents to authenticate.
type HelloMsg struct {
	Type      string        `json:"type"`
	Profile   types.Profile `json:"profile"`
	Timestamp int64         `json:"timestamp"`
	Nonce     string        `json:"nonce"`
	Signature string        `json:"signature"`
}

// HelloPowMsg is the PoW solution for handshake.
type HelloPowMsg struct {
	Type      string   `json:"type"`
	Pow       PowProof `json:"pow"`
	Signature string   `json:"signature"`
}

// PowProof contains the challenge and proof.
type PowProof struct {
	Challenge string `json:"challenge"`
	Proof     string `json:"proof"`
}

// PowChallenge is sent by the relay.
type PowChallenge struct {
	Type       string `json:"type"`
	Challenge  string `json:"challenge"`
	Difficulty int    `json:"difficulty"`
	Expires    int64  `json:"expires"`
}

// WelcomeMsg is sent after successful auth.
type WelcomeMsg struct {
	Type  string `json:"type"`
	Relay string `json:"relay"`
}

// ErrorMsg is sent for errors.
type ErrorMsg struct {
	Type         string `json:"type"`
	Code         string `json:"code"`
	Message      string `json:"message"`
	RetryAfterMs int64  `json:"retry_after_ms,omitempty"`
}

// RoomJoinMsg is sent by agents to join a room.
type RoomJoinMsg struct {
	Type      string `json:"type"`
	Room      string `json:"room"`
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

// RoomCreateMsg is sent by agents to create a room.
type RoomCreateMsg struct {
	Type      string   `json:"type"`
	Room      string   `json:"room"`
	Topic     string   `json:"topic,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Pow       *PowProof `json:"pow,omitempty"`
	Nonce     string   `json:"nonce"`
	Timestamp int64    `json:"timestamp"`
	Signature string   `json:"signature"`
}

// RoomLeaveMsg is sent by agents to leave a room.
type RoomLeaveMsg struct {
	Type      string `json:"type"`
	Room      string `json:"room"`
	Nonce     string `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

// RoomJoinedMsg is the response when joining a room.
type RoomJoinedMsg struct {
	Type    string             `json:"type"`
	Room    string             `json:"room"`
	Topic   string             `json:"topic"`
	Tags    []string           `json:"tags"`
	Members []types.MemberInfo `json:"members"`
}

// RoomMemberEvent is broadcast when a member joins or leaves.
type RoomMemberEvent struct {
	Type  string           `json:"type"`
	Room  string           `json:"room"`
	Agent types.MemberInfo `json:"agent"`
}

// RoomsListMsg is a request to list rooms.
type RoomsListMsg struct {
	Type   string   `json:"type"`
	Tags   []string `json:"tags,omitempty"`
	Match  string   `json:"match,omitempty"`
	Sort   string   `json:"sort,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
}

// RoomListItem is a room in a list response.
type RoomListItem struct {
	Name       string   `json:"name"`
	Topic      string   `json:"topic"`
	Tags       []string `json:"tags"`
	Agents     int      `json:"agents"`
	LastActive int64    `json:"last_active"`
}

// RoomsListResult is the response to rooms.list.
type RoomsListResult struct {
	Type    string         `json:"type"`
	Rooms   []RoomListItem `json:"rooms"`
	Cursor  string         `json:"cursor,omitempty"`
	HasMore bool           `json:"has_more"`
}

// MessageMsg is a room message.
type MessageMsg struct {
	Type      string         `json:"type"`
	ID        string         `json:"id"`
	Room      string         `json:"room"`
	From      string         `json:"from"`
	Content   MessageContent `json:"content"`
	Timestamp int64          `json:"timestamp"`
	Nonce     string         `json:"nonce"`
	Signature string         `json:"signature"`
}

// MessageContent is the content of a message.
type MessageContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
