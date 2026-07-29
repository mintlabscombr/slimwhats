package send

import (
	"testing"
	"time"
)

// TestPerJIDRateLimiter_AllowsUnderLimit verifies that the limiter lets
// requests through when the per-JID count is below the max.
func TestPerJIDRateLimiter_AllowsUnderLimit(t *testing.T) {
	rl := NewPerJIDRateLimiter()
	for i := 0; i < 20; i++ {
		ok, retry := rl.Check("5511999999999@s.whatsapp.net")
		if !ok {
			t.Fatalf("request %d should be allowed; got retry=%v", i+1, retry)
		}
		if retry != 0 {
			t.Fatalf("retry should be 0 on allow; got %v", retry)
		}
	}
}

// TestPerJIDRateLimiter_DeniesAtLimit verifies the 21st request to the
// same JID is rejected with a positive retry-after.
func TestPerJIDRateLimiter_DeniesAtLimit(t *testing.T) {
	rl := NewPerJIDRateLimiter()
	jid := "5511999999999@s.whatsapp.net"
	for i := 0; i < 20; i++ {
		_, _ = rl.Check(jid)
	}
	ok, retry := rl.Check(jid)
	if ok {
		t.Fatalf("21st request should be denied")
	}
	if retry <= 0 {
		t.Fatalf("retry-after should be positive; got %v", retry)
	}
	if retry < time.Second {
		t.Fatalf("retry-after should be ≥ 1s; got %v", retry)
	}
}

// TestPerJIDRateLimiter_PerJIDIsolation ensures one JID's bucket doesn't
// affect another.
func TestPerJIDRateLimiter_PerJIDIsolation(t *testing.T) {
	rl := NewPerJIDRateLimiter()
	for i := 0; i < 20; i++ {
		_, _ = rl.Check("1111@s.whatsapp.net")
	}
	// 21st to JID-A is rejected
	if ok, _ := rl.Check("1111@s.whatsapp.net"); ok {
		t.Fatal("JID-A should be at limit")
	}
	// JID-B is fresh
	if ok, _ := rl.Check("2222@s.whatsapp.net"); !ok {
		t.Fatal("JID-B should be allowed")
	}
}

// TestPerJIDRateLimiter_WindowExpiry verifies that requests slide out of
// the window after the window duration.
func TestPerJIDRateLimiter_WindowExpiry(t *testing.T) {
	rl := &PerJIDRateLimiter{
		buckets: make(map[string]*bucket),
		max:     2,
		window:  100 * time.Millisecond,
	}
	jid := "test@s.whatsapp.net"
	if ok, _ := rl.Check(jid); !ok {
		t.Fatal("1st should be allowed")
	}
	if ok, _ := rl.Check(jid); !ok {
		t.Fatal("2nd should be allowed")
	}
	if ok, _ := rl.Check(jid); ok {
		t.Fatal("3rd should be denied (over limit)")
	}
	time.Sleep(150 * time.Millisecond)
	if ok, _ := rl.Check(jid); !ok {
		t.Fatal("after window expiry the bucket should drain")
	}
}
