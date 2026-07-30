package handlers

import (
	"sync"
	"testing"
	"time"
)

// TestAuditBus_PublishReceive asserts the happy path: one
// subscriber, one publish, the entry arrives intact.
func TestAuditBus_PublishReceive(t *testing.T) {
	bus := NewAuditBus()

	ch, cancel := bus.Subscribe()
	defer cancel()

	want := AuditEntry{
		Timestamp: "2026-07-30 12:00:00 UTC",
		Username:  "admin",
		Action:    "instance.create",
		TargetID:  "abc",
		SourceIP:  "127.0.0.1",
		UserAgent: "curl/8.0",
	}
	bus.Publish(want)

	select {
	case got := <-ch:
		if got != want {
			t.Errorf("received entry mismatch:\n got:  %+v\n want: %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published entry")
	}
}

// TestAuditBus_DropOnFullBuffer asserts the backpressure contract:
// a subscriber whose buffer is full does NOT block the publisher.
// We pre-fill the 16-slot buffer and verify a 17th publish returns
// immediately. (The full subscriber's 17th entry is dropped; the
// publisher never sees a stall.)
func TestAuditBus_DropOnFullBuffer(t *testing.T) {
	bus := NewAuditBus()
	ch, cancel := bus.Subscribe()
	defer cancel()
	_ = ch // used indirectly — the buffer is filled via Publish

	// Fill the 16-slot buffer. Each publish is non-blocking because
	// the slot is empty, so this loop returns immediately.
	for i := 0; i < 16; i++ {
		bus.Publish(AuditEntry{Action: "filler"})
	}
	// At this point ch is full. A 17th publish must NOT block. If
	// it did, the test would hang and Go's test runner would kill
	// it after 10 minutes — we add an explicit timeout so a
	// regression is caught as a clear failure instead.
	done := make(chan struct{})
	go func() {
		bus.Publish(AuditEntry{Action: "overflow"})
		close(done)
	}()
	select {
	case <-done:
		// Good — publisher returned.
	case <-time.After(time.Second):
		t.Fatal("Publish blocked when subscriber buffer was full")
	}
}

// TestAuditBus_CancelClosesChannel asserts the cancel func closes
// the subscriber channel and the subscriber is removed from the
// bus (subsequent Publishes don't try to send to the closed
// channel, which would panic).
func TestAuditBus_CancelClosesChannel(t *testing.T) {
	bus := NewAuditBus()
	ch, cancel := bus.Subscribe()
	if got, want := bus.SubscriberCount(), 1; got != want {
		t.Fatalf("subscriber count before cancel: got %d, want %d", got, want)
	}
	cancel()
	if got, want := bus.SubscriberCount(), 0; got != want {
		t.Errorf("subscriber count after cancel: got %d, want %d", got, want)
	}
	// Reading from a closed channel returns the zero value
	// immediately. If cancel failed to close, this would block.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel was not closed by cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel was not closed within 1s of cancel")
	}
	// After cancel, Publish must not panic (the bus removes the
	// subscriber from its slice before closing).
	bus.Publish(AuditEntry{Action: "after-cancel"})
}

// TestAuditBus_NilSafe asserts Publish/Subscribe on a nil bus is a
// no-op. The SSE handler must work even if the bus reference is
// nil (e.g. tests that don't wire one up).
func TestAuditBus_NilSafe(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil bus panicked: %v", r)
		}
	}()
	var bus *AuditBus // nil
	ch, cancel := bus.Subscribe()
	cancel()
	if got := bus.SubscriberCount(); got != 0 {
		t.Errorf("nil bus SubscriberCount: got %d, want 0", got)
	}
	// Publish on a nil bus must not panic.
	bus.Publish(AuditEntry{Action: "noop"})
	// ch is already closed (cancel ran) — read should return zero.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("nil-bus Subscribe channel should be closed")
		}
	case <-time.After(time.Second):
		t.Fatal("nil-bus Subscribe channel was not closed")
	}
}

// TestAuditBus_MultipleSubscribers asserts every subscriber gets
// a copy of each published entry (broadcast semantics).
func TestAuditBus_MultipleSubscribers(t *testing.T) {
	bus := NewAuditBus()
	ch1, cancel1 := bus.Subscribe()
	defer cancel1()
	ch2, cancel2 := bus.Subscribe()
	defer cancel2()

	bus.Publish(AuditEntry{Action: "broadcast", Username: "admin"})

	for i, ch := range []<-chan AuditEntry{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Action != "broadcast" {
				t.Errorf("subscriber %d: got action %q, want %q", i, got.Action, "broadcast")
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d timed out", i)
		}
	}
}

// TestAuditBus_PublishBeforeSubscribeMissed asserts the bus is a
// live stream: entries published before Subscribe are NOT replayed.
// (We have the DB for the replay story; the bus is just a hint.)
func TestAuditBus_PublishBeforeSubscribeMissed(t *testing.T) {
	bus := NewAuditBus()
	bus.Publish(AuditEntry{Action: "before-subscribe"})

	ch, cancel := bus.Subscribe()
	defer cancel()

	select {
	case got := <-ch:
		t.Errorf("received pre-subscribe entry: %+v", got)
	case <-time.After(50 * time.Millisecond):
		// Expected: nothing in the 50ms window.
	}
}

// TestAuditBus_ConcurrentPublishSubscribe is a smoke test for
// the RWMutex: 8 publishers and 4 subscribers, each publishing
// 100 entries, no panics and no lost-signal deadlocks. Cheap
// because the buffer absorbs the burst.
func TestAuditBus_ConcurrentPublishSubscribe(t *testing.T) {
	bus := NewAuditBus()
	const subs = 4
	const perSub = 100
	chs := make([]<-chan AuditEntry, subs)
	cancels := make([]func(), subs)
	for i := 0; i < subs; i++ {
		chs[i], cancels[i] = bus.Subscribe()
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	var wg sync.WaitGroup
	wg.Add(8)
	for p := 0; p < 8; p++ {
		p := p
		go func() {
			defer wg.Done()
			for i := 0; i < perSub; i++ {
				bus.Publish(AuditEntry{
					Action:   "concurrent",
					Username: "p",
					TargetID: "t",
				})
			}
		}()
		_ = p
	}
	wg.Wait()

	// Each subscriber should have received up to 8*perSub entries
	// (some dropped if a sub is slow). We just assert that each
	// sub has SOMETHING — a deadlock or zero-receive would fail
	// this check. A max-bound would be flaky on slow CI, so we
	// don't assert one.
	for i, ch := range chs {
		count := 0
	drain:
		for {
			select {
			case _, ok := <-ch:
				if !ok {
					break drain
				}
				count++
			case <-time.After(20 * time.Millisecond):
				break drain
			}
		}
		if count == 0 {
			t.Errorf("subscriber %d received 0 entries", i)
		}
	}
}
