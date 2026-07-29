package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// loginTmpl is the manager login page. Minimal CSS inlined, single
// password field, static "admin" username label.
var loginTmpl = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>whatsmeow-api — Manager login</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #f5f5f5; margin: 0; padding: 2rem; }
  .card { max-width: 360px; margin: 4rem auto; background: #fff; padding: 2rem; border-radius: 8px; box-shadow: 0 2px 8px rgba(0,0,0,.08); }
  h1 { margin: 0 0 .5rem; font-size: 1.25rem; }
  p.sub { margin: 0 0 1.5rem; color: #666; font-size: .9rem; }
  label { display: block; font-size: .85rem; color: #444; margin: 1rem 0 .25rem; }
  input[type=text], input[type=password] { width: 100%; padding: .6rem .7rem; border: 1px solid #ccc; border-radius: 4px; box-sizing: border-box; font-size: 1rem; }
  input[readonly] { background: #f5f5f5; color: #555; }
  button { width: 100%; margin-top: 1.5rem; padding: .7rem; background: #1a7f37; color: #fff; border: 0; border-radius: 4px; font-size: 1rem; cursor: pointer; }
  button:hover { background: #166a2e; }
  .error { background: #fee; border: 1px solid #fcc; color: #a00; padding: .75rem; border-radius: 4px; margin-bottom: 1rem; font-size: .9rem; }
  .hint { margin-top: 1.5rem; font-size: .75rem; color: #888; text-align: center; }
</style>
</head>
<body>
<form class="card" method="POST" action="/admin/login">
  <h1>whatsmeow-api</h1>
  <p class="sub">Manager panel</p>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  <label for="username">Username</label>
  <input id="username" type="text" name="username" value="{{.Username}}" readonly>
  <label for="password">Password</label>
  <input id="password" type="password" name="password" autofocus required>
  <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
  <button type="submit">Sign in</button>
  <p class="hint">whatsmeow-api v0.1</p>
</form>
</body>
</html>
`))

// LoginPageData is the template context for the login page.
type LoginPageData struct {
	Username  string
	Error     string
	CSRFToken string
}

// LoginPageHandler serves GET /admin/login. When the request is JSON
// (e.g. from an API client), returns the LoginRequest shape; otherwise
// renders the HTML form.
func LoginPageHandler(managerUsername string) gin.HandlerFunc {
	return func(c *gin.Context) {
		accept := c.GetHeader("Accept")
		if !wantsHTML(accept) {
			// JSON client — return the schema, not the HTML page
			c.JSON(http.StatusOK, gin.H{
				"endpoint": "POST /admin/login",
				"body":     gin.H{"password": "string"},
			})
			return
		}
		errMsg := c.Query("error")
		if errMsg == "invalid_credentials" {
			errMsg = "Invalid username or password."
		} else if errMsg == "rate_limited" {
			errMsg = "Too many failed attempts. Try again in 15 minutes."
		}
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.Status(http.StatusOK)
		_ = loginTmpl.Execute(c.Writer, LoginPageData{
			Username:  managerUsername,
			Error:     errMsg,
			CSRFToken: newCSRFToken(),
		})
	}
}

// wantsHTML returns true when the client is a browser (empty Accept or
// contains text/html). Defaults to HTML for empty Accept (browsers
// historically don't always send the header).
func wantsHTML(accept string) bool {
	if accept == "" {
		return true
	}
	return strings.Contains(accept, "text/html")
}

func newCSRFToken() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// csrfTokenOrEmpty returns the request's CSRF token if the cookie and
// form field match. For US-005 the form just round-trips the hidden field;
// real enforcement lands when we add /admin/ pages that mutate state.
func csrfTokenOrEmpty(c *gin.Context) string {
	return c.PostForm("csrf_token")
}

// SessionExpiry is a small helper for templates that need to render the
// session expiry as a human-readable duration.
func SessionExpiry(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	if d < time.Hour {
		mins := int(d.Minutes())
		return formatInt(mins) + " minutes"
	}
	hours := int(d.Hours())
	return formatInt(hours) + " hours"
}

func formatInt(n int) string {
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
