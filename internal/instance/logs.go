// Package instance — instance_logs table access (US-029).
//
// Logs are populated by the event subscriber in main.go. Every
// whatsmeow event is mirrored here so operators have a queryable
// timeline of what happened to an instance, in addition to the
// outbound webhook stream.
package instance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// LogLevel is the severity of a log entry. Stored as TEXT in the DB.
type LogLevel string

const (
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
	LogLevelDebug LogLevel = "debug"
)

// LogCategory is the source of the log entry. The event normalizer in
// the webhook package maps whatsmeow event types to one of these.
type LogCategory string

const (
	LogCategoryConnect    LogCategory = "connect"
	LogCategoryMessage    LogCategory = "message"
	LogCategoryReceipt    LogCategory = "receipt"
	LogCategoryGroup      LogCategory = "group"
	LogCategoryContact    LogCategory = "contact"
	LogCategoryPresence   LogCategory = "presence"
	LogCategorySystem     LogCategory = "system"
)

// LogEntry is one row in the instance_logs table.
type LogEntry struct {
	ID         string
	InstanceID string
	Timestamp  time.Time
	Level      LogLevel
	Category   LogCategory
	Message    string
	Data       json.RawMessage
}

// LogQuery holds filter parameters for ListLogs.
type LogQuery struct {
	InstanceID string
	Level      string // optional
	Category   string // optional
	Since      *time.Time // optional
	Limit      int
	Offset     int
}

// InsertLog appends a new entry. The ID is auto-minted as a UUIDv4
// (same scheme as newID for the instances table).
func (s *Store) InsertLog(ctx context.Context, instanceID string, level LogLevel, category LogCategory, message string, data map[string]any) error {
	id, err := newID()
	if err != nil {
		return err
	}
	var dataJSON string
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal log data: %w", err)
		}
		dataJSON = string(b)
	} else {
		dataJSON = "{}"
	}
	_, err = s.DB.ExecContext(ctx, `
		INSERT INTO instance_logs (id, instance_id, timestamp, level, category, message, data)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, instanceID, time.Now().UTC(), string(level), string(category), message, dataJSON,
	)
	return err
}

// ListLogs returns log entries for one instance, ordered by timestamp
// DESC, with optional level/category/since filters.
func (s *Store) ListLogs(ctx context.Context, q LogQuery) ([]LogEntry, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 50
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	query := `SELECT id, instance_id, timestamp, level, category, message, data
		FROM instance_logs WHERE instance_id = ?`
	args := []any{q.InstanceID}
	if q.Level != "" {
		query += " AND level = ?"
		args = append(args, q.Level)
	}
	if q.Category != "" {
		query += " AND category = ?"
		args = append(args, q.Category)
	}
	if q.Since != nil {
		query += " AND timestamp >= ?"
		args = append(args, *q.Since)
	}
	query += " ORDER BY timestamp DESC LIMIT ? OFFSET ?"
	args = append(args, q.Limit, q.Offset)

	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		var dataStr string
		if err := rows.Scan(&e.ID, &e.InstanceID, &e.Timestamp, &e.Level, &e.Category, &e.Message, &dataStr); err != nil {
			return nil, err
		}
		if dataStr != "" {
			e.Data = json.RawMessage(dataStr)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountLogsByLevel returns the number of log entries at or above a
// given level. Used by the retention pruner (future US).
func (s *Store) CountLogsByLevel(ctx context.Context, instanceID string, level LogLevel) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM instance_logs WHERE instance_id = ? AND level = ?`,
		instanceID, string(level)).Scan(&n)
	return n, err
}

// sqlDB is a tiny alias so the file compiles standalone (the *sql.DB
// is the only thing we need from store.Store). Defensive: avoids a
// no-op if someone deletes the import.
var _ = sql.ErrNoRows
