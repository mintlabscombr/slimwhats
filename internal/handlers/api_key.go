// Package handlers — API key management (US-031).
//
// PUT    /admin/api/instances/{id}/api-key           — set a custom key
// POST   /admin/api/instances/{id}/api-key/rotate    — auto-generate a new key
// POST   /admin/api/instances/{id}/api-key/reveal    — return the plaintext key (requires manager_password in body)
//
// All three require a manager session (the adminAPI group enforces
// that). The reveal endpoint additionally requires the operator to
// re-submit APP_MANAGER_PASSWORD in the request body — the session
// cookie alone is NOT sufficient, because revealing a plaintext API
// key is a sensitive operation that warrants explicit re-auth.
package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/whatsmeow-api/internal/auth"
	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// APIKeyDeps groups the deps needed by the API-key handlers.
type APIKeyDeps struct {
	Store          *instance.Store
	ManagerPassword string // plaintext, for the reveal re-auth check
	Audit          AuditLogger
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

// RevealAPIKeyRequest is the JSON body for POST .../api-key/reveal.
type RevealAPIKeyRequest struct {
	ManagerPassword string `json:"manager_password" binding:"required"`
}

// RevealAPIKeyResponse is the JSON body for the reveal response.
type RevealAPIKeyResponse struct {
	ID     string `json:"id"`
	APIKey string `json:"api_key"`
}

// SetAPIKeyHandler — PUT /admin/api/instances/{id}/api-key.
// Operator-supplied key. Regex-validated against APIKeyRegex
// (^sk_live_[A-Za-z0-9]{16,128}$). Returns 200 + masked representation.
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
		if !instance.APIKeyRegex.MatchString(req.APIKey) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_api_key",
				"message": "api_key must match ^sk_live_[A-Za-z0-9]{16,128}$",
			})
			return
		}
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
		hash, err := instance.BcryptAPIKey(req.APIKey)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "bcrypt_failed",
				"message": err.Error(),
			})
			return
		}
		if err := deps.Store.SetAPIKey(id, hash); err != nil {
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
			"id":            id,
			"api_key_masked": "sk_live_••••••••" + req.APIKey[len(req.APIKey)-4:],
		})
	}
}

// RotateAPIKeyHandler — POST /admin/api/instances/{id}/api-key/rotate.
// Auto-generates a new key, bcrypt-hashes it, replaces the hash on
// the row, and returns the plaintext exactly once. Old key dies
// immediately (the new hash no longer matches it).
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
		hash, err := instance.BcryptAPIKey(plaintext)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "bcrypt_failed",
				"message": err.Error(),
			})
			return
		}
		if err := deps.Store.SetAPIKey(id, hash); err != nil {
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
// Returns the plaintext API key. The session cookie alone is NOT
// sufficient: the operator must also submit the APP_MANAGER_PASSWORD
// in the body. This is a deliberate second factor — if a session
// cookie is leaked, the attacker still can't extract API keys
// without also knowing the manager password.
//
// IMPORTANT: we cannot recover the plaintext from the bcrypt hash
// (that's the whole point of bcrypt). Instead, the create / rotate /
// set flows surface the plaintext. This endpoint works by re-checking
// the submitted password against the env-stored manager password and,
// if it matches, the operator is presumed to already know the API key
// (they have to have set it via the create / set / rotate flow). For
// v1 this is acceptable; v2 can swap to an encrypted-at-rest API-key
// column (mirroring the webhook secret pattern) to make reveal
// truly recoverable.
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
		// Audit the success — but NEVER log the plaintext key.
		if deps.Audit != nil {
			deps.Audit.Log(c.Request.Context(), "instance.api_key_revealed", id, currentUser(c), c.ClientIP(), c.GetHeader("User-Agent"), nil)
		}
		// We do NOT have the plaintext in the DB (bcrypt is one-way).
		// Return a clear message so the operator knows the v1 limitation.
		c.JSON(http.StatusOK, gin.H{
			"id":      id,
			"message": "v1 limitation: API keys are bcrypt-hashed and cannot be recovered. Use PUT .../api-key to set a new known key, or POST .../api-key/rotate to auto-generate a new one. The plaintext was shown exactly once at create / rotate / set time.",
		})
	}
}
