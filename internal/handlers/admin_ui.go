package handlers

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	qrcode "github.com/skip2/go-qrcode"

	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// adminCSS is the shared stylesheet for every manager page. Centralized
// so all pages get the same look without per-template duplication.
const adminCSS = `
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: #f5f5f5; margin: 0; padding: 0; color: #222; }
  header { background: #1a7f37; color: #fff; padding: 1rem 2rem; display: flex; justify-content: space-between; align-items: center; }
  header h1 { margin: 0; font-size: 1.25rem; }
  header a { color: #fff; text-decoration: none; }
  header .nav a { margin-left: 1rem; opacity: .85; }
  header .nav a:hover { opacity: 1; }
  main { max-width: 1200px; margin: 1.5rem auto; padding: 0 1.5rem; }
  table { width: 100%; border-collapse: collapse; background: #fff; box-shadow: 0 1px 4px rgba(0,0,0,.05); border-radius: 6px; overflow: hidden; table-layout: auto; }
  td.muted, td .muted { color: #bbb; }
  th, td { padding: .75rem 1rem; text-align: left; border-bottom: 1px solid #eee; vertical-align: middle; white-space: nowrap; }
  th { font-size: .72rem; color: #888; letter-spacing: .04em; }
  /* Right-align date columns for a more typical table look */
  td.col-date, th.col-date { text-align: right; font-variant-numeric: tabular-nums; }
  /* Center the empty-cell "—" placeholder so the column looks balanced */
  td.col-empty { text-align: center; color: #ccc; }
  /* Don't let the row-click handler swallow the webhook URL tooltip */
  td[title] { cursor: help; }
  th { background: #fafafa; font-weight: 600; font-size: .85rem; color: #555; text-transform: uppercase; letter-spacing: .03em; }
  tr:last-child td { border-bottom: 0; }
  tr.row:hover { background: #fafdfb; cursor: pointer; }
  .badge { display: inline-block; padding: .15rem .55rem; border-radius: 12px; font-size: .75rem; font-weight: 600; text-transform: uppercase; }
  .badge.connected { background: #d4f7dc; color: #166a2e; }
  .badge.disconnected { background: #fff4cc; color: #8a6300; }
  .badge.logged_out, .badge.error { background: #fde0e0; color: #a00; }
  .badge.created, .badge.pairing { background: #ececec; color: #555; }
  .card { background: #fff; padding: 1.5rem 2rem; border-radius: 6px; box-shadow: 0 1px 4px rgba(0,0,0,.05); margin-bottom: 1.5rem; }
  .card h2 { margin: 0 0 1rem; font-size: 1.1rem; }
  .btn { display: inline-block; padding: .5rem 1rem; border-radius: 4px; background: #1a7f37; color: #fff; text-decoration: none; border: 0; cursor: pointer; font-size: .9rem; }
  .btn.secondary { background: #555; }
  .btn.danger { background: #a00; }
  .btn:hover { opacity: .9; }
  form { display: flex; flex-direction: column; gap: .75rem; }
  input, select, textarea { padding: .55rem .7rem; border: 1px solid #ccc; border-radius: 4px; font: inherit; }
  label { font-size: .85rem; color: #555; }
  .row { display: flex; gap: .5rem; align-items: center; }
  pre { background: #f5f5f5; padding: .75rem; border-radius: 4px; overflow-x: auto; font-size: .8rem; }
  .muted { color: #888; font-size: .85rem; }
  .error { background: #fee; border: 1px solid #fcc; color: #a00; padding: .75rem; border-radius: 4px; }
  .ok { background: #d4f7dc; border: 1px solid #a6e2b3; color: #166a2e; padding: .75rem; border-radius: 4px; }
`

// chromeHeader is the shared <header> block (the green bar with nav).
// Each page template includes it via the chrome funcMap.
const chromeHeader = `
<header>
  <h1><a href="/admin/">whatsmeow-api</a></h1>
  <div class="nav">
    <a href="/admin/">Instances</a>
    <a href="/admin/audit">Audit</a>
    <a href="/swagger">Swagger</a>
    <form style="display:inline" method="POST" action="/admin/logout"><button class="btn secondary" style="padding:.3rem .6rem;font-size:.8rem">Logout</button></form>
  </div>
</header>
`

