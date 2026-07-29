// Package handlers — admin audit log (US-030, FR-18).
//
// Every state-changing admin action goes through AuditLogger.Log so we
// have a complete trail of who did what when. The plaintext API key
// and the manager password are NEVER stored — only the event fact
// (e.g. "instance.api_key_revealed") and the target id.
package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"
)

// AuditLoggerImpl is the concrete implementation of the AuditLogger
// interface used by the lifecycle handlers.
type AuditLoggerImpl struct {
	DB *sql.DB
}

// NewAuditLogger returns an AuditLoggerImpl ready to use. Pass nil
// for db to disable audit logging.
func NewAuditLogger(db *sql.DB) *AuditLoggerImpl {
	return &AuditLoggerImpl{DB: db}
}

// Log inserts a row into admin_actions. The data map is JSON-encoded
// for the `data` column; nil data is stored as NULL. Returns
// silently on error (audit must never block the user's request —
// we just log to slog and move on).
func (a *AuditLoggerImpl) Log(ctx context.Context, action, targetID, username, sourceIP, userAgent string, data map[string]any) {
	if a == nil || a.DB == nil {
		return
	}
	var dataJSON any
	if data != nil {
		b, err := json.Marshal(data)
		if err != nil {
			slog.Warn("audit marshal", "err", err)
		} else {
			dataJSON = string(b)
		}
	}
	now := time.Now().UTC()
	_, err := a.DB.ExecContext(ctx, `
		INSERT INTO admin_actions
			(timestamp, username, action, target_id, source_ip, user_agent, data)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		now, nullableStr(username), action, nullableStr(targetID),
		nullableStr(sourceIP), nullableStr(userAgent), dataJSON,
	)
	if err != nil {
		slog.Warn("audit insert", "action", action, "err", err)
	}
}

// nullableStr returns nil for empty so we can use it directly in
// nullable TEXT columns. Pulled into one place so the audit file
// doesn't import the webhook file's version (they're not the same
// function even though they look the same).
func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
