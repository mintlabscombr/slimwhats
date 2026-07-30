// Package instance defines the instance data model and the manager that
// owns the whatsmeow Client for each row in the `instances` table.
package instance

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Status is the lifecycle state of an instance.
type Status string

const (
	StatusCreated      Status = "created"
	StatusPairing      Status = "pairing"
	StatusConnected    Status = "connected"
	StatusDisconnected Status = "disconnected"
	StatusLoggedOut    Status = "logged_out"
	StatusError        Status = "error"
)

// Instance is the row in the `instances` table.
type Instance struct {
	ID            string
	Name          string
	APIKey        string // plaintext (post 2026-07-29 drop-bcrypt). May be empty if not yet rotated.
	WebhookURL    sql.NullString
	WebhookSecret sql.NullString // plaintext (post 2026-07-29 drop-encryption). Column is BLOB but the bytes are ASCII.
	Status        Status
	Phone         sql.NullString
	JID           sql.NullString
	LID           sql.NullString
	ConnectedAt   sql.NullTime
	LastSeenAt    sql.NullTime
	APISetAt      sql.NullTime
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// APIKeyRegex enforces the operator-supplied key shape. The auto-generated
// keys are constructed to match this same pattern.
var APIKeyRegex = regexp.MustCompile(`^sk_live_[A-Za-z0-9]{16,128}$`)

// ErrNameTaken is returned by Create when an instance with the same
// name already exists.
var ErrNameTaken = errors.New("instance name already taken")

// ErrNotLoaded is returned by Manager.Disconnect when the instance
// is not in the in-memory map (i.e. hasn't been started).
var ErrNotLoaded = errors.New("instance not loaded")

// Store is the DB layer for instances. Whatsmeow client lifecycle is
// managed separately by the Manager.
type Store struct {
	DB *sql.DB
}

// NewStore returns a Store wrapping the given DB.
func NewStore(db *sql.DB) *Store {
	return &Store{DB: db}
}

// CreateInput is the validated input for creating an instance.
type CreateInput struct {
	Name   string
	APIKey string // plaintext; stored as-is in the api_key column
}

// Create persists a new instance in the `created` status with the API
// key stored in plaintext (no bcrypt). If APIKey is empty, one is
// auto-generated. Returns the new instance and the plaintext API key
// (caller must surface it to the operator once).
func (s *Store) Create(in CreateInput) (*Instance, string, error) {
	if in.Name == "" {
		return nil, "", errors.New("name is required")
	}
	if len(in.Name) > 64 {
		return nil, "", errors.New("name must be ≤ 64 chars")
	}
	plaintext := in.APIKey
	if plaintext == "" {
		var err error
		plaintext, err = generateAPIKey()
		if err != nil {
			return nil, "", fmt.Errorf("generate api key: %w", err)
		}
	} else if !APIKeyRegex.MatchString(plaintext) {
		return nil, "", errors.New("api_key must match ^sk_live_[A-Za-z0-9]{16,128}$")
	}

	id, err := newID()
	if err != nil {
		return nil, "", fmt.Errorf("mint id: %w", err)
	}
	now := time.Now().UTC()
	_, err = s.DB.Exec(`
		INSERT INTO instances
			(id, name, api_key, status, api_key_set_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, in.Name, plaintext, string(StatusCreated), now, now, now,
	)
	if err != nil {
		// Best-effort uniqueness detection: SQLite uses "UNIQUE constraint failed"
		// and Postgres uses "duplicate key value violates unique constraint".
		if isUniqueViolation(err) {
			return nil, "", ErrNameTaken
		}
		return nil, "", fmt.Errorf("insert instance: %w", err)
	}
	return &Instance{
		ID:        id,
		Name:      in.Name,
		APIKey:    plaintext,
		Status:    StatusCreated,
		APISetAt:  sql.NullTime{Time: now, Valid: true},
		CreatedAt: now,
		UpdatedAt: now,
	}, plaintext, nil
}

// GetByID returns the instance with the given id, or nil if not found.
func (s *Store) GetByID(id string) (*Instance, error) {
	row := s.DB.QueryRow(`
		SELECT id, name, api_key, webhook_url, webhook_secret, status, phone, jid, lid,
		       connected_at, last_seen_at, api_key_set_at, created_at, updated_at
		FROM instances WHERE id = ?`, id)
	return scanInstance(row)
}

// GetByName returns the instance with the given name, or nil if not found.
func (s *Store) GetByName(name string) (*Instance, error) {
	row := s.DB.QueryRow(`
		SELECT id, name, api_key, webhook_url, webhook_secret, status, phone, jid, lid,
		       connected_at, last_seen_at, api_key_set_at, created_at, updated_at
		FROM instances WHERE name = ?`, name)
	return scanInstance(row)
}

// ListAll returns up to limit instances starting at offset, ordered by
// created_at DESC.
func (s *Store) ListAll(limit, offset int) ([]*Instance, error) {
	rows, err := s.DB.Query(`
		SELECT id, name, api_key, webhook_url, webhook_secret, status, phone, jid, lid,
		       connected_at, last_seen_at, api_key_set_at, created_at, updated_at
		FROM instances ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer rows.Close()
	var out []*Instance
	for rows.Next() {
		inst, err := scanInstanceRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inst)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInstance(row rowScanner) (*Instance, error) {
	inst, err := scanInstanceRows(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return inst, err
}

func scanInstanceRows(row rowScanner) (*Instance, error) {
	var inst Instance
	var status string
	var apiKey sql.NullString
	err := row.Scan(
		&inst.ID, &inst.Name, &apiKey, &inst.WebhookURL, &inst.WebhookSecret,
		&status, &inst.Phone, &inst.JID, &inst.LID,
		&inst.ConnectedAt, &inst.LastSeenAt, &inst.APISetAt,
		&inst.CreatedAt, &inst.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if apiKey.Valid {
		inst.APIKey = apiKey.String
	}
	inst.Status = Status(status)
	return &inst, nil
}

// generateAPIKey returns a fresh API key that matches APIKeyRegex.
func generateAPIKey() (string, error) {
	// 32 random bytes → 43-char base64url. Combined with the "sk_live_"
	// prefix the total length is 50 chars, well within the 16-128 range.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk_live_" + base64URLNoPad(buf), nil
}

// GenerateAPIKey is the exported version of generateAPIKey, used by
// the API-key rotate handler.
func GenerateAPIKey() (string, error) { return generateAPIKey() }

// SetAPIKey replaces the api_key (plaintext, post 2026-07-29 drop-
// bcrypt) for an instance and bumps api_key_set_at + updated_at.
// Used by the rotate and set-custom flows.
func (s *Store) SetAPIKey(id, newPlaintext string) error {
	now := time.Now().UTC()
	_, err := s.DB.Exec(`UPDATE instances SET api_key = ?, api_key_set_at = ?, updated_at = ? WHERE id = ?`,
		newPlaintext, now, now, id)
	return err
}

// SetStatus updates the instance's lifecycle status (and
// connected_at / last_seen_at where relevant). Used by the event
// subscriber in main.go.
func (s *Store) SetStatus(id string, status Status, connectedAt *time.Time, lastSeenAt *time.Time) error {
	now := time.Now().UTC()
	_, err := s.DB.Exec(`
		UPDATE instances
		SET status = ?, connected_at = COALESCE(?, connected_at), last_seen_at = COALESCE(?, last_seen_at), updated_at = ?
		WHERE id = ?`,
		string(status), connectedAt, lastSeenAt, now, id)
	return err
}

// SetIdentity persists the device's JID, LID, and phone number
// once the pairing completes. The whatsmeow event stream fires
// events.Connected on every connect (including reconnects of an
// already-known device), so this gets called on each handshake
// success — overwriting any previous values. Phone is derived from
// the JID by stripping the ":deviceid" suffix (the format is
// "phone:deviceid@s.whatsapp.net", e.g. "5551933811858:8@s.whatsapp.net"
// → phone "5551933811858"). LID is the new privacy-preserving
// identifier (e.g. "243546758062161:8@lid") and is set independently.
//
// Used by the event subscriber in main.go's Connected handler so
// the manager UI and admin API can show the phone / JID / LID that
// the device is actually announcing.
func (s *Store) SetIdentity(id, jid, lid, phone string) error {
	now := time.Now().UTC()
	// Use empty string to mean "leave existing value" so a
	// partial update (e.g. LID-only) doesn't clobber a known JID.
	// The caller can pass "" for fields they want to keep.
	_, err := s.DB.Exec(`
		UPDATE instances
		SET jid = COALESCE(NULLIF(?, ''), jid),
		    lid = COALESCE(NULLIF(?, ''), lid),
		    phone = COALESCE(NULLIF(?, ''), phone),
		    updated_at = ?
		WHERE id = ?`,
		jid, lid, phone, now, id)
	return err
}

// Delete removes an instance row (and by CASCADE, its webhook config
// columns, deliveries, and instance_logs). The whatsmeow device row
// in the same DB is preserved so re-creation with the same name does
// not require re-pairing. If you really want to nuke the device, do
// it via the whatsmeow Container API separately.
func (s *Store) Delete(id string) error {
	_, err := s.DB.Exec(`DELETE FROM instances WHERE id = ?`, id)
	return err
}

// newID returns a fresh UUIDv4.
func newID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	// Set version 4 and variant bits per RFC 4122.
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16]), nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return contains(msg, "UNIQUE constraint failed") || contains(msg, "duplicate key value")
}

func contains(s, sub string) bool {
	return len(sub) <= len(s) && (len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
