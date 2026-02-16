package relay

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// handleAPIRooms returns rooms with recent message activity.
// GET /api/rooms
func (s *Server) handleAPIRooms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := parseIntParam(r, "limit", 50)
	rooms, err := s.store.GetRooms(limit)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"rooms": rooms,
	})
}

// handleAPIRoomMessages returns message history for a room.
// GET /api/rooms/{name}/messages?limit=50&before=<timestamp>
func (s *Server) handleAPIRoomMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse room name from path: /api/rooms/{name}/messages
	path := strings.TrimPrefix(r.URL.Path, "/api/rooms/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 || parts[1] != "messages" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	roomName := parts[0]

	limit := parseIntParam(r, "limit", 50)
	before := parseIntParam(r, "before", 0)

	msgs, err := s.store.GetMessages(roomName, limit, int64(before))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"room":     roomName,
		"messages": msgs,
	})
}

func parseIntParam(r *http.Request, name string, defaultVal int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return defaultVal
	}
	return v
}
