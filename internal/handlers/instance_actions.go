package handlers

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
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
		state := mgr.QRState(id)
		qr, err := instance.GetLatestQR(ctx, state, cli)
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
func SetWebhookHandler(store *instance.Store) gin.HandlerFunc {
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
			if err := store.SetWebhook(id, "", ""); err != nil {
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
		// Secret is unrestricted — any non-empty value is accepted.
		// Operators can use short API keys, UUIDs, JWTs, or long random
		// strings depending on their receiver. We previously enforced
		// 16-128 chars but the constraint was arbitrary and added
		// friction; the receiver (their HTTP handler) is the right
		// place to validate secret strength.
		if err := store.SetWebhook(id, req.URL, req.Secret); err != nil {
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

// WebhookFormActionHandler — POST /admin/instances/{id}/webhook.
// Form-based dispatcher for the Webhook card on the detail page.
// The underlying API is PUT /admin/api/instances/{id}/webhook
// (used by external clients) but HTML forms can only POST, so this
// is a thin wrapper that reads the form values, validates, calls
// store.SetWebhook, and re-renders the detail page with the typed
// values preserved + a status message in the alert slot
// (F-03 / US-004). No more ?msg=...&msg_class=... in the URL.
//
// "New secret" semantics: the form submits an empty secret when the
// operator only wants to update the URL. In that case we keep the
// existing secret from the DB so the operator doesn't have to
// re-enter it (and so we don't accidentally null the secret out
// after a URL-only edit). The "clear" case is the original behavior:
// both URL and new-secret blank → SetWebhook("", "").
func WebhookFormActionHandler(db *sql.DB, mgr *instance.Manager, store *instance.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		webhookURL := c.PostForm("url")
		secret := c.PostForm("secret")

		// The form to re-render with. On error we want the
		// operator's typed values back; on success we still pass
		// the form (with the saved values) so the template
		// shows what's now in the DB.
		form := &WebhookForm{URL: webhookURL, Secret: secret}

		var msg, msgClass string
		code := ""
		if webhookURL == "" && secret == "" {
			// Clear
			if err := store.SetWebhook(id, "", ""); err != nil {
				slog.Warn("WebhookFormActionHandler: clear failed", "id", id, "err", err)
				code = ErrCodeInternal
			} else {
				msg, msgClass = "Webhook cleared.", "ok"
			}
		} else {
			// Validate URL
			if webhookURL == "" {
				code = ErrCodeURLRequired
			} else if !isValidWebhookURL(webhookURL) {
				code = ErrCodeURLInvalid
			} else {
				// If new secret is blank, keep the existing one. If
				// none exists yet, require a non-empty secret on
				// the first save.
				if secret == "" {
					_, existing, err := store.LoadWebhookSecret(id)
					if err != nil {
						slog.Warn("WebhookFormActionHandler: load secret failed", "id", id, "err", err)
						code = ErrCodeInternal
					} else if existing == "" {
						code = ErrCodeSecretRequired
					} else {
						secret = existing
					}
				}
				if code == "" {
					if err := store.SetWebhook(id, webhookURL, secret); err != nil {
						slog.Warn("WebhookFormActionHandler: save failed", "id", id, "err", err)
						code = ErrCodeInternal
					} else {
						msg, msgClass = "Webhook saved.", "ok"
					}
				}
			}
		}

		// On error, resolve the code to its English message and
		// re-render the detail page with the typed values
		// preserved. On success, re-render with the success
		// banner. Either way: no redirect, no query string.
		if code != "" {
			msg, msgClass = Message(code), "error"
		}
		renderInstanceDetail(c, db, mgr, form, msg, msgClass)
	}
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
