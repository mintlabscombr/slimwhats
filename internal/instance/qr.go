package instance

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// QRState holds the latest QR code event for an instance. The
// whatsmeow library emits the events.QR event (with Codes []string —
// up to 6 codes) whenever the client generates a new QR batch.
// The library rotates through those codes automatically (one every
// 60s for the first code, 20s for the rest), and re-emits a new
// events.QR every minute or so.
//
// We use this struct as a thread-safe buffer between the event
// subscriber (which writes) and the HTTP handlers (which read).
// This replaces the use of whatsmeow's GetQRChannel, which has a
// side effect of calling cli.Disconnect() the moment the caller
// stops reading from the channel — see the comment on
// managedClient.qrState in manager.go for the full explanation.
type QRState struct {
	mu     sync.Mutex
	codes  []string // most recent batch of QR codes (first is the active one)
	signal chan struct{}
}

// NewQRState returns a fresh QRState. The signal channel is
// buffered (capacity 1) so the writer can publish without
// blocking, even if no one is currently reading.
func NewQRState() *QRState {
	return &QRState{signal: make(chan struct{}, 1)}
}

// Set replaces the cached codes. Called from the event subscriber
// whenever a new events.QR arrives. Non-blocking — drops the signal
// if it's already pending (the most recent codes are the only
// ones that matter; intermediate batches that the HTTP handler
// didn't get to will be replaced before they're read).
func (q *QRState) Set(codes []string) {
	q.mu.Lock()
	q.codes = codes
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Get returns the most recent cached codes (empty slice if no
// events.QR has been seen yet).
func (q *QRState) Get() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, len(q.codes))
	copy(out, q.codes)
	return out
}

// WaitForNew blocks until a new code batch arrives, the context
// is cancelled, or the timeout fires. Returns the new codes
// (or nil on timeout/cancel) and a bool indicating whether a new
// batch actually arrived (false = timeout or cancel).
func (q *QRState) WaitForNew(ctx context.Context, prevCodes []string) ([]string, bool) {
	q.mu.Lock()
	current := make([]string, len(q.codes))
	copy(current, q.codes)
	q.mu.Unlock()

	// Fast path: codes already changed since the caller last saw
	// them — no need to block.
	if !sameCodeSet(current, prevCodes) {
		return current, true
	}

	// Otherwise wait for the next signal.
	select {
	case <-q.signal:
		q.mu.Lock()
		defer q.mu.Unlock()
		out := make([]string, len(q.codes))
		copy(out, q.codes)
		return out, true
	case <-ctx.Done():
		return nil, false
	}
}

func sameCodeSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// QRCode is the API response shape for GET /admin/api/instances/{id}/qr.
type QRCode struct {
	QR       string `json:"qr"`
	Format   string `json:"format"` // "raw" — caller renders with their QR library
	IssuedAt string `json:"issued_at"`
}

// ErrAlreadyPaired is returned by GetLatestQR when the client is
// already logged in.
var ErrAlreadyPaired = errors.New("instance already paired")

// GetLatestQR returns the most recent QR code payload from the
// whatsmeow library's events.QR event (consumed by the event
// subscriber and stored in qrState — see manager.go for why we
// don't use GetQRChannel directly).
//
// If no QR has been emitted yet, blocks up to ctx's deadline for
// the first one. If the client is already connected, kicks a
// fresh Connect() in a goroutine to refresh the QR batch.
//
// Only safe to call for unpaired clients — IsLoggedIn() must be
// false (we don't want to disconnect a paired client just to
// fetch a QR).
func GetLatestQR(ctx context.Context, state *QRState, client *whatsmeow.Client) (string, error) {
	if client.IsLoggedIn() {
		return "", ErrAlreadyPaired
	}
	// Diagnostic: log what the whatsmeow library will use to
	// determine the trailing PairClientType field in the QR
	// payload. The library checks (1) cli.QRClientType first, then
	// (2) store.DeviceProps.GetPlatformType() (the global). We
	// force (1) in Manager.Start to be Chrome; the log below
	// confirms the override is in effect and shows what (2) would
	// have resolved to (in case (1) is somehow empty and the
	// fallback fires). Trailing field is what the phone sees as
	// the "client identity" — ",1" (Chrome) is what we want; ",9"
	// (OtherWebClient / fallback) is what gets us rejected.
	slog.Debug("GetLatestQR: client identity for QR",
		"cli_QRClientType", string(client.QRClientType),
		"global_PlatformType", store.DeviceProps.GetPlatformType().String(),
		"global_Version_Primary", store.DeviceProps.GetVersion().GetPrimary(),
		"global_Version_Secondary", store.DeviceProps.GetVersion().GetSecondary())
	// If the client isn't connected yet, kick the connect in a
	// goroutine. (If it IS connected, the library is already
	// rotating codes automatically and the qrState is being
	// updated by the event subscriber.)
	if !client.IsConnected() {
		go func() {
			if err := client.Connect(); err != nil {
				slog.Warn("client.Connect returned error", "err", err)
			}
		}()
	}
	// Wait for the first QR event. The qrState was created at
	// Manager.Start time, but the events.QR hasn't fired yet (the
	// client has to connect and complete the noise handshake first).
	codes, _ := state.WaitForNew(ctx, nil)
	if len(codes) == 0 {
		// Maybe the event was missed during the boot (race
		// between the goroutine writing and this reader
		// registering). One more check on the latest snapshot:
		codes = state.Get()
	}
	if len(codes) == 0 {
		// Still nothing — the QR hasn't been generated yet
		// (client.Connect is still in flight) and the context
		// timed out. Return ctx.Err so the HTTP handler surfaces
		// it.
		return "", ctx.Err()
	}
	// Diagnostic: print the trailing field (PairClientType) so
	// the operator can verify the QR is ",1" (Chrome) and not
	// ",9" (OtherWebClient). Format is
	// "...,<PairClientType>" — the last comma-separated field in
	// the URL fragment after the '#'.
	if i := strings.LastIndex(codes[0], ","); i >= 0 {
		slog.Info("GetLatestQR: emitted QR with client type",
			"client_type", codes[0][i+1:],
			"qr_prefix", codes[0][:min(80, len(codes[0]))]+"...")
	}
	return codes[0], nil
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
