// Package send implements the outbound message send flow: recipient
// resolution, typing simulation, retry, and the per-type button
// envelope construction.
package send

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

// RecipInput is the input to Resolve.
type RecipInput struct {
	Number     string
	FormatJID  *bool
	DisableWA  bool // APP_CHECK_USER_EXISTS=false
}

// Resolve implements the FR-13a pipeline: bypass list → IsOnWhatsApp
// lookup (two attempts) → CreateJID normalization → + strip.
//
// Returns a validated types.JID that the caller can use with client.SendMessage.
func Resolve(ctx context.Context, client *whatsmeow.Client, in RecipInput) (types.JID, error) {
	if in.Number == "" {
		return types.JID{}, errors.New("number is empty")
	}
	// 1. Bypass list — group/broadcast/newsletter/LID don't go through IsOnWhatsApp
	if in.DisableWA || isBypassServer(in.Number) {
		return normalizeJID(in.Number, in.FormatJID), nil
	}
	// 2. IsOnWhatsApp lookup
	jid, err := lookupOnWA(ctx, client, in.Number, in.FormatJID)
	if err != nil {
		slog.Warn("IsOnWhatsApp failed, falling back to direct parse", "err", err)
		return normalizeJID(in.Number, in.FormatJID), nil
	}
	if jid != "" {
		// Trust WA's canonical JID
		parsed, err := types.ParseJID(jid)
		if err != nil {
			return types.JID{}, fmt.Errorf("parse WA jid: %w", err)
		}
		return parsed, nil
	}
	return types.JID{}, fmt.Errorf("number %s is not registered on WhatsApp", in.Number)
}

func isBypassServer(s string) bool {
	for _, srv := range []string{"@g.us", "@broadcast", "@newsletter", "@lid"} {
		if strings.Contains(s, srv) {
			return true
		}
	}
	return false
}

func lookupOnWA(ctx context.Context, client *whatsmeow.Client, raw string, formatJID *bool) (string, error) {
	// Attempt 1: raw
	if jid, ok, err := callIsOnWA(ctx, client, raw); err == nil && ok {
		return jid, nil
	}
	// Attempt 2: normalized
	normalized := normalizePhone(raw)
	if normalized != raw {
		if jid, ok, err := callIsOnWA(ctx, client, normalized); err == nil && ok {
			return jid, nil
		}
	}
	return "", nil
}

func callIsOnWA(ctx context.Context, client *whatsmeow.Client, num string) (string, bool, error) {
	resp, err := client.IsOnWhatsApp(ctx, []string{num})
	if err != nil {
		return "", false, err
	}
	if len(resp) == 0 {
		return "", false, nil
	}
	if !resp[0].IsIn {
		return "", false, nil
	}
	return resp[0].JID.String(), true, nil
}

func normalizePhone(s string) string {
	return CreateJID(s)
}

func normalizeJID(s string, formatJID *bool) types.JID {
	shouldFormat := true
	if formatJID != nil {
		shouldFormat = *formatJID
	}
	raw := s
	if strings.Contains(s, "@s.whatsapp.net") {
		raw = strings.SplitN(s, "@", 2)[0]
	}
	normalized := CreateJID(raw)
	jid, err := types.ParseJID(normalized)
	if err != nil {
		// Fallback: parse the raw string directly
		jid, _ = types.ParseJID(s)
	}
	// Strip leading + from the user part (whatsmeow doesn't want it)
	jid.User = strings.ReplaceAll(jid.User, "+", "")
	_ = shouldFormat
	return jid
}

// --- CreateJID (portable phone number normalization, BR/MX/AR rules) ---

var digitsOnlyRe = regexp.MustCompile(`[^0-9]`)

// CreateJID normalizes a digit-only input per the FR-13a-bis rules. It
// always returns a string like "+15551112222@s.whatsapp.net" (or
// "<digits>@g.us" for groups).
func CreateJID(input string) string {
	if input == "" {
		return ""
	}
	// If the input already has a server, pass through.
	for _, suffix := range []string{"@g.us", "@s.whatsapp.net", "@lid", "@broadcast", "@newsletter"} {
		if strings.Contains(input, suffix) {
			return input
		}
	}
	// Strip non-digit-ish characters
	cleaned := strings.ReplaceAll(input, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "+", "")
	cleaned = strings.ReplaceAll(cleaned, "(", "")
	cleaned = strings.ReplaceAll(cleaned, ")", "")
	cleaned = strings.SplitN(cleaned, ":", 2)[0]

	// Group ID heuristic: contains "-" and >= 24 chars, OR >= 18 digits
	if strings.Contains(cleaned, "-") && len(cleaned) >= 24 {
		return stripNonDigitsDashes(cleaned) + "@g.us"
	}
	if len(cleaned) >= 18 {
		return stripNonDigitsDashes(cleaned) + "@g.us"
	}

	// Phone number: keep digits only
	digits := digitsOnlyRe.ReplaceAllString(cleaned, "")
	if digits == "" {
		return ""
	}

	// Apply country rules
	digits = formatMXOrARNumber(digits)
	digits = formatBRNumber(digits)

	if !strings.HasPrefix(digits, "+") {
		digits = "+" + digits
	}
	return digits + "@s.whatsapp.net"
}

func stripNonDigitsDashes(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '-' {
			out = append(out, c)
		}
	}
	return string(out)
}

// formatBRNumber applies the BR legacy-9 stripping rule per FR-13a-bis.
func formatBRNumber(jid string) string {
	if len(jid) != 13 || !strings.HasPrefix(jid, "55") {
		return jid
	}
	ddd, _ := atoiSafe(jid[2:4])
	first, _ := atoiSafe(jid[4:5])
	if ddd >= 31 && first >= 7 {
		return jid[:4] + jid[5:]
	}
	return jid
}

// formatMXOrARNumber applies MX/AR 13-digit mobile-digit stripping.
func formatMXOrARNumber(jid string) string {
	if len(jid) == 13 {
		switch {
		case strings.HasPrefix(jid, "52"):
			return "52" + jid[4:]
		case strings.HasPrefix(jid, "54"):
			return "54" + jid[3:]
		}
	}
	return jid
}

func atoiSafe(s string) (int, bool) {
	if len(s) == 0 {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, true
}
