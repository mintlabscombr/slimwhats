// Package handlers — API key management (US-031).
//
// PUT    /admin/api/instances/{id}/api-key           — set a custom key
// POST   /admin/api/instances/{id}/api-key/rotate    — auto-generate a new key
// POST   /admin/api/instances/{id}/api-key/reveal    — return the plaintext key
//
// All three require a manager session (the adminAPI group enforces
// that). The reveal endpoint additionally requires the operator to
// re-submit APP_MANAGER_PASSWORD in the request body — the session
// cookie alone is NOT sufficient, because revealing an API key is a
// sensitive operation that warrants explicit re-auth.
//
// Post 2026-07-29 (drop-bcrypt), the API key is stored in plaintext.
// Reveal now actually returns the stored value (no more v1 limitation
// message about bcrypt). The trade-off: anyone with read access to
// the DB sees all API keys. This matches the decision we already
// made for webhook secrets in 1e41637. Documented in the README.
package handlers

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/whatsmeow-api/internal/auth"
	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// APIKeyDeps groups the deps needed by the API-key handlers.
type APIKeyDeps struct {
	Store           *instance.Store
	Manager         *instance.Manager
	ManagerPassword string // plaintext, for the reveal re-auth check
	Audit           AuditLogger
}

// SetAPIKeyRequest is the JSON body for PUT .../api-key.
type SetAPIKeyRequest struct {
	APIKey string `json:"api_key" binding:"required"`
}

// RotateAPIKeyResponse is the JSON body for POST .../api-key/rotate.
type RotateAPIKeyResponse struct {
	ID     string `json:"id"`
	APIKey string `json:"api_key"` // plaintext, returned once
}

// RevealAPIKeyRequest is the body for POST .../api-key/reveal. Used
// by BOTH the JSON API and the HTML form-dispatcher on the manager
// detail page — so the struct needs both `json:` and `form:` tags.
// The `form:` tag is what gin's ShouldBind looks at when the request
// is Content-Type: application/x-www-form-urlencoded; without it the
// field is silently missing and `binding:"required"` fails.
type RevealAPIKeyRequest struct {
	ManagerPassword string `json:"manager_password" form:"manager_password" binding:"required"`
}

// RevealAPIKeyResponse is the JSON body for the reveal response.
type RevealAPIKeyResponse struct {
	ID     string `json:"id"`
	APIKey string `json:"api_key"`
}

// SetAPIKeyHandler — PUT /admin/api/instances/{id}/api-key.
// Operator-supplied key. F-03 / US-002 dropped the regex check:
// any non-empty string is now accepted. Returns 200 + masked
// representation. The plaintext is stored as-is in the api_key column.
func SetAPIKeyHandler(deps APIKeyDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req SetAPIKeyRequest
		if err := c.ShouldBind(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": "api_key is required",
			})
			return
		}
		// F-03 / US-002: no more format check. The operator can
		// use any opaque string — UUID, base64, custom prefix,
		// whatever their client code expects. The auto-generated
		// key from POST /rotate is the only path that still emits
		// the `sk_live_` prefix; user-supplied keys are unrestricted.
		// Confirm the instance exists first.
		inst, err := deps.Store.GetByID(id)
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
		if err := deps.Store.SetAPIKey(id, req.APIKey); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "set_api_key_failed",
				"message": err.Error(),
			})
			return
		}
		if deps.Audit != nil {
			deps.Audit.Log(c.Request.Context(), "instance.set_api_key", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusOK, gin.H{
			"id":             id,
			"api_key_masked": "sk_live_••••••••" + req.APIKey[len(req.APIKey)-4:],
		})
	}
}

// RotateAPIKeyHandler — POST /admin/api/instances/{id}/api-key/rotate.
// Auto-generates a new key, stores the plaintext in the row, and
// returns the plaintext exactly once. Old key dies immediately (the
// new value no longer matches it).
func RotateAPIKeyHandler(deps APIKeyDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		inst, err := deps.Store.GetByID(id)
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
		plaintext, err := instance.GenerateAPIKey()
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "generate_failed",
				"message": err.Error(),
			})
			return
		}
		if err := deps.Store.SetAPIKey(id, plaintext); err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "rotate_failed",
				"message": err.Error(),
			})
			return
		}
		if deps.Audit != nil {
			deps.Audit.Log(c.Request.Context(), "instance.rotate_api_key", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusOK, RotateAPIKeyResponse{
			ID:     id,
			APIKey: plaintext,
		})
	}
}

// RevealAPIKeyHandler — POST /admin/api/instances/{id}/api-key/reveal.
// Returns the plaintext API key from the DB. The session cookie alone
// is NOT sufficient: the operator must also submit the
// APP_MANAGER_PASSWORD in the body. This is a deliberate second
// factor — if a session cookie is leaked, the attacker still can't
// extract API keys without also knowing the manager password.
//
// Post 2026-07-29 (drop-bcrypt) the reveal endpoint actually
// recovers the plaintext. The audit log records the action but NEVER
// the plaintext value.
func RevealAPIKeyHandler(deps APIKeyDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		var req RevealAPIKeyRequest
		if err := c.ShouldBind(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": "manager_password is required",
			})
			return
		}
		if !auth.CompareConstantTime(req.ManagerPassword, deps.ManagerPassword) {
			// Audit even the failures (no plaintext anywhere — just
			// the fact that someone tried).
			if deps.Audit != nil {
				deps.Audit.Log(c.Request.Context(), "instance.api_key_reveal_failed", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_manager_password",
				"message": "manager_password mismatch",
			})
			return
		}
		// Confirm the instance exists.
		inst, err := deps.Store.GetByID(id)
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
		if inst.APIKey == "" {
			c.AbortWithStatusJSON(http.StatusConflict, gin.H{
				"error":   "no_api_key",
				"message": "this instance has no API key yet (just migrated); call POST .../api-key/rotate to set one",
			})
			return
		}
		// Audit the success — but NEVER log the plaintext key.
		if deps.Audit != nil {
			deps.Audit.Log(c.Request.Context(), "instance.api_key_revealed", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		c.JSON(http.StatusOK, RevealAPIKeyResponse{
			ID:     id,
			APIKey: inst.APIKey,
		})
	}
}

