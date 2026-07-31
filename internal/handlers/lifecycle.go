// Package handlers — lifecycle endpoints (US-025..US-028).
//
// POST   /admin/api/instances/{id}/connect
// POST   /admin/api/instances/{id}/disconnect
// POST   /admin/api/instances/{id}/reconnect
// DELETE /admin/api/instances/{id}
//
// All four require an authenticated manager session (the manager
// middleware is applied in main.go). The DELETE handler is the only
// destructive one — it removes the in-memory client, the in-memory
// API-key lookup cache, and the DB row. The whatsmeow device record
// in the same DB is intentionally kept (see Store.Delete for why).
package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/slimwhats/internal/instance"
)

// LifecycleDeps groups the deps needed by the lifecycle handlers.
type LifecycleDeps struct {
	DB      *sql.DB
	Store   *instance.Store
	Manager *instance.Manager
	Audit   AuditLogger // nil-safe: nil means audit logging is off
}

// AuditLogger is the minimal contract the lifecycle handlers use to
// record state-changing actions. The concrete implementation lives in
// audit.go (*AuditLoggerImpl). Kept as an interface so lifecycle
// handlers don't import the concrete type.
type AuditLogger interface {
	Log(ctx context.Context, action, targetID, username, sourceIP, userAgent string, data map[string]any)
}

// ConnectInstanceHandler — POST /admin/api/instances/{id}/connect.
// If the instance is not yet paired, returns 409. If already
// connected, returns 200 with no-op (and syncs the DB status if it
// drifted from reality). Otherwise calls Manager.Start in a goroutine
// and returns 202.
func ConnectInstanceHandler(deps LifecycleDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		inst, _, err := deps.Manager.LookupByID(ctx, id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "lookup_failed",
				"message": err.Error(),
			})
			return
		}
		if inst == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "not_found",
				"message": "no such instance",
			})
			return
		}
		cli := deps.Manager.Get(id)
		// already_connected: websocket is open. Sync the DB status to
		// reality in case it drifted (the event subscriber can miss
		// transitions during a network blip + auto-reconnect, leaving
		// the row at "disconnected" while the client is actually up).
		if cli != nil && cli.IsConnected() {
			syncStatusToClientState(deps.Store, id, cli)
			c.JSON(http.StatusOK, gin.H{
				"id":     id,
				"status": "already_connected",
			})
			return
		}
		// not_paired: client exists but never scanned a QR, OR the
		// client is not loaded at all. Either way, the operator needs
		// to scan the QR on the detail page — there is no "connect"
		// step for unpaired devices. The detail page renders the QR
		// automatically when !cli.IsLoggedIn().
		if cli == nil || !cli.IsLoggedIn() {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "not_paired",
				"message": "instance is not paired; open the detail page and scan the QR code",
			})
			return
		}
		// Reset the expected-disconnect flag (in case it was set by a
		// previous operator-driven disconnect) and trigger Connect().
		if err := deps.Manager.Start(ctx, id); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "start_failed",
				"message": err.Error(),
			})
			return
		}
		if deps.Audit != nil {
			deps.Audit.Log(ctx, "instance.connect", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusAccepted, gin.H{
			"id":     id,
			"status": "connecting",
		})
	}
}

// syncStatusToClientState writes the instance's DB status to match
// what the whatsmeow client is actually doing. This is the "drift
// correction" path — the event subscriber sometimes misses
// transitions (especially during a network blip + auto-reconnect),
// leaving the row in an inconsistent state. We only correct the row
// when we have a positive signal from the live client.
func syncStatusToClientState(store *instance.Store, id string, cli interface{ IsLoggedIn() bool }) {
	now := time.Now().UTC()
	if cli.IsLoggedIn() {
		// Connected AND logged in → real "connected" state.
		_ = store.SetStatus(id, instance.StatusConnected, &now, &now)
	} else {
		// Connected but not logged in → the device is in the middle
		// of pairing (a QR scan is pending). "pairing" is the
		// accurate status for the UI.
		_ = store.SetStatus(id, instance.StatusPairing, nil, nil)
	}
}

