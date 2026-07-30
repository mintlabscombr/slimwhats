package handlers

import "fmt"

// Error code constants. Short snake_case — these end up in URL
// query strings (e.g. `?error=name_required`) so keep them
// URL-safe and grep-friendly. The matching English copy lives
// in `errMessages` below; templates call `Message(code)` via the
// `adminFuncs` template funcMap to resolve the code at render time.
//
// The full list of admin UI errors is centralised here so a new
// error = 1 new constant + 1 map entry. No more inline message
// strings scattered across form-dispatcher handlers.
const (
	// Success (no error). Empty string is the convention for "no error".
	ErrCodeOK = ""

	// New instance form (AdminNewSubmit).
	ErrCodeNameRequired   = "name_required"
	ErrCodeNameTaken      = "name_taken"
	ErrCodeNameTooLong    = "name_too_long"
	ErrCodeWebhookURLEmpty = "webhook_url_empty"

	// Webhook form (WebhookFormActionHandler).
	ErrCodeURLRequired    = "url_required"
	ErrCodeURLInvalid     = "url_invalid"
	ErrCodeSecretRequired = "secret_required"
	ErrCodeSecretOrphan   = "secret_orphan"
	ErrCodeURLEmpty       = "webhook_url_orphan"

	// Lifecycle (LifecycleActionHandler + ConnectInstanceHandler).
	ErrCodeLookupFailed  = "lookup_failed"
	ErrCodeNotFound      = "not_found"
	ErrCodeNotPaired     = "not_paired"
	ErrCodeStartFailed   = "start_failed"
	ErrCodeConnectFailed   = "connect_failed"
	ErrCodeDisconnectFailed = "disconnect_failed"
	ErrCodeReconnectFailed  = "reconnect_failed"

	// API key rotate / set / delete (APIKeyFormActionHandler).
	ErrCodeAPIKeySetFailed    = "api_key_set_failed"
	ErrCodeAPIKeyRotateFailed = "api_key_rotate_failed"
	ErrCodeAPIKeyInvalid      = "api_key_invalid"

	// Login (LoginHandler).
	ErrCodeInvalidCredentials = "invalid_credentials"
	ErrCodeRateLimited        = "rate_limited"
	ErrCodeInvalidRequest     = "invalid_request"
	ErrCodeSessionCreate      = "session_create_failed"

	// Generic fallback.
	ErrCodeInternal = "internal_error"
)

// errMessages is the single source of truth for admin UI error copy.
// Codes with parameters use Go format verbs (e.g. %d for seconds).
// Unknown codes return the internal-error fallback so the template
// always has something to render.
var errMessages = map[string]string{
	// New instance form
	ErrCodeNameRequired:    "Name is required.",
	ErrCodeNameTaken:       "An instance with this name already exists.",
	ErrCodeNameTooLong:     "Name must be 64 characters or fewer.",
	ErrCodeWebhookURLEmpty: "Webhook URL is required when webhook secret is set.",

	// Webhook form
	ErrCodeURLRequired:    "Webhook URL is required.",
	ErrCodeURLInvalid:     "Webhook URL is invalid (must be http or https).",
	ErrCodeSecretRequired: "Webhook secret is required when URL is set.",
	ErrCodeSecretOrphan:   "Webhook secret is set but URL is empty.",
	ErrCodeURLEmpty:       "Webhook URL is set but secret is empty.",

	// Lifecycle
	ErrCodeLookupFailed:      "Could not load the instance.",
	ErrCodeNotFound:          "Instance not found.",
	ErrCodeNotPaired:         "Instance is not paired. Open the detail page and scan the QR code to pair.",
	ErrCodeStartFailed:       "Could not start the instance.",
	ErrCodeConnectFailed:     "Could not connect the instance.",
	ErrCodeDisconnectFailed:  "Could not disconnect the instance.",
	ErrCodeReconnectFailed:   "Could not reconnect the instance.",

	// API key
	ErrCodeAPIKeySetFailed:    "Could not set the API key.",
	ErrCodeAPIKeyRotateFailed: "Could not rotate the API key.",
	ErrCodeAPIKeyInvalid:      "API key is invalid.",

	// Login
	ErrCodeInvalidCredentials: "Invalid username or password.",
	ErrCodeRateLimited:        "Too many failed attempts. Try again in %d seconds.",
	ErrCodeInvalidRequest:     "Password is required.",
	ErrCodeSessionCreate:      "Could not start a session. Please try again.",

	// Generic
	ErrCodeInternal: "Internal error. Please try again.",
}

// Message returns the user-friendly English text for the given
// error code. For codes that take parameters (e.g. ErrCodeRateLimited
// with the retry seconds), pass the values as args; the result is
// fmt.Sprintf'd against the template string. Unknown codes return
// the internal-error fallback so templates can render unconditionally
// (`{{if .ErrorCode}}{{message .ErrorCode}}{{end}}` is safe even
// when ErrorCode is "").
//
// The variadic signature keeps the simple case (`Message("name_required")`)
// trivial while supporting templated cases (`Message("rate_limited", 120)`).
func Message(code string, args ...any) string {
	if code == "" {
		return ""
	}
	tmpl, ok := errMessages[code]
	if !ok {
		return errMessages[ErrCodeInternal]
	}
	if len(args) == 0 {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}
