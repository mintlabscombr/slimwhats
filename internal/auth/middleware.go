package auth

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// SessionMiddleware enforces a valid session cookie for `/admin/*` and
// `/admin/api/*` routes. Behavior:
//   - `/admin/api/*`   → returns 401 JSON when unauthenticated
//   - `/admin/*` (UI)  → redirects to /admin/login when unauthenticated
func SessionMiddleware(store *SessionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Cookie(store.CookieName)
		if err != nil || cookie == "" {
			respondUnauth(c)
			return
		}
		sess, err := store.Validate(c.Request.Context(), cookie, true)
		if err != nil {
			respondUnauth(c)
			return
		}
		c.Set("session", sess)
		c.Set("username", sess.Username)
		c.Next()
	}
}

func respondUnauth(c *gin.Context) {
	if isAPIRequest(c) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
			"error":   "unauthorized",
			"message": "missing or invalid session",
		})
		return
	}
	c.AbortWithStatus(http.StatusFound) // 302
	c.Header("Location", "/admin/login")
}

func isAPIRequest(c *gin.Context) bool {
	// Convention: routes under /admin/api/ are JSON; everything else
	// under /admin/ is HTML.
	return len(c.Request.URL.Path) >= len("/admin/api/") &&
		c.Request.URL.Path[:len("/admin/api/")] == "/admin/api/"
}
