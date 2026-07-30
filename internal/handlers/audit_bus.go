package handlers

import (
	"sync"
)

// AuditBus is a per-process in-memory pub/sub for audit log entries.
// Every AuditLoggerImpl.Log call Publishes a fresh entry; the SSE
// handler Subscribes once per open browser tab and pipes the entries
// out as `event: audit_entry` SSE messages.
//
// Why in-memory (vs a DB poll or a LISTEN/NOTIFY channel):
//
//   - The audit table only gets written by this process. There's
//     nothing to learn from polling.
//   - The audit page is a single-instance UI — every browser tab
//     connected to this process is a valid subscriber. There's no
//     horizontal-scaling story in F-04.
//   - The volume is tiny (lifecycle actions, API key rotates, webhook
//     saves — single-digit per minute even on a busy box). A small
//     buffered channel per subscriber is plenty.
//
// Backpressure: a slow subscriber's channel fills up after 16
// unread entries; further Publishes for that subscriber are
// dropped silently (the audit row is still in the DB, so a
// page refresh catches up). This is the same trade-off the
// whatsmeow event stream makes in events.go — never let a
// slow consumer wedge the audit writer.
//
// Lifecycle: NewAuditBus returns a zero-value bus; there is
// no Close — the bus lives as long as the process. Subscribers
// must call the cancel func returned by Subscribe to avoid
// leaks (the SSE handler does this via defer).
type AuditBus struct {
	mu     sync.RWMutex
	subs   []chan AuditEntry
	closed bool
}

// AuditEntry is the published shape. Mirrors the table columns
// the audit page renders (timestamp, username, action, target_id,
// source_ip, user_agent) plus the optional `id` and `data` for
// future use. JSON tags are lowercase_snake_case to match the
// rest of the SSE wire format (status, qr_update payloads).
type AuditEntry struct {
	ID        string `json:"id,omitempty"`
	Timestamp string `json:"timestamp"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	TargetID  string `json:"target_id,omitempty"`
	SourceIP  string `json:"source_ip"`
	UserAgent string `json:"user_agent,omitempty"`
}

// NewAuditBus returns an empty bus ready for Publish/Subscribe.
func NewAuditBus() *AuditBus {
	return &AuditBus{}
}

// Subscribe returns a buffered channel that receives a copy of
// every entry published from this point on (entries published
// before Subscribe are NOT replayed — this is a live stream,
// not a queue). The cancel func removes the subscription and
// closes the channel.
//
// Buffer size = 16. A burst of 5-10 lifecycle actions in quick
// succession (e.g. an operator deleting several instances) still
// arrives intact; a pathologically slow consumer (network jam,
// page hidden for minutes) drops entries but doesn't block the
// audit writer.
func (b *AuditBus) Subscribe() (<-chan AuditEntry, func()) {
	if b == nil {
		// Defensive: a nil bus is treated as "no subscribers" so
		// Publish on a nil bus is a no-op without panicking.
		ch := make(chan AuditEntry)
		close(ch)
		return ch, func() {}
	}
	ch := make(chan AuditEntry, 16)
	b.mu.Lock()
	b.subs = append(b.subs, ch)
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, s := range b.subs {
			if s == ch {
				// Remove from slice without preserving order —
				// subscribers are equal-priority, no one cares
				// about position.
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, cancel
}

// Publish sends e to every current subscriber. Non-blocking:
// if a subscriber's buffer is full, that subscriber misses
// the entry. Best-effort by design — the audit row is the
// source of truth, the bus is a hint.
func (b *AuditBus) Publish(e AuditEntry) {
	if b == nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		// `select` with default = non-blocking send. The buffer
		// is the only protection against a wedged consumer; if
		// it's full, drop the entry for this subscriber.
		select {
		case ch <- e:
		default:
			// Drop. The DB still has the row; the next page
			// refresh catches up.
		}
	}
}

// SubscriberCount returns the number of active subscribers.
// Useful for tests and for the slog debug line we could add
// in main.go if it ever becomes interesting.
func (b *AuditBus) SubscriberCount() int {
	if b == nil {
		return 0
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}