// APIKeyFormActionHandler — form-based dispatcher for the api-key
// actions on the manager detail page. The HTML forms submit to
// /admin/instances/{id}/{action} (no /api/ segment, no DELETE
// verb — HTML forms only support GET/POST), so we need a small
// shim that reads the action from the URL and routes to the
// matching logic. Then it redirects back to the detail page
// (or the list page, for delete) with a status message in the
// query string.
//
// Routes handled (all POST):
//
//	POST /admin/instances/{id}/api-key/rotate → generate a new key
//	POST /admin/instances/{id}/delete         → delete the instance
//
// (The old `reveal-key` action used to live here too — removed when
// the manager-password / Re-fetch-from-DB form on the detail page
// was dropped. Programmatic reveals go through the JSON endpoint
// RevealAPIKeyHandler at POST /admin/api/instances/{id}/api-key/reveal,
// which still requires the manager password.)
func APIKeyFormActionHandler(deps APIKeyDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		parts := splitPath(path)
		if len(parts) < 4 {
			redirectWithMsg(c, path, "error", "Malformed action URL.")
			return
		}
		id := parts[2]
		// The action is the last path segment. URLs look like:
		//   /admin/instances/{id}/api-key/rotate → action="rotate"
		//   /admin/instances/{id}/delete         → action="delete"
		action := parts[len(parts)-1]
		username := currentUser(c)

		switch action {
		case "rotate":
			inst, err := deps.Store.GetByID(id)
			if err != nil {
				redirectWithMsg(c, path, "error", "Lookup failed: "+err.Error())
				return
			}
			if inst == nil {
				redirectWithMsg(c, path, "error", "Instance not found.")
				return
			}
			plaintext, err := instance.GenerateAPIKey()
			if err != nil {
				redirectWithMsg(c, path, "error", "Generate failed: "+err.Error())
				return
			}
			if err := deps.Store.SetAPIKey(id, plaintext); err != nil {
				redirectWithMsg(c, path, "error", "Rotate failed: "+err.Error())
				return
			}
			if deps.Audit != nil {
				deps.Audit.Log(c.Request.Context(), "instance.rotate_api_key", id, username, c.ClientIP(), c.GetHeader("User-Agent"), nil)
			}
			// F-03 / US-007: drop the `?new_api_key=...` query param.
			// The new key is in the show/hide field on the detail
			// page; the operator reads it from there.
			//
			// F-03 / US-005 will convert this whole handler to a
			// re-render of the detail page (no redirect) and switch
			// the success/error banner to an error-code lookup. For
			// US-007 we just kill the new-key banner; the redirect-
			// with-msg pattern stays.
			_ = plaintext
			c.Redirect(http.StatusFound, "/admin/instances/"+id+"?msg=API+key+rotated.&msg_class=ok")

		case "delete":
			deps.Manager.Remove(id)
			time.Sleep(100 * time.Millisecond) // let whatsmeow release the socket
			if err := deps.Store.Delete(id); err != nil {
				slog.Error("delete instance failed", "id", id, "err", err)
				redirectWithMsg(c, path, "error", "Delete failed: "+err.Error())
				return
			}
			if deps.Audit != nil {
				deps.Audit.Log(c.Request.Context(), "instance.delete", id, username, c.ClientIP(), c.GetHeader("User-Agent"), nil)
			}
			// Delete is destructive — bounce to the list page.
			c.Redirect(http.StatusFound, "/admin/?msg=Instance+deleted.&msg_class=ok")

		default:
			redirectWithMsg(c, path, "error", "Unknown action: "+action)
		}
	}
}

// redirectWithMsg sends a 302 to the detail page (or the list page
// for the delete action) with a status message in the query string.
// The detail template renders the message via .ActionResult /
// .ActionResultClass.
func redirectWithMsg(c *gin.Context, fromPath, msgClass, msg string) {
	parts := splitPath(fromPath)
	target := "/admin/"
	if len(parts) >= 3 && (msgClass == "ok" || msgClass == "error") {
		// Default to the detail page for most actions.
		// (The delete handler overrides this directly.)
		target = "/admin/instances/" + parts[2]
	}
	c.Redirect(http.StatusFound, target+"?msg="+url.QueryEscape(msg)+"&msg_class="+msgClass)
}

// splitPath returns the non-empty path segments of p.
//
//	splitPath("/admin/instances/abc/rotate") = ["admin", "instances", "abc", "rotate"]
func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if i > start {
				out = append(out, p[start:i])
			}
			start = i + 1
		}
	}
	if start < len(p) {
		out = append(out, p[start:])
	}
	return out
}