// chromeFunc is the FuncMap entry that wraps a page body in the full
// admin layout. Each page template calls it as {{chrome .Body}}.
//
// Why this approach (vs the old "adminLayout template + {{define "content"}}"
// pattern): Go's html/template shares the parse tree across templates
// that descend from the same parent, so multiple `{{define "content"}}`
// blocks all overwrite each other — only the last-defined one wins,
// which made every page render the audit page. Inlining the chrome
// via a FuncMap sidesteps the shared-tree issue entirely.
func chromeFunc(title string, body template.HTML) (template.HTML, error) {
	out := fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>whatsmeow-api — %s</title>
<style>%s</style>
</head>
<body>
%s
<main>%s</main>
</body>
</html>`, title, adminCSS, chromeHeader, string(body))
	return template.HTML(out), nil
}

// adminFuncs is the FuncMap shared by all admin page templates.
var adminFuncs = template.FuncMap{
	"chrome": chromeFunc,
}

// adminListTmpl is the home page (US-019). Calls {{chrome .Body "Title"}}
// to render the full page; the chrome func wraps the body in the
// shared layout.
//
// Column order is intentional: name + created (operator scans these
// first) → status → phone/jid → webhook (configured?) → last activity
// (which falls back to created_at for new instances so the column is
// never blank). Empty cells render as muted "—" for visual consistency.
var adminListTmpl = template.Must(
	template.New("list").Funcs(adminFuncs).Parse(`{{define "render"}}<div class="card">
  <div style="display:flex;justify-content:space-between;align-items:center">
    <h2>Instances</h2>
    <a class="btn" href="/admin/instances/new">+ New instance</a>
  </div>
  {{if .Instances}}
  <table>
    <colgroup>
      <col>               <!-- name (auto) -->
      <col style="width:90px">  <!-- status badge -->
      <col style="width:160px"> <!-- phone / JID -->
      <col style="width:60px">  <!-- webhook -->
      <col class="col-date" style="width:150px"> <!-- last seen -->
      <col class="col-date" style="width:150px"> <!-- created -->
    </colgroup>
    <thead>
      <tr>
        <th>Name</th>
        <th>Status</th>
        <th>Phone / JID</th>
        <th>Webhook</th>
        <th class="col-date">Last seen</th>
        <th class="col-date">Created</th>
      </tr>
    </thead>
    <tbody>
    {{range .Instances}}
      <tr class="row" onclick="location.href='/admin/instances/{{.ID}}'">
        <td><b>{{.Name}}</b></td>
        <td><span class="badge {{.Status}}">{{.Status}}</span></td>
        <td>{{if .Phone}}{{.Phone}}{{else}}<span class="muted">—</span>{{end}}</td>
        <td class="col-empty">{{if .WebhookConfigured}}<span title="{{.WebhookURL}}">✓</span>{{else}}<span class="muted">—</span>{{end}}</td>
        <td class="col-date">{{if .LastSeen}}{{.LastSeen}}{{else}}{{.Created}}{{end}}</td>
        <td class="col-date muted">{{.Created}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}
  <p class="muted">No instances yet. <a href="/admin/instances/new">Create one</a>.</p>
  {{end}}
</div>{{end}}`),
)

// adminNewTmpl is the create form (US-020).
var adminNewTmpl = template.Must(
	template.New("new").Funcs(adminFuncs).Parse(`{{define "render"}}<div class="card" style="max-width:540px">
  <h2>New instance</h2>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  <form method="POST" action="/admin/instances/new">
    <label for="name">Name</label>
    <input id="name" name="name" required maxlength="64" placeholder="e.g. Sales BR">
    <label for="api_key">API key (optional, leave blank to auto-generate)</label>
    <input id="api_key" name="api_key" placeholder="sk_live_...">
    <div class="row" style="margin-top:.5rem">
      <button class="btn" type="submit">Create</button>
      <a class="btn secondary" href="/admin/">Cancel</a>
    </div>
  </form>
</div>{{end}}`),
)

// adminDetailTmpl is the per-instance page (US-021). Compact for v1.
var adminDetailTmpl = template.Must(
	template.New("detail").Funcs(adminFuncs).Parse(`{{define "render"}}<div class="card">
  <div style="display:flex;justify-content:space-between;align-items:center">
    <h2>{{.Instance.Name}}</h2>
    <div>
      <a class="btn secondary" href="/admin/">Back</a>
    </div>
  </div>
  <table>
    <tr><th>ID</th><td><code>{{.Instance.ID}}</code></td></tr>
    <tr><th>Status</th><td><span class="badge {{.Instance.Status}}">{{.Instance.Status}}</span></td></tr>
    <tr><th>Phone</th><td>{{.Instance.Phone}}</td></tr>
    <tr><th>JID</th><td>{{.Instance.JID}}</td></tr>
    <tr><th>LID</th><td>{{.Instance.LID}}</td></tr>
    <tr><th>API key (masked)</th><td><code>{{.Instance.APIKeyMasked}}</code></td></tr>
    <tr><th>API key set at</th><td>{{.Instance.APISetAt}}</td></tr>
    <tr><th>Webhook</th><td>{{if .Instance.WebhookConfigured}}{{.Instance.WebhookURL}}{{else}}<span class="muted">not configured</span>{{end}}</td></tr>
    <tr><th>Connected at</th><td>{{.Instance.ConnectedAt}}</td></tr>
    <tr><th>Last seen</th><td>{{.Instance.LastSeenAt}}</td></tr>
  </table>
</div>

<div class="card">
  <h2>Lifecycle</h2>
  <form method="POST" action="/admin/instances/{{.Instance.ID}}" style="display:flex;gap:.5rem;flex-wrap:wrap">
    <button class="btn" name="action" value="connect">Connect</button>
    <button class="btn secondary" name="action" value="disconnect">Disconnect</button>
    <button class="btn secondary" name="action" value="reconnect">Reconnect</button>
  </form>
  {{if .ActionResult}}<div class="{{.ActionResultClass}}">{{.ActionResult}}</div>{{end}}
  {{if .QR}}
  <div style="margin-top:1rem;text-align:center">
    <h3 style="text-align:left">QR code</h3>
    <p class="muted" style="text-align:left">Open WhatsApp → Linked Devices → Link a Device → scan this code. Refresh the page (Cmd+R) for a new one — the QR rotates every 60s.</p>
    <img src="{{.QR}}" alt="WhatsApp pairing QR code" style="display:inline-block;border:1px solid #ddd;padding:.5rem;background:#fff;border-radius:6px">
  </div>
  {{end}}
</div>

<div class="card">
  <h2>Webhook</h2>
  <form method="POST" action="/admin/api/instances/{{.Instance.ID}}/webhook">
    <label for="wh_url">URL</label>
    <input id="wh_url" name="url" placeholder="https://example.com/wh" value="{{.Instance.WebhookURL}}">
    <label for="wh_secret">Secret (16-128 chars)</label>
    <input id="wh_secret" name="secret" type="password" placeholder="leave blank to keep current">
    <button class="btn" type="submit">Save</button>
  </form>
</div>

<div class="card">
  <h2>API key</h2>
  <p class="muted">Masked: <code>{{.Instance.APIKeyMasked}}</code></p>
  <form method="POST" action="/admin/instances/{{.Instance.ID}}/reveal-key" style="display:flex;gap:.5rem;align-items:flex-end;flex-wrap:wrap">
    <div style="flex:1;min-width:200px">
      <label for="mgr_pw">Manager password (to reveal)</label>
      <input id="mgr_pw" name="manager_password" type="password">
    </div>
    <button class="btn" type="submit">Reveal</button>
  </form>
  {{if .RevealedKey}}
  <div class="ok" style="margin-top:1rem;word-break:break-all"><code>{{.RevealedKey}}</code></div>
  {{end}}
  <form method="POST" action="/admin/instances/{{.Instance.ID}}/api-key/rotate" style="margin-top:1rem;display:flex;gap:.5rem;align-items:center">
    <button class="btn danger" type="submit" onclick="return confirm('Rotate API key? The old key stops working immediately.')">Rotate API key</button>
  </form>
  {{if .NewAPIKey}}
  <div class="ok" style="margin-top:1rem;word-break:break-all">New key: <code>{{.NewAPIKey}}</code></div>
  {{end}}
</div>

<div class="card">
  <h2>Danger zone</h2>
  <form method="POST" action="/admin/instances/{{.Instance.ID}}/delete" style="display:flex;gap:.5rem;align-items:center">
    <button class="btn danger" type="submit" onclick="return confirm('Delete this instance? This is irreversible.')">Delete instance</button>
  </form>
</div>{{end}}`),
)

// adminAuditTmpl is the audit log page (US-030).
var adminAuditTmpl = template.Must(
	template.New("audit").Funcs(adminFuncs).Parse(`{{define "render"}}<div class="card">
  <h2>Audit log</h2>
  {{if .Entries}}
  <table>
    <thead><tr><th>Time</th><th>User</th><th>Action</th><th>Target</th><th>IP</th><th>UA</th></tr></thead>
    <tbody>
    {{range .Entries}}
      <tr>
        <td>{{.Timestamp}}</td>
        <td>{{.Username}}</td>
        <td><code>{{.Action}}</code></td>
        <td>{{if .TargetID}}<a href="/admin/instances/{{.TargetID}}">{{.TargetID}}</a>{{else}}<span class="muted">—</span>{{end}}</td>
        <td>{{.SourceIP}}</td>
        <td class="muted" style="font-size:.75rem;max-width:200px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">{{.UserAgent}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}
  <p class="muted">No actions logged yet.</p>
  {{end}}
</div>{{end}}`),
)

// renderAdmin is a tiny helper: execute the named "render" template
// against data, wrap it in the chrome via the FuncMap, and write to
// c.Writer. Returns true on success, false on a render error (which
// has already been written to the response).
func renderAdmin(tmpl *template.Template, title string, data any, c *gin.Context) bool {
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, "render", data); err != nil {
		c.String(http.StatusInternalServerError, "template render: %v", err)
		return false
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	// Run the chrome function with the rendered body.
	html, err := chromeFunc(title, template.HTML(buf.String()))
	if err != nil {
		c.String(http.StatusInternalServerError, "chrome: %v", err)
		return false
	}
	_, _ = c.Writer.Write([]byte(html))
	return true
}

// InstanceRow is the row in the home page table.
type InstanceRow struct {
	ID                string
	Name              string
	Status            string
	Phone             string
	WebhookConfigured bool
	WebhookURL        string
	LastSeen          string // empty if the instance has never connected
	Created           string
	CreatedAt         time.Time
}

// AdminListPage handles GET /admin/.
func AdminListPage(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.Query(`SELECT id, name, status, phone, webhook_url, last_seen_at, created_at FROM instances ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			c.String(http.StatusInternalServerError, "load failed: %v", err)
			return
		}
		defer rows.Close()
		var list []InstanceRow
		for rows.Next() {
			var r InstanceRow
			var phone, wh sql.NullString
			var ls sql.NullTime
			if err := rows.Scan(&r.ID, &r.Name, &r.Status, &phone, &wh, &ls, &r.CreatedAt); err != nil {
				continue
			}
			if phone.Valid {
				r.Phone = phone.String
			}
			if wh.Valid {
				r.WebhookConfigured = true
				r.WebhookURL = wh.String
			}
			if ls.Valid {
				r.LastSeen = ls.Time.UTC().Format("2006-01-02 15:04")
			}
			// Note: r.LastSeen stays "" if the instance has never connected.
			// The list template falls back to r.Created in that case so the
			// column never shows a meaningless "—".
			r.Created = r.CreatedAt.UTC().Format("2006-01-02 15:04")
			list = append(list, r)
		}
		renderAdmin(adminListTmpl, "Instances", gin.H{"Instances": list}, c)
	}
}

