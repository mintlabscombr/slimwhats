// Package auth handles manager sessions and API-key auth.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"time"
)

// Session is a row from admin_sessions.
type Session struct {
	ID         string
	Username   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// SessionStore persists manager sessions in the admin_sessions table.
type SessionStore struct {
	DB         *sql.DB
	TTL        time.Duration // default 12h
	CookieName string        // default "session" — "__Host-session" when SecureCookie=true
}

// NewSessionStore returns a store with the default 12h TTL. The cookie
// name is `__Host-session` only when running over HTTPS (SecureCookie=true);
// otherwise it's plain `session` because the `__Host-` prefix REQUIRES
// the Secure flag and clients (browsers + curl) silently drop it otherwise.
func NewSessionStore(db *sql.DB) *SessionStore {
	return &SessionStore{DB: db, TTL: 12 * time.Hour, CookieName: "session"}
}

// NewSessionStoreWithCookieName lets the caller pick the cookie name
// (e.g. "__Host-session" in production over HTTPS).
func NewSessionStoreWithCookieName(db *sql.DB, cookieName string) *SessionStore {
	return &SessionStore{DB: db, TTL: 12 * time.Hour, CookieName: cookieName}
}

// ErrInvalidSession is returned by Validate when the session ID doesn't
// match any non-expired row.
var ErrInvalidSession = errors.New("invalid or expired session")

// Create mints a new session for the given username, persists it, and
// returns the new Session. The session ID is 32 random bytes, base64url.
func (s *SessionStore) Create(ctx context.Context, username string) (*Session, error) {
	id, err := newSessionID()
	if err != nil {
		return nil, fmt.Errorf("mint id: %w", err)
	}
	now := time.Now().UTC()
	expires := now.Add(s.TTL)
	_, err = s.DB.ExecContext(ctx,
		`INSERT INTO admin_sessions (id, username, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		id, username, now, expires, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert session: %w", err)
	}
	return &Session{ID: id, Username: username, CreatedAt: now, ExpiresAt: expires, LastSeenAt: now}, nil
}

// Validate looks up a session by ID, returns ErrInvalidSession if it
// doesn't exist or is expired. On success it also updates last_seen_at
// (sliding refresh) when refresh is true.
func (s *SessionStore) Validate(ctx context.Context, id string, refresh bool) (*Session, error) {
	if id == "" {
		return nil, ErrInvalidSession
	}
	row := s.DB.QueryRowContext(ctx,
		`SELECT id, username, created_at, expires_at, last_seen_at FROM admin_sessions WHERE id = ?`, id)
	var sess Session
	if err := row.Scan(&sess.ID, &sess.Username, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidSession
		}
		return nil, fmt.Errorf("scan session: %w", err)
	}
	if time.Now().UTC().After(sess.ExpiresAt) {
		_ = s.Delete(ctx, id)
		return nil, ErrInvalidSession
	}
	if refresh {
		newExpires := time.Now().UTC().Add(s.TTL)
		_, _ = s.DB.ExecContext(ctx,
			`UPDATE admin_sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
			time.Now().UTC(), newExpires, id)
		sess.LastSeenAt = time.Now().UTC()
		sess.ExpiresAt = newExpires
	}
	return &sess, nil
}

// Delete invalidates a session by ID. No-op if the session doesn't exist.
func (s *SessionStore) Delete(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id = ?`, id)
	return err
}

// CompareConstantTime does a constant-time string comparison. Used by the
// login handler to verify the submitted password against the env-stored one.
func CompareConstantTime(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func newSessionID() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
