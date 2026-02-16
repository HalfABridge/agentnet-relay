package store

import (
	"database/sql"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Store is the SQLite message history store.
type Store struct {
	db *sql.DB
}

// StoredMessage represents a persisted message.
type StoredMessage struct {
	ID        string `json:"id"`
	Room      string `json:"room"`
	FromID    string `json:"from_id"`
	FromName  string `json:"from_name"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

// New opens (or creates) the SQLite database.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite3", path+"?_journal=WAL&_timeout=5000")
	if err != nil {
		return nil, err
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS messages (
			id         TEXT PRIMARY KEY,
			room       TEXT NOT NULL,
			from_id    TEXT NOT NULL,
			from_name  TEXT NOT NULL,
			content    TEXT NOT NULL,
			timestamp  INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_messages_room_ts ON messages(room, timestamp DESC);
	`)
	return err
}

// SaveMessage persists a message.
func (s *Store) SaveMessage(id, room, fromID, fromName, content string, ts int64) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO messages (id, room, from_id, from_name, content, timestamp) VALUES (?, ?, ?, ?, ?, ?)`,
		id, room, fromID, fromName, content, ts,
	)
	return err
}

// GetMessages retrieves messages for a room, newest first.
// If before > 0, returns messages before that timestamp.
func (s *Store) GetMessages(room string, limit int, before int64) ([]StoredMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var rows *sql.Rows
	var err error

	if before > 0 {
		rows, err = s.db.Query(
			`SELECT id, room, from_id, from_name, content, timestamp FROM messages WHERE room = ? AND timestamp < ? ORDER BY timestamp DESC LIMIT ?`,
			room, before, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, room, from_id, from_name, content, timestamp FROM messages WHERE room = ? ORDER BY timestamp DESC LIMIT ?`,
			room, limit,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []StoredMessage
	for rows.Next() {
		var m StoredMessage
		if err := rows.Scan(&m.ID, &m.Room, &m.FromID, &m.FromName, &m.Content, &m.Timestamp); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, nil
}

// GetRooms returns rooms with recent activity.
func (s *Store) GetRooms(limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT DISTINCT room FROM messages ORDER BY timestamp DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rooms []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		rooms = append(rooms, r)
	}
	return rooms, nil
}

// Prune deletes messages older than the given duration.
func (s *Store) Prune(olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan).UnixMilli()
	result, err := s.db.Exec(`DELETE FROM messages WHERE timestamp < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}