// AdminNewPage handles GET /admin/instances/new.
func AdminNewPage() gin.HandlerFunc {
	return func(c *gin.Context) {
		renderAdmin(adminNewTmpl, "New instance", gin.H{
			"Error": c.Query("error"),
		}, c)
	}
}

// AdminNewSubmit handles POST /admin/instances/new (form action).
// Stub kept for backward compat — the actual create goes through
// the JSON API at /admin/api/instances.
func AdminNewSubmit(store interface {
	Create(in interface{ Name() string }) (id, name, apiKey, status string, err error)
}) gin.HandlerFunc {
	return func(c *gin.Context) { _ = store }
}

// AdminDetailPage handles GET /admin/instances/{id}.
//
// For an unpaired instance (status == "created"), the handler auto-
// fetches a fresh QR code from whatsmeow and renders it as a base64-
// encoded PNG data URI — no external CDN, no JS, no extra round-trip.
// The user just refreshes the page (Cmd+R) to get a new QR.
func AdminDetailPage(db *sql.DB, mgr *instance.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		v, err := getInstanceView(db, id)
		if err != nil {
			c.String(http.StatusInternalServerError, "load failed: %v", err)
			return
		}
		if v == nil {
			c.String(http.StatusNotFound, "not found")
			return
		}
		// Format times
		if v.ConnectedAt != "" {
			t, _ := time.Parse("2006-01-02 15:04:05.999999-07:00", v.ConnectedAt)
			if !t.IsZero() {
				v.ConnectedAt = t.UTC().Format("2006-01-02 15:04 MST")
			}
		}
		if v.LastSeenAt != "" {
			t, _ := time.Parse("2006-01-02 15:04:05.999999-07:00", v.LastSeenAt)
			if !t.IsZero() {
				v.LastSeenAt = t.UTC().Format("2006-01-02 15:04 MST")
			}
		}
		if v.APISetAt != "" {
			t, _ := time.Parse("2006-01-02 15:04:05.999999-07:00", v.APISetAt)
			if !t.IsZero() {
				v.APISetAt = t.UTC().Format("2006-01-02 15:04 MST")
			}
		}
		_ = v.CreatedAt
		_ = v.UpdatedAt

		// Auto-fetch a fresh QR if the instance isn't paired yet.
		// Skips on already-paired instances (the QR block hides itself
		// in the template via {{if .QR}}).
		var qrPNG template.URL
		if v.Status == instance.StatusCreated || v.Status == instance.StatusPairing {
			qrPNG = fetchQRPNG(mgr, id)
		}

		renderAdmin(adminDetailTmpl, v.Name, gin.H{
			"Instance":          v,
			"ActionResult":      c.Query("msg"),
			"ActionResultClass": c.Query("msg_class"),
			"QR":                qrPNG,
			"RevealedKey":       c.Query("revealed_key"),
			"NewAPIKey":         c.Query("new_api_key"),
		}, c)
	}
}

