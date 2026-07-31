// Package handlers wires the HTTP handlers for the admin API and manager UI.
package handlers

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/slimwhats/internal/auth"
)

// AdminAuthDeps groups the dependencies needed by the admin auth handlers.
type AdminAuthDeps struct {
	DB              *sql.DB
	Sessions        *auth.SessionStore
	Limiter         *auth.LoginRateLimiter
	ManagerPassword string
	ManagerUsername string
	SecureCookie    bool // true in production (HTTPS), false in dev (HTTP)
}

// LoginRequest is the body for POST /admin/login. Tagged for both
// `json` and `form` so gin's ShouldBind picks up either a JSON
// request body or an application/x-www-form-urlencoded browser
// form submission.
type LoginRequest struct {
	Password string `json:"password" form:"password" binding:"required"`
}

// LoginResponse is the JSON body returned on successful login.
type LoginResponse struct {
	Username  string `json:"username"`
	ExpiresAt string `json:"expires_at"`
}

// LoginHandler validates the manager password and mints a session cookie.
// Rate limit: 5 failed attempts per source IP per 10 minutes → 429 with
// Retry-After.
//
// Error responses branch on Accept: a browser form submit (text/html)
// gets a 302 to /admin/login?error=... so the login page can render
// the message inline; an API client (application/json) gets a
// structured JSON error. Pre-F-02 the handler always returned JSON,
// which rendered as a raw {"error":"..."} blob in the browser — looked
// like the login was broken to anyone who fat-fingered a password
// or got rate-limited.
func LoginHandler(d AdminAuthDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		browser := wantsHTML(c.GetHeader("Accept"))

		// Lockout check
		if remaining := d.Limiter.Check(ip); remaining > 0 {
			c.Header("Retry-After", formatSeconds(remaining))
			if browser {
				c.Redirect(http.StatusFound,
					"/admin/login?error=rate_limited&retry_after="+formatSeconds(remaining))
				return
			}
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":   "rate_limited",
				"message": "too many failed login attempts",
			})
			return
		}

		// Accept both JSON and form-encoded
		var req LoginRequest
		if err := c.ShouldBind(&req); err != nil {
			d.recordFailure(c, ip)
			if browser {
				c.Redirect(http.StatusFound, "/admin/login?error=invalid_request")
				return
			}
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"error":   "invalid_request",
				"message": "password field is required",
			})
			return
		}

		if !auth.CompareConstantTime(req.Password, d.ManagerPassword) {
			d.recordFailure(c, ip)
			if browser {
				c.Redirect(http.StatusFound, "/admin/login?error=invalid_credentials")
				return
			}
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":   "invalid_credentials",
				"message": "invalid username or password",
			})
			return
		}

		d.Limiter.RecordSuccess(ip)
		sess, err := d.Sessions.Create(c.Request.Context(), d.ManagerUsername)
		if err != nil {
			if browser {
				c.Redirect(http.StatusFound, "/admin/login?error=session_create_failed")
				return
			}
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"error":   "session_create_failed",
				"message": "could not create session",
			})
			return
		}
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(
			d.Sessions.CookieName,
			sess.ID,
			int(time.Until(sess.ExpiresAt).Seconds()),
			"/", // root path so the cookie covers both /admin/* and /swagger (US-037)
			"",
			d.SecureCookie, // Secure flag — true in prod
			true,           // HttpOnly
		)
		// Browser form submit (Accept: text/html) → 302 to /admin/ so
		// the user lands on the home page. JSON client → keep the
		// structured LoginResponse so API consumers can parse it.
		if browser {
			c.Redirect(http.StatusFound, "/admin/")
			return
		}
		c.JSON(http.StatusOK, LoginResponse{
			Username:  sess.Username,
			ExpiresAt: sess.ExpiresAt.UTC().Format(time.RFC3339),
		})
	}
}

func (d AdminAuthDeps) recordFailure(c *gin.Context, ip string) {
	if remaining := d.Limiter.RecordFailure(ip); remaining > 0 {
		// already over the limit; the next call will see the lockout via Check
		_ = remaining
	}
}

// LogoutHandler invalidates the session and clears the cookie.
func LogoutHandler(d AdminAuthDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie(d.Sessions.CookieName)
		if err == nil && cookie != "" {
			_ = d.Sessions.Delete(c.Request.Context(), cookie)
		}
		c.SetCookie(d.Sessions.CookieName, "", -1, "/", "", d.SecureCookie, true)
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

func formatSeconds(d time.Duration) string {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return itoa(secs)
}

func itoa(n int) string {
	// small helper so we don't import strconv just for one int
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// csrfToken is a placeholder for the double-submit CSRF token used by
// the manager UI forms. Real implementation lives in the templates package.
func csrfToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
