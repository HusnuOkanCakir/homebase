package api

import (
	"testing"
	"time"
)

// ADR-0015. The limiter exists to bound how often an unauthenticated caller can
// make this server compute a 64 MiB hash, so what matters is that the ceiling
// really is a ceiling and that waiting really does lift it.

func TestRateLimiterAllowsABurstThenSlowsDown(t *testing.T) {
	now := time.Now()
	l := newRateLimiter(5, 10*time.Second)
	l.now = func() time.Time { return now }

	for i := range 5 {
		if ok, _ := l.allow("10.0.0.1"); !ok {
			t.Fatalf("attempt %d was refused inside the burst", i+1)
		}
	}

	ok, wait := l.allow("10.0.0.1")
	if ok {
		t.Fatal("the burst is not a limit if the sixth attempt goes through")
	}
	if wait <= 0 || wait > 10*time.Second {
		t.Errorf("wait of %v is not a useful thing to tell somebody", wait)
	}

	// Waiting earns exactly one attempt back, not the whole burst.
	now = now.Add(10 * time.Second)
	if ok, _ := l.allow("10.0.0.1"); !ok {
		t.Error("waiting the advertised time did not earn an attempt")
	}
	if ok, _ := l.allow("10.0.0.1"); ok {
		t.Error("one refill returned more than one attempt")
	}
}

// One noisy device must not lock everybody else out of their own server.
func TestRateLimiterIsPerAddress(t *testing.T) {
	now := time.Now()
	l := newRateLimiter(2, time.Minute)
	l.now = func() time.Time { return now }

	for range 3 {
		l.allow("10.0.0.1")
	}
	if ok, _ := l.allow("10.0.0.1"); ok {
		t.Fatal("the noisy address was not limited")
	}
	if ok, _ := l.allow("10.0.0.2"); !ok {
		t.Error("a different device was refused because of somebody else's attempts")
	}
}

// The bucket map must not be a way to make the server allocate for ever.
func TestRateLimiterForgetsIdleAddresses(t *testing.T) {
	now := time.Now()
	l := newRateLimiter(5, 10*time.Second)
	l.now = func() time.Time { return now }

	l.allow("10.0.0.1")
	l.allow("10.0.0.2")
	if len(l.buckets) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(l.buckets))
	}

	now = now.Add(authBucketIdle + time.Minute)
	l.allow("10.0.0.3")
	l.prune()

	if len(l.buckets) != 1 {
		t.Errorf("idle buckets were kept: %d remain", len(l.buckets))
	}
	if _, ok := l.buckets["10.0.0.3"]; !ok {
		t.Error("pruning removed an address that had just been seen")
	}
}

// Tokens accumulate, but never past the burst — otherwise a client that waits
// overnight arrives with thousands of free attempts.
func TestRateLimiterDoesNotBankAttempts(t *testing.T) {
	now := time.Now()
	l := newRateLimiter(5, 10*time.Second)
	l.now = func() time.Time { return now }

	l.allow("10.0.0.1")
	now = now.Add(24 * time.Hour)

	for i := range 5 {
		if ok, _ := l.allow("10.0.0.1"); !ok {
			t.Fatalf("attempt %d was refused after a long wait", i+1)
		}
	}
	if ok, _ := l.allow("10.0.0.1"); ok {
		t.Error("a long wait banked more attempts than the burst allows")
	}
}