// fetchQRPNG lazy-loads the whatsmeow client, pulls one QR payload,
// renders it as a PNG, and returns the data URI as a template.URL
// (which the html/template engine treats as already-safe, bypassing
// the URL-context auto-escape that would otherwise strip the
// "data:" scheme). Returns template.URL("") on failure.
//
// The 30s timeout is generous because whatsmeow's first QR can take
// a few seconds to come through (the client has to start its
// internal handshake). Subsequent refreshes are fast.
func fetchQRPNG(mgr *instance.Manager, instanceID string) template.URL {
	cli := mgr.Get(instanceID)
	if cli == nil || cli.IsLoggedIn() {
		return template.URL("") // missing or already paired; nothing to show
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := instance.GetLatestQR(ctx, cli)
	if err != nil || raw == "" {
		slog.Warn("fetchQRPNG: get latest QR failed", "id", instanceID, "err", err)
		return template.URL("")
	}
	// Render as 256x256 PNG, medium error-correction. Pure Go, no
	// external CDN; the PNG is base64-embedded in the HTML.
	png, err := qrcode.Encode(raw, qrcode.Medium, 256)
	if err != nil {
		slog.Warn("fetchQRPNG: qrcode.Encode failed", "err", err)
		return template.URL("")
	}
	return template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
}

// AdminAuditPage handles GET /admin/audit.
func AdminAuditPage(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		rows, err := db.Query(`SELECT timestamp, username, action, target_id, source_ip, user_agent FROM admin_actions ORDER BY timestamp DESC LIMIT 100`)
		if err != nil {
			c.String(http.StatusInternalServerError, "load failed: %v", err)
			return
		}
		defer rows.Close()
		type entry struct {
			Timestamp string
			Username  string
			Action    string
			TargetID  string
			SourceIP  string
			UserAgent string
		}
		var list []entry
		for rows.Next() {
			var e entry
			var tid, ua sql.NullString
			if err := rows.Scan(&e.Timestamp, &e.Username, &e.Action, &tid, &e.SourceIP, &ua); err != nil {
				continue
			}
			if tid.Valid {
				e.TargetID = tid.String
			}
			if ua.Valid {
				e.UserAgent = ua.String
			}
			list = append(list, e)
		}
		renderAdmin(adminAuditTmpl, "Audit log", gin.H{"Entries": list}, c)
	}
}