// DisconnectInstanceHandler — POST /admin/api/instances/{id}/disconnect.
// Sets the expected-disconnect flag and calls client.Disconnect().
func DisconnectInstanceHandler(deps LifecycleDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		if err := deps.Manager.Disconnect(id); err != nil {
			if errors.Is(err, instance.ErrNotLoaded) {
				c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
					"error":   "not_loaded",
					"message": "instance is not loaded; nothing to disconnect",
				})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "disconnect_failed",
				"message": err.Error(),
			})
			return
		}
		// Mark the DB row as disconnected immediately (the event
		// subscriber will also write the same status; this is the
		// operator's explicit request, so write it now for
		// observability).
		status := instance.StatusDisconnected
		_ = deps.Store.SetStatus(id, status, nil, nil)
		if deps.Audit != nil {
			deps.Audit.Log(ctx, "instance.disconnect", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusOK, gin.H{
			"id":     id,
			"status": "disconnected",
		})
	}
}

// ReconnectInstanceHandler — POST /admin/api/instances/{id}/reconnect.
// disconnect + start with a small delay.
func ReconnectInstanceHandler(deps LifecycleDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		inst, _, err := deps.Manager.LookupByID(ctx, id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "lookup_failed",
				"message": err.Error(),
			})
			return
		}
		if inst == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "not_found",
				"message": "no such instance",
			})
			return
		}
		if err := deps.Manager.Reconnect(ctx, id); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "reconnect_failed",
				"message": err.Error(),
			})
			return
		}
		if deps.Audit != nil {
			deps.Audit.Log(ctx, "instance.reconnect", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusAccepted, gin.H{
			"id":     id,
			"status": "reconnecting",
		})
	}
}

// DeleteInstanceHandler — DELETE /admin/api/instances/{id}.
// Evicts from the in-memory manager, removes the DB row. CASCADE
// cleans up webhook_deliveries and instance_logs for that instance.
func DeleteInstanceHandler(deps LifecycleDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		// Confirm existence first so 404 is distinguishable from
		// "deleted successfully".
		inst, _, err := deps.Manager.LookupByID(ctx, id)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "lookup_failed",
				"message": err.Error(),
			})
			return
		}
		if inst == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "not_found",
				"message": "no such instance",
			})
			return
		}
		// Evict from the in-memory map (this also calls Disconnect).
		deps.Manager.Remove(id)
		// Best-effort: give whatsmeow a moment to release the socket.
		time.Sleep(100 * time.Millisecond)
		// Delete the row.
		if err := deps.Store.Delete(id); err != nil {
			slog.Error("delete instance failed", "id", id, "err", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "delete_failed",
				"message": err.Error(),
			})
			return
		}
		if deps.Audit != nil {
			deps.Audit.Log(ctx, "instance.delete", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusOK, gin.H{
			"id":     id,
			"status": "deleted",
		})
	}
}

