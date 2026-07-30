package handlers

import (
	"strings"
	"testing"
)

// TestMessage_NonEmptyForAllCodes asserts that every defined error
// code (i.e. every constant in the package prefixed with ErrCode)
// maps to a non-empty user-friendly message. This is the catalog
// sanity test — if you add a new ErrCode constant without adding
// it to errMessages, the template will silently fall back to
// ErrCodeInternal and operators will see "Internal error" instead
// of the real problem.
func TestMessage_NonEmptyForAllCodes(t *testing.T) {
	codes := []string{
		ErrCodeNameRequired,
		ErrCodeNameTaken,
		ErrCodeNameTooLong,
		ErrCodeWebhookURLEmpty,
		ErrCodeURLRequired,
		ErrCodeURLInvalid,
		ErrCodeSecretRequired,
		ErrCodeSecretOrphan,
		ErrCodeURLEmpty,
		ErrCodeLookupFailed,
		ErrCodeNotFound,
		ErrCodeNotPaired,
		ErrCodeStartFailed,
		ErrCodeConnectFailed,
		ErrCodeDisconnectFailed,
		ErrCodeReconnectFailed,
		ErrCodeAPIKeySetFailed,
		ErrCodeAPIKeyRotateFailed,
		ErrCodeAPIKeyInvalid,
		ErrCodeInvalidCredentials,
		ErrCodeRateLimited,
		ErrCodeInvalidRequest,
		ErrCodeSessionCreate,
		ErrCodeInternal,
	}
	for _, c := range codes {
		if c == "" {
			continue // ErrCodeOK is the no-error sentinel
		}
		got := Message(c)
		if got == "" {
			t.Errorf("Message(%q) returned empty string", c)
		}
		if got == Message(ErrCodeInternal) && c != ErrCodeInternal {
			t.Errorf("Message(%q) fell back to internal-error fallback (missing errMessages entry?)", c)
		}
	}
}

// TestMessage_EmptyForEmptyCode asserts the no-error sentinel
// returns "" so templates can do `{{if .ErrorCode}}` cleanly.
func TestMessage_EmptyForEmptyCode(t *testing.T) {
	if got := Message(""); got != "" {
		t.Errorf(`Message("") = %q, want ""`, got)
	}
	if got := Message(ErrCodeOK); got != "" {
		t.Errorf("Message(ErrCodeOK) = %q, want \"\"", got)
	}
}

// TestMessage_UnknownCodeReturnsFallback asserts that an unknown
// code (e.g. a typo in the URL) returns the internal-error
// fallback instead of an empty string or panic.
func TestMessage_UnknownCodeReturnsFallback(t *testing.T) {
	got := Message("not_a_real_code_zzz")
	if got == "" {
		t.Error("Message for unknown code returned empty string")
	}
	if got != Message(ErrCodeInternal) {
		t.Errorf("Message for unknown code = %q, want internal fallback %q", got, Message(ErrCodeInternal))
	}
}

// TestMessage_FormatArgsForRateLimited asserts the variadic args
// path is wired up. The rate_limited message is the only one that
// uses args (the retry seconds); if someone refactors Message and
// breaks the Sprintf, this test catches it.
func TestMessage_FormatArgsForRateLimited(t *testing.T) {
	got := Message(ErrCodeRateLimited, 120)
	if !strings.Contains(got, "120") {
		t.Errorf("Message(rate_limited, 120) = %q, expected to contain \"120\"", got)
	}
	if !strings.Contains(got, "seconds") {
		t.Errorf("Message(rate_limited, 120) = %q, expected to contain \"seconds\"", got)
	}
}