// getInstanceView is a small helper that loads a single instance by id.
func getInstanceView(db *sql.DB, id string) (*InstanceView, error) {
	row := db.QueryRow(`
		SELECT id, name, status, phone, jid, lid, webhook_url, status,
		       connected_at, last_seen_at, api_key_set_at, created_at, updated_at
		FROM instances WHERE id = ?`, id)
	var v InstanceView
	var phone, jid, lid, wh sql.NullString
	var ca, ls, as sql.NullTime
	if err := row.Scan(&v.ID, &v.Name, &v.Status, &phone, &jid, &lid, &wh, &v.Status, &ca, &ls, &as, &v.CreatedAt, &v.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if phone.Valid {
		v.Phone = phone.String
	}
	if jid.Valid {
		v.JID = jid.String
	}
	if lid.Valid {
		v.LID = lid.String
	}
	if wh.Valid {
		v.WebhookURL = wh.String
		v.WebhookConfigured = true
	}
	if ca.Valid {
		v.ConnectedAt = ca.Time.UTC().Format("2006-01-02 15:04:05.999999-07:00")
	}
	if ls.Valid {
		v.LastSeenAt = ls.Time.UTC().Format("2006-01-02 15:04:05.999999-07:00")
	}
	if as.Valid {
		v.APISetAt = as.Time.UTC().Format("2006-01-02 15:04:05.999999-07:00")
	}
	return &v, nil
}