// currentUser is a tiny helper to pull the username from the session
// middleware's context key. Returns "" if no session is present.
func currentUser(c *gin.Context) string {
	if v, ok := c.Get("username"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// LifecycleActionHandler — POST /admin/instances/{id}.
//
// Form-based dispatcher for the lifecycle buttons in the detail
// page (Connect / Disconnect / Reconnect). Reads the `action` form
// value and routes to the matching Manager method, then re-renders
// the detail page (F-03 / US-006) with a status message in the
// alert slot. No redirect, no query string — the URL stays
// /admin/instances/{id}. Re-render is preferred over redirect so
// the page state (status badge, QR, etc.) updates in place; the
// "are you sure you want to resubmit the form?" prompt on browser
// refresh is handled by the `data-alert-slot` div being outside
// the form (the GET re-render doesn't re-fire the POST).
func LifecycleActionHandler(deps LifecycleDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		ctx := c.Request.Context()
		action := c.PostForm("action")
		username := currentUser(c)

		// msg/msgClass are the (English) text for the alert slot;
		// they default to the resolved `Message(code)` on error
		// paths and a fixed string on success paths. The codes
		// themselves are short snake_case (errors.go) but we
		// resolve here so the template's `{{.ActionResult}}`
		// doesn't need to call a funcMap.
		var msg, msgClass string
		switch action {
		case "connect":
			// Confirm the instance exists + is paired.
			inst, _, err := deps.Manager.LookupByID(ctx, id)
			if err != nil {
				slog.Warn("LifecycleActionHandler: lookup failed", "id", id, "err", err)
				msg, msgClass = Message(ErrCodeLookupFailed), "error"
				break
			}
			if inst == nil {
				msg, msgClass = Message(ErrCodeNotFound), "error"
				break
			}
			cli := deps.Manager.Get(id)
			if cli != nil && cli.IsConnected() {
				// Sync DB status to reality (drift correction) and
				// give the operator a useful message depending on
				// whether the client is also logged in. "Already
				// connected" was misleading when the DB said
				// "disconnected" — now we say exactly what the
				// client is doing.
				syncStatusToClientState(deps.Store, id, cli)
				if cli.IsLoggedIn() {
					msg = "Already connected."
					msgClass = "ok"
				} else {
					msg = "Already connected (waiting for QR scan — see below)."
					msgClass = "ok"
				}
				break
			}
			if cli == nil || !cli.IsLoggedIn() {
				// Unpaired device. The detail page renders a fresh
				// QR when it loads. We kick the connect in a goroutine
				// so the QR is in flight by the time the re-render
				// lands (saves a few hundred ms of waiting). The
				// user-facing message is "loading QR" rather than
				// "not paired" because the latter sounds like a
				// failure when really the system is just generating
				// a pairing code.
				if cli == nil {
					cli = deps.Manager.Get(id)
				}
				if cli != nil {
					go func() {
						if err := cli.Connect(); err != nil {
							_ = err
						}
					}()
				}
				msg = "Loading fresh QR — page will show it momentarily."
				msgClass = "ok"
				break
			}
			if err := deps.Manager.Start(ctx, id); err != nil {
				slog.Warn("LifecycleActionHandler: connect failed", "id", id, "err", err)
				msg, msgClass = Message(ErrCodeConnectFailed), "error"
				break
			}
			if deps.Audit != nil {
				deps.Audit.Log(ctx, "instance.connect", id, username, c.ClientIP(), c.GetHeader("User-Agent"), nil)
			}
			msg, msgClass = "Connecting.", "ok"

		case "disconnect":
			if err := deps.Manager.Disconnect(id); err != nil {
				slog.Warn("LifecycleActionHandler: disconnect failed", "id", id, "err", err)
				msg, msgClass = Message(ErrCodeDisconnectFailed), "error"
				break
			}
			_ = deps.Store.SetStatus(id, instance.StatusDisconnected, nil, nil)
			if deps.Audit != nil {
				deps.Audit.Log(ctx, "instance.disconnect", id, username, c.ClientIP(), c.GetHeader("User-Agent"), nil)
			}
			msg, msgClass = "Disconnected.", "ok"

		case "reconnect":
			inst, _, err := deps.Manager.LookupByID(ctx, id)
			if err != nil {
				slog.Warn("LifecycleActionHandler: lookup failed", "id", id, "err", err)
				msg, msgClass = Message(ErrCodeLookupFailed), "error"
				break
			}
			if inst == nil {
				msg, msgClass = Message(ErrCodeNotFound), "error"
				break
			}
			if err := deps.Manager.Reconnect(ctx, id); err != nil {
				slog.Warn("LifecycleActionHandler: reconnect failed", "id", id, "err", err)
				msg, msgClass = Message(ErrCodeReconnectFailed), "error"
				break
			}
			if deps.Audit != nil {
				deps.Audit.Log(ctx, "instance.reconnect", id, username, c.ClientIP(), c.GetHeader("User-Agent"), nil)
			}
			msg, msgClass = "Reconnecting.", "ok"

		default:
			msg, msgClass = "Unknown action: "+action, "error"
		}

		// F-03 / US-006: re-render the detail page in place. No
		// ?msg= / ?msg_class= in the URL.
		renderInstanceDetail(c, deps.DB, deps.Manager, &WebhookForm{}, msg, msgClass)
	}
}
