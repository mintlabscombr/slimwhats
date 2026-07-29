package instance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
)

// QRState holds the latest QR code event for an instance. The whatsmeow
// QR channel is fire-and-forget per login attempt; we cache the latest
// payload so a second call returns the cached value while a fresh one
// is being generated.
type QRState struct {
	mu        sync.Mutex
	latest    string // raw payload from the QR channel
	expiresCh chan struct{}
}

// NewQRState returns a fresh QRState.
func NewQRState() *QRState { return &QRState{} }

// Set updates the latest QR payload. Empty string signals "expired".
func (q *QRState) Set(payload string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.latest = payload
}

// Get returns the latest QR payload (empty if expired).
func (q *QRState) Get() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.latest
}

// QRCode is the API response shape for GET /admin/api/instances/{id}/qr.
type QRCode struct {
	QR       string `json:"qr"`
	Format   string `json:"format"` // "raw" — caller renders with their QR library
	IssuedAt string `json:"issued_at"`
}

// EnsureQRChannel starts (or re-starts) the whatsmeow QR pairing channel
// for the given client and returns a channel of QR events. The caller
// must drain the channel until either an "success" event arrives or
// the context is cancelled.
func EnsureQRChannel(ctx context.Context, client *whatsmeow.Client) (<-chan whatsmeow.QRChannelItem, error) {
	if client == nil {
		return nil, errors.New("client is nil")
	}
	if client.IsLoggedIn() {
		return nil, ErrAlreadyPaired
	}
	if client.Store.ID == nil {
		// NewDevice() — need to start with QR login
	}
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return nil, fmt.Errorf("get qr channel: %w", err)
	}
	// Non-blocking connect in a goroutine
	go func() {
		if err := client.Connect(); err != nil {
			// context-canceled is expected; other errors are logged elsewhere
			_ = err
		}
	}()
	return qrChan, nil
}

// ErrAlreadyPaired is returned by EnsureQRChannel when the client is
// already logged in.
var ErrAlreadyPaired = errors.New("instance already paired")

// GetLatestQR pulls one payload off the QR channel with a short timeout.
// Returns the payload as a base64-encoded string (callers can render
// it as a PNG).
//
// IMPORTANT: whatsmeow's GetQRChannel must be called BEFORE the client
// has ever been Connect()'d. If the client is already in a connected
// state (e.g. the operator opened the detail page once, got a QR,
// didn't scan it within 60s, and is now refreshing the page), the
// channel call returns "GetQRChannel must be called before connecting"
// and no QR is returned. The fix is to Disconnect first, then start a
// fresh GetQRChannel → Connect cycle.
//
// Only safe to call for unpaired clients — IsLoggedIn() must be false
// (we don't want to disconnect a paired client just to fetch a QR).
func GetLatestQR(ctx context.Context, client *whatsmeow.Client) (string, error) {
	if client.IsLoggedIn() {
		return "", ErrAlreadyPaired
	}
	// If the client is already connected, disconnect first so we can
	// set up a fresh QR channel. This is a no-op for paired clients
	// (we return early above) and only affects unpaired clients in
	// the "connected, waiting for QR scan" state.
	if client.IsConnected() {
		client.Disconnect()
		// Give whatsmeow a moment to release the socket before we
		// re-init the QR channel.
		time.Sleep(100 * time.Millisecond)
	}
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return "", fmt.Errorf("get qr channel: %w", err)
	}
	go func() {
		if err := client.Connect(); err != nil {
			// context-canceled is expected; other errors are surfaced
			// by the event subscriber (which writes to instance_logs).
			_ = err
		}
	}()
	select {
	case item := <-qrChan:
		if item.Event == "success" {
			return "", nil
		}
		return item.Code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// SetWebhook persists the webhook URL and secret for an instance.
// Pass empty url/secret to clear.
//
// v1 simplification: the secret is stored as plaintext in the DB. This
// is a deliberate trade-off — the AES-256-GCM layer was overkill for
// the use case. The on-disk threat model now assumes the DB file is
// protected at the operator's discretion (filesystem permissions,
// disk encryption, etc.). If you need at-rest encryption in the
// future, wrap this function in a call to the encryption helper of
// your choice.
func (s *Store) SetWebhook(id, url, secret string) error {
	if url == "" {
		// Clear
		_, err := s.DB.Exec(`UPDATE instances SET webhook_url = NULL, webhook_secret = NULL WHERE id = ?`, id)
		return err
	}
	if secret == "" {
		return errors.New("secret is required when url is set")
	}
	_, err := s.DB.Exec(`
		UPDATE instances
		SET webhook_url = ?, webhook_secret = ?
		WHERE id = ?`, url, secret, id)
	return err
}

// LoadWebhookSecret returns the webhook URL and secret for an instance.
// Returns ("", "", nil) when no webhook is configured.
func (s *Store) LoadWebhookSecret(id string) (url, secret string, err error) {
	row := s.DB.QueryRow(`SELECT webhook_url, webhook_secret FROM instances WHERE id = ?`, id)
	var urlVal, secretVal sql.NullString
	if err := row.Scan(&urlVal, &secretVal); err != nil {
		return "", "", err
	}
	if !urlVal.Valid {
		return "", "", nil
	}
	if !secretVal.Valid {
		return urlVal.String, "", nil
	}
	return urlVal.String, secretVal.String, nil
}
