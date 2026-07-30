package handlers

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/mauroneto/whatsmeow-api/internal/instance"
	"github.com/mauroneto/whatsmeow-api/internal/store"
)

// newTestDB opens a fresh sqlite DB in a per-test temp dir, runs
// migrations, and returns the *sql.DB + a cleanup func. Each test
// gets its own DB so they're fully isolated.
func newTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dsn := filepath.Join(dir, "test.db")
	db, err := store.Open(context.Background(), "sqlite", dsn)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := store.Migrate(db, "sqlite"); err != nil {
		_ = db.Close()
		os.RemoveAll(dir)
		t.Fatalf("migrate test db: %v", err)
	}
	return db, func() {
		_ = db.Close()
		os.RemoveAll(dir)
	}
}

// postForm spins up a Gin engine with the given handler, POSTs the
// given form to the given path, and returns the response. Bypasses
// auth middleware — we're testing handler logic, not session gating.
func postForm(handler gin.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST(path, handler)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestAdminNewSubmit_ReRendersOnEmptyName asserts the F-03 / US-003
// promise: an empty name doesn't redirect-with-?error=, it re-renders
// the form with the typed values preserved + an alert block. No URL
// pollution, no wiping of the form.
func TestAdminNewSubmit_ReRendersOnEmptyName(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	inst := instance.NewStore(db)

	rr := postForm(AdminNewSubmit(inst, nil), "/admin/instances/new",
		"api_key=hello&webhook_url="+url.QueryEscape("https://example.com/wh")+
			"&webhook_secret=secret123")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	// The empty-name error should render via the message funcMap.
	if !strings.Contains(body, "Name is required.") {
		t.Errorf("expected name-required error in body; got: %s", extractAlert(body))
	}
	// Typed values must be preserved (NOT wiped) on re-render.
	if !strings.Contains(body, `value="hello"`) {
		t.Error("api_key typed value was wiped on re-render")
	}
	if !strings.Contains(body, `value="https://example.com/wh"`) {
		t.Error("webhook_url typed value was wiped on re-render")
	}
	if !strings.Contains(body, `value="secret123"`) {
		t.Error("webhook_secret typed value was wiped on re-render")
	}
	// The body must be the new-instance page (chrome header), not a
	// 302 redirect to itself.
	if !strings.Contains(body, "New instance") {
		t.Error("expected new-instance page title in re-render")
	}
	if rr.Header().Get("Location") != "" {
		t.Errorf("expected no Location header (no redirect), got: %s", rr.Header().Get("Location"))
	}
}

// TestAdminNewSubmit_ReRendersOnNameTooLong asserts the 64-char
// limit surfaces as ErrCodeNameTooLong with the typed name preserved.
func TestAdminNewSubmit_ReRendersOnNameTooLong(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	inst := instance.NewStore(db)

	longName := strings.Repeat("x", 65)
	rr := postForm(AdminNewSubmit(inst, nil), "/admin/instances/new",
		"name="+url.QueryEscape(longName))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Name must be 64 characters or fewer.") {
		t.Error("expected name-too-long error")
	}
	if !strings.Contains(rr.Body.String(), `value="`+longName+`"`) {
		t.Error("typed long name was wiped on re-render")
	}
}

// TestAdminNewSubmit_ReRendersOnWebhookOrphan asserts the webhook
// URL-without-secret (and the inverse) re-render with the typed
// values intact and a code-mapped error.
func TestAdminNewSubmit_ReRendersOnWebhookOrphan(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	inst := instance.NewStore(db)

	// URL set, secret empty → ErrCodeSecretRequired.
	rr := postForm(AdminNewSubmit(inst, nil), "/admin/instances/new",
		"name=foo&webhook_url="+url.QueryEscape("https://example.com/wh"))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Webhook secret is required when URL is set.") {
		t.Error("expected secret-required error")
	}
	if !strings.Contains(body, `value="https://example.com/wh"`) {
		t.Error("typed webhook_url was wiped on re-render")
	}
	// The secret input is type=password, so the empty value is
	// value="" — we just check the form is rendered.
	if !strings.Contains(body, `name="webhook_secret"`) {
		t.Error("webhook_secret input missing from re-render")
	}
}

// TestAdminNewSubmit_ReRendersOnDuplicateName asserts the
// store.ErrNameTaken path also re-renders (not redirect-with-msg).
// Two-step: first create, then try to create again with the same name.
func TestAdminNewSubmit_ReRendersOnDuplicateName(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	inst := instance.NewStore(db)

	// First create succeeds → 302 to detail page.
	rr1 := postForm(AdminNewSubmit(inst, nil), "/admin/instances/new",
		"name=dupe&api_key=first-key")
	if rr1.Code != http.StatusFound {
		t.Fatalf("first create: expected 302, got %d (body: %s)", rr1.Code, rr1.Body.String())
	}

	// Second create with same name → re-render with name_taken error.
	rr2 := postForm(AdminNewSubmit(inst, nil), "/admin/instances/new",
		"name=dupe&api_key=second-key")
	if rr2.Code != http.StatusOK {
		t.Fatalf("duplicate create: expected 200 re-render, got %d", rr2.Code)
	}
	body := rr2.Body.String()
	if !strings.Contains(body, "An instance with this name already exists.") {
		t.Error("expected name-taken error in re-render")
	}
	if !strings.Contains(body, `value="dupe"`) {
		t.Error("typed name was wiped on duplicate-name re-render")
	}
	if !strings.Contains(body, `value="second-key"`) {
		t.Error("typed api_key was wiped on duplicate-name re-render")
	}
}

// TestAdminNewSubmit_AcceptsArbitraryAPIKey asserts the F-03 / US-002
// promise: any non-empty string is a valid API key. No more regex
// rejection of UUIDs / base64 / custom prefixes.
func TestAdminNewSubmit_AcceptsArbitraryAPIKey(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	inst := instance.NewStore(db)

	cases := []struct {
		name string
		key  string
	}{
		{"uuid-with-dashes", "550e8400-e29b-41d4-a716-446655440000"},
		{"base64-random", "aGVsbG8td29ybGQtdGhpcy1pcy1hLXRlc3Q="},
		{"custom-prefix", "myapp_secretkey_1234567890abcdef"},
		{"short", "abc"},
		{"long-128-chars", strings.Repeat("x", 128)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := postForm(AdminNewSubmit(inst, nil), "/admin/instances/new",
				"name="+url.QueryEscape(tc.name)+"&api_key="+url.QueryEscape(tc.key))
			// Success path: 302 to /admin/instances/{id}. No error
			// means the key was accepted. The regex rejection would
			// return 200 with a re-render + error.
			if rr.Code != http.StatusFound {
				t.Errorf("key %q: expected 302 success, got %d (body: %s)",
					tc.key, rr.Code, extractAlert(rr.Body.String()))
			}
		})
	}
}

// extractAlert pulls the contents of the first <div class="alert ...">
// from an HTML body, or returns the whole body if none. Used for
// cleaner test failure output.
func extractAlert(body string) string {
	i := strings.Index(body, `class="alert`)
	if i < 0 {
		// Try a substring of the body for context.
		end := len(body)
		if end > 200 {
			end = 200
		}
		return body[:end]
	}
	end := strings.Index(body[i:], `</div>`)
	if end < 0 {
		return body[i:]
	}
	return body[i : i+end]
}
