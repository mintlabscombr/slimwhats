package handlers

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mauroneto/whatsmeow-api/internal/instance"
)

// postFormWithID posts a form to a path with an :id param. The
// router is registered with /admin/instances/:id/<suffix> to match
// the production route group (adminUI in main.go). The form
// dispatcher reads c.Request.URL.Path via splitPath, which uses
// the leading /admin to index the id segment correctly (parts[3]
// in the full /admin/instances/{id}/... path).
func postFormWithID(handler gin.HandlerFunc, pathSuffix, id, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/admin/instances/:id/"+pathSuffix, handler)
	path := "/admin/instances/" + id + "/" + pathSuffix
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// TestWebhookFormActionHandler_ReRendersOnInvalidURL asserts
// F-03 / US-004: invalid URL → re-render the detail page (not
// redirect-with-msg), error code rendered, typed URL preserved.
func TestWebhookFormActionHandler_ReRendersOnInvalidURL(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	store := instance.NewStore(db)

	// Create an instance first so the form has a valid :id.
	inst, _, err := store.Create(instance.CreateInput{Name: "wh-target"})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}

	rr := postFormWithID(WebhookFormActionHandler(db, nil, store), "webhook", inst.ID,
		"url="+url.QueryEscape("not-a-url")+"&secret=secret123")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 re-render, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Webhook URL is invalid") {
		t.Error("expected url-invalid error in re-render")
	}
	if !strings.Contains(body, `value="not-a-url"`) {
		t.Error("typed url was wiped on re-render")
	}
	if rr.Header().Get("Location") != "" {
		t.Error("expected no redirect, got Location header")
	}
}

// TestWebhookFormActionHandler_ReRendersOnURLMissingSecret asserts
// the half-paired config (URL set, secret empty) re-renders with
// the secret-required code.
func TestWebhookFormActionHandler_ReRendersOnURLMissingSecret(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	store := instance.NewStore(db)

	inst, _, err := store.Create(instance.CreateInput{Name: "wh-target"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rr := postFormWithID(WebhookFormActionHandler(db, nil, store), "webhook", inst.ID,
		"url="+url.QueryEscape("https://example.com/wh"))

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Webhook secret is required when URL is set.") {
		t.Error("expected secret-required error")
	}
	if !strings.Contains(rr.Body.String(), `value="https://example.com/wh"`) {
		t.Error("typed url was wiped")
	}
}

// TestWebhookFormActionHandler_ClearsOnBothEmpty asserts the
// clear case (both url and secret blank) re-renders with a
// success message.
func TestWebhookFormActionHandler_ClearsOnBothEmpty(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	store := instance.NewStore(db)

	inst, _, err := store.Create(instance.CreateInput{Name: "wh-target"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rr := postFormWithID(WebhookFormActionHandler(db, nil, store), "webhook", inst.ID,
		"url=&secret=")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Webhook cleared.") {
		t.Error("expected success banner with 'Webhook cleared.'")
	}
}

// TestAPIKeyFormActionHandler_Rotate_NotFound asserts the
// unknown-id path: renderInstanceDetail's getInstanceView
// returns nil for a missing instance, which the helper
// surfaces as a 404 with the body "not found" (no detail page
// to render). Documenting the actual behaviour — a 200 re-render
// with an alert would be a UX improvement (operator sees
// "Instance not found." instead of a bare 404) but is a future
// change, not in F-03 / US-005.
func TestAPIKeyFormActionHandler_Rotate_NotFound(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	store := instance.NewStore(db)

	rr := postFormWithID(APIKeyFormActionHandler(APIKeyDeps{
		DB:    db,
		Store: store,
	}), "api-key/rotate", "nonexistent-id", "")

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown id, got %d (body: %s)", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Location") != "" {
		t.Error("expected no redirect on not-found")
	}
}

// TestAPIKeyFormActionHandler_Rotate_Success asserts the happy
// path re-renders the detail page (no redirect, no query
// string) with the success banner. The new key shows up in
// the API key section.
func TestAPIKeyFormActionHandler_Rotate_Success(t *testing.T) {
	db, cleanup := newTestDB(t)
	defer cleanup()
	store := instance.NewStore(db)

	inst, originalKey, err := store.Create(instance.CreateInput{
		Name:   "rotate-target",
		APIKey: "sk_live_original",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if originalKey != "sk_live_original" {
		t.Fatalf("expected original key, got %q", originalKey)
	}

	rr := postFormWithID(APIKeyFormActionHandler(APIKeyDeps{
		DB:    db,
		Store: store,
	}), "api-key/rotate", inst.ID, "")

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 re-render, got %d", rr.Code)
	}
	body := rr.Body.String()
	// Success banner
	if !strings.Contains(body, "API key rotated.") {
		t.Error("expected success banner")
	}
	// New key visible in the show/hide input (it starts masked
	// with type=password, so the value= attribute has the
	// plaintext — operator clicks "Show secret" to read it).
	// The sk_live_ prefix is the auto-generated key format.
	if !strings.Contains(body, "sk_live_") {
		t.Error("expected new auto-generated key (sk_live_...) in the re-render")
	}
	// No ?msg= / no redirect
	if rr.Header().Get("Location") != "" {
		t.Error("expected no redirect")
	}
	// DB now has a different key
	inst2, err := store.GetByID(inst.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if inst2.APIKey == "sk_live_original" {
		t.Error("DB still has the original key after rotate")
	}
	if !strings.HasPrefix(inst2.APIKey, "sk_live_") {
		t.Errorf("expected new key with sk_live_ prefix, got %q", inst2.APIKey)
	}
}
