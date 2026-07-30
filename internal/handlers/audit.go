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
	DB  *sql.DB
	Bus *AuditBus // optional; if set, every Log call also publishes
}

// NewAuditLogger returns an AuditLoggerImpl ready to use. Pass nil
// for db to disable audit logging. bus may be nil — a nil bus is
// a no-op publisher (see AuditBus.Publish).
func NewAuditLogger(db *sql.DB, bus *AuditBus) *AuditLoggerImpl {
	return &AuditLoggerImpl{DB: db, Bus: bus}
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
	} else {
		dataJSON = "{}"
	}
	now := time.Now().UTC()
	// The NOT NULL columns (username, source_ip) need real values; fall
	// back to a placeholder so an empty string doesn't blow up the
	// insert.
	if username == "" {
		username = "(unknown)"
	}
	if sourceIP == "" {
		sourceIP = "0.0.0.0"
	}
	_, err := a.DB.ExecContext(ctx, `
		INSERT INTO admin_actions
			(timestamp, username, action, target_id, source_ip, user_agent, data)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		now, username, action, nullableStr(targetID),
		sourceIP, nullableStr(userAgent), dataJSON,
	)
	if err != nil {
		slog.Warn("audit insert", "action", action, "err", err)
	}
	// Fire-and-forget publish. The DB is the source of truth; the bus
	// is a best-effort hint to connected browsers. Publish happens
	// regardless of the DB outcome — if the insert failed, the operator
	// still wants to see the attempt on the live page (it's the only
	// signal that something happened, since the row never landed). The
	// bus itself is non-blocking (per-subscriber buffer = 16; full
	// buffers drop the entry for that subscriber).
	//
	// Timestamp is formatted the same way the audit page renders it
	// ("2006-01-02 15:04:05 UTC") so SSE-inserted rows look identical
	// to the rows the page loaded from the DB. Note: the initial
	// render reads timestamps as Go's default time.Time.String() via
	// the sqlite scan, which is uglier. Matching it would mean
	// inheriting the ugliness; we use the clean format instead and
	// accept the minor visual inconsistency on the first 100 rows.
	if a.Bus != nil {
		a.Bus.Publish(AuditEntry{
			Timestamp: now.UTC().Format("2006-01-02 15:04:05 UTC"),
			Username:  username,
			Action:    action,
			TargetID:  targetID,
			SourceIP:  sourceIP,
			UserAgent: userAgent,
		})
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
