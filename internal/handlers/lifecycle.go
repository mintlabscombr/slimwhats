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

	"github.com/mauroneto/whatsmeow-api/internal/instance"
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
// connected, returns 200 with no-op. Otherwise calls Manager.Start
// in a goroutine and returns 202.
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
		if cli != nil && cli.IsConnected() {
			c.JSON(http.StatusOK, gin.H{
				"id":     id,
				"status": "already_connected",
			})
			return
		}
		if cli == nil || !cli.IsLoggedIn() {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "not_paired",
				"message": "instance is not paired; call GET /qr first",
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
