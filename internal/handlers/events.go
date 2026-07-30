package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow/types/events"

	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// EventsHandler streams whatsmeow events AND audit log entries to
// the browser over Server-Sent Events. The browser opens an
// `EventSource` on /admin/api/events; this handler subscribes to
// the manager (for whatsmeow events) and to the AuditBus (for
// audit entries), filters the relevant event types, and writes
// them as SSE messages.
//
// Wire format (one SSE message per emitted event):
//
//	event: status\ndata: {"instance_id":"abc","status":"connected",...}\n\n
//	event: qr_update\ndata: {"instance_id":"abc"}\n\n
//	event: audit_entry\ndata: {"timestamp":"2026-07-30 ...","username":...}\n\n
//	: ping\n\n                       (heartbeat comment every 30s)
//
// Filtered event types (everything else is dropped):
//
//	Connected, Disconnected, LoggedOut, ConnectFailure,
//	PairError, PairPasskeyError, PairSuccess,
//	Message (so last_seen_at updates when a message arrives),
//	QR (so the pairing screen can re-fetch the latest PNG — US-041).
//
// Audit entries (F-04): every entry written by AuditLoggerImpl.Log
// is also Published on the AuditBus. The bus is per-process; the
// SSE handler Subscribes once per open connection. The bus drops
// entries for slow consumers instead of blocking the audit writer.
//
// Per-connection subscription: each open SSE connection gets its own
// AddEventHandler on every managed whatsmeow client. When the client
// disconnects (browser closes the tab, network drops, etc.) the
// handler unsubscribes from every client. See
// instance.Manager.AddEventHandler for the underlying machinery.
//
// Backpressure: writes go behind a per-connection mutex and a 1s
// watchdog. A slow client (network jam, page hidden, etc.) can't wedge
// the event source — the connection is dropped and the subscription
// is cleaned up.
func EventsHandler(mgr *instance.Manager, bus *AuditBus) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Set SSE headers BEFORE writing anything — gin won't let us
		// change the status code or content type after the first write.
		h := c.Writer.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no") // disable nginx response buffering if proxied
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()

		// Per-connection writer with 1s watchdog. safeWrite returns
		// false if the client can't keep up or the connection is gone.
		var writeMu sync.Mutex
		safeWrite := func(event string, data any) bool {
			writeMu.Lock()
			defer writeMu.Unlock()
			payload := formatSSE(event, data)
			done := make(chan error, 1)
			go func() {
				_, err := c.Writer.Write([]byte(payload))
				done <- err
			}()
			select {
			case <-time.After(time.Second):
				return false
			case err := <-done:
				if err != nil {
					return false
				}
				c.Writer.Flush()
				return true
			}
		}

		// Subscribe to manager events. The cancel func removes our
		// handler from every client.
		cancelSub := mgr.AddEventHandler(func(instanceID string, evt interface{}) {
			emitEvent(c, instanceID, evt, safeWrite)
		})
		defer cancelSub()

		// Subscribe to the audit bus (best-effort; bus may be nil
		// in tests or in the rare deployment that disables the
		// audit stream — the handler still works, just without
		// audit_entry events).
		auditCh, cancelAudit := bus.Subscribe()
		defer cancelAudit()

		// Heartbeat: a 30s SSE comment keeps the TCP connection warm
		// across proxies and load balancers. The browser ignores
		// comment lines (they start with ':').
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		// Block until the client disconnects or a write fails.
		for {
			select {
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				if !safeWrite("ping", nil) {
					return
				}
			case e, ok := <-auditCh:
				// Channel closed by the bus (server shutdown or
				// explicit cancel). Not an error — just stop
				// reading and let the rest of the loop drain.
				if !ok {
					auditCh = nil // disable this case
					continue
				}
				if !safeWrite("audit_entry", e) {
					return
				}
			}
		}
	}
}

// emitEvent maps a whatsmeow event to one or more SSE messages.
// The status field always reflects what the manager UI cares about
// (not the raw whatsmeow type); identity fields (phone, jid, lid)
// are filled when the source event carries them; last_seen is set
// on every Message.
func emitEvent(_ *gin.Context, instanceID string, evt interface{}, write func(string, any) bool) {
	switch e := evt.(type) {
	case *events.Connected:
		// Connected event itself is empty in whatsmeow. The DB layer
		// (handlers/admin_ui.go, main.go's event subscription) is
		// responsible for persisting phone/jid/lid; the JS in
		// US-040 re-fetches the full instance state to pick them up.
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "connected",
		})
	case *events.Disconnected:
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "disconnected",
		})
	case *events.LoggedOut:
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "logged_out",
		})
	case *events.ConnectFailure:
		// Server said <failure> on the connect handshake — "Cant link
		// new devices right now", "Too many devices", etc. Surface
		// as 'error' so the badge turns red.
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "error",
		})
	case *events.PairError, *events.PairPasskeyError:
		// Pairing failed locally. Status flips back to "pairing" so
		// the operator can retry; the JS also re-fetches the latest
		// error message.
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "pairing",
		})
	case *events.PairSuccess:
		// PairSuccess carries the device identity (ID + LID) — surface
		// phone/jid/lid in the SSE payload so the JS doesn't need a
		// second round-trip to learn them.
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "connected",
			"phone":       e.ID.User,
			"jid":         e.ID.String(),
			"lid":         e.LID.String(),
		})
	case *events.Message:
		// No status change but last_seen ticks on every message.
		// Format as RFC3339 so the JS can parse + display it.
		write("status", map[string]any{
			"instance_id": instanceID,
			"status":      "connected",
			"last_seen":   time.Now().UTC().Format(time.RFC3339),
		})
	case *events.QR:
		// Pairing screen (US-041) listens for this and re-fetches
		// the latest PNG via /admin/api/instances/{id}/qr. The SSE
		// message carries just the id; the data URI is too large
		// to broadcast to every connected dashboard.
		write("qr_update", map[string]any{
			"instance_id": instanceID,
		})
	}
}

// formatSSE serializes a single SSE message. If data is nil, the
// message is emitted as a comment line (used for the heartbeat:
// `: ping\n\n`); the browser ignores comments but the connection
// stays warm.
func formatSSE(event string, data any) string {
	if data == nil {
		// Comment line. event is the comment body for debuggability.
		return ":" + event + "\n\n"
	}
	b, err := json.Marshal(data)
	if err != nil {
		// json.Marshal on a map[string]any of basic types can't fail;
		// if it ever does, surface as an error comment rather than
		// crashing the stream.
		return ": marshal-error: " + err.Error() + "\n\n"
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, b)
}
