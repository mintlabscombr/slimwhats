package instance

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"

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
func GetLatestQR(ctx context.Context, client *whatsmeow.Client) (string, error) {
	if client.IsLoggedIn() {
		return "", ErrAlreadyPaired
	}
	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		return "", fmt.Errorf("get qr channel: %w", err)
	}
	go func() {
		if err := client.Connect(); err != nil {
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

// EncryptSecret AES-256-GCM-encrypts the webhook secret for at-rest
// storage. Returns base64(nonce || ciphertext).
func EncryptSecret(key []byte, plaintext string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// DecryptSecret reverses EncryptSecret.
func DecryptSecret(key []byte, ciphertextB64 string) (string, error) {
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	raw, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// SetWebhook persists the webhook URL and encrypted secret for an instance.
// Pass empty url/secret to clear.
func (s *Store) SetWebhook(id, url, secret string, encryptionKey []byte) error {
	if url == "" {
		// Clear
		_, err := s.DB.Exec(`UPDATE instances SET webhook_url = NULL, webhook_secret_encrypted = NULL WHERE id = ?`, id)
		return err
	}
	if secret == "" {
		return errors.New("secret is required when url is set")
	}
	encrypted, err := EncryptSecret(encryptionKey, secret)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`
		UPDATE instances
		SET webhook_url = ?, webhook_secret_encrypted = ?
		WHERE id = ?`, url, encrypted, id)
	return err
}

// LoadWebhookSecret returns the decrypted webhook secret for an instance.
func (s *Store) LoadWebhookSecret(id string, encryptionKey []byte) (url, secret string, err error) {
	row := s.DB.QueryRow(`SELECT webhook_url, webhook_secret_encrypted FROM instances WHERE id = ?`, id)
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
	plaintext, err := DecryptSecret(encryptionKey, secretVal.String)
	if err != nil {
		return "", "", err
	}
	return urlVal.String, plaintext, nil
}
