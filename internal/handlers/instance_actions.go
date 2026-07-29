package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.mau.fi/whatsmeow"

	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// InstanceQRHandler handles GET /admin/api/instances/{id}/qr. Returns
// the current QR code payload as base64. Triggers Connect() lazily.
func InstanceQRHandler(mgr *instance.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		cli := mgr.Get(id)
		if cli == nil {
			c.AbortWithStatusJSON(http.StatusNotFound, gin.H{
				"error":   "not_found",
				"message": "no such instance",
			})
			return
		}
		if cli.IsLoggedIn() {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "already_paired",
				"message": "instance is already paired; no QR needed",
			})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 65*time.Second)
		defer cancel()
		qr, err := instance.GetLatestQR(ctx, cli)
		if err != nil {
			if errors.Is(err, instance.ErrAlreadyPaired) {
				c.AbortWithStatusJSON(http.StatusConflict, gin.H{
					"error": "already_paired",
				})
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "qr_failed",
				"message": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, instance.QRCode{
			QR:       qr,
			Format:   "raw",
			IssuedAt: time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// InstanceStatusHandler handles GET /admin/api/instances/{id}/status.
// Returns a status-only view (subset of the detail endpoint).
func InstanceStatusHandler(mgr *instance.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		inst, cli, err := mgr.LookupByID(c.Request.Context(), id)
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
		connected := false
		if cli != nil {
			connected = cli.IsConnected()
		}
		c.JSON(http.StatusOK, gin.H{
			"id":        inst.ID,
			"name":      inst.Name,
			"status":    inst.Status,
			"connected": connected,
			"logged_in": cli != nil && cli.IsLoggedIn(),
			"phone":     inst.Phone.String,
			"jid":       inst.JID.String,
			"lid":       inst.LID.String,
		})
	}
}

// SetWebhookRequest is the body for PUT /admin/api/instances/{id}/webhook.
type SetWebhookRequest struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

// SetWebhookHandler handles PUT /admin/api/instances/{id}/webhook. Empty
// url+secret clears the config.
func SetWebhookHandler(store *instance.Store, encryptionKey []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req SetWebhookRequest
		if err := c.ShouldBind(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": err.Error(),
			})
			return
		}
		if req.URL == "" && req.Secret == "" {
			// Clear
			if err := store.SetWebhook(id, "", "", encryptionKey); err != nil {
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error":   "set_webhook_failed",
					"message": err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{"status": "cleared"})
			return
		}
		// Validate URL
		if !isValidWebhookURL(req.URL) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_url",
				"message": "url must start with https:// (or http://localhost for dev)",
			})
			return
		}
		// Validate secret length
		if l := len(req.Secret); l < 16 || l > 128 {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_secret",
				"message": "secret must be 16-128 chars",
			})
			return
		}
		if err := store.SetWebhook(id, req.URL, req.Secret, encryptionKey); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "set_webhook_failed",
				"message": err.Error(),
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"id":     id,
			"url":    req.URL,
			"status": "configured",
		})
	}
}

func isValidWebhookURL(u string) bool {
	if len(u) > 2048 {
		return false
	}
	// https://, or http://localhost / http://127.0.0.1 for dev
	if startsWith(u, "https://") {
		return true
	}
	if startsWith(u, "http://localhost") || startsWith(u, "http://127.0.0.1") {
		return true
	}
	return false
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// InstanceAPIKeyAuth is a Gin middleware that authenticates requests
// via Authorization: Bearer sk_live_... and looks up the instance via
// the manager.
func InstanceAPIKeyAuth(mgr *instance.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if len(header) < 8 || header[:7] != "Bearer " {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_api_key",
				"message": "Authorization: Bearer sk_live_... required",
			})
			return
		}
		plaintext := header[7:]
		inst, cli, err := mgr.GetByAPIKey(c.Request.Context(), plaintext)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "lookup_failed",
				"message": err.Error(),
			})
			return
		}
		if inst == nil || cli == nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_api_key",
				"message": "no instance matches this api key (or the instance isn't loaded)",
			})
			return
		}
		c.Set("instance", inst)
		c.Set("client", cli)
		c.Next()
	}
}

// PingResponse is the shape returned by /api/v1/ping (sanity check
// that the bearer-auth middleware is wired correctly).
type PingResponse struct {
	InstanceID   string `json:"instance_id"`
	InstanceName string `json:"instance_name"`
	Connected    bool   `json:"connected"`
}

// PingHandler responds with the authenticated instance identity.
func PingHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		v, _ := c.Get("instance")
		inst, _ := v.(*instance.Instance)
		cliV, _ := c.Get("client")
		cli, _ := cliV.(*whatsmeow.Client)
		connected := cli != nil && cli.IsConnected()
		name := ""
		id := ""
		if inst != nil {
			name = inst.Name
			id = inst.ID
		}
		c.JSON(http.StatusOK, PingResponse{
			InstanceID:   id,
			InstanceName: name,
			Connected:    connected,
		})
	}
}

// SQLNullToString returns the string value of a sql.NullString, or "" if null.
func SQLNullToString(ns sql.NullString) string {
	if ns.Valid {
		return ns.String
	}
	return ""
}
