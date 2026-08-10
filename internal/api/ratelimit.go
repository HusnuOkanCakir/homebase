package api

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Rate limiting for the endpoints anybody can reach without signing in.
//
// ADR-0015. Sign in, first-run setup and recovery all verify an argon2id hash,
// which costs 64 MiB and a measurable amount of CPU on purpose. That makes them
// the only expensive thing on the server an unauthenticated caller can trigger
// at will, so the limiter sits in front of the hash rather than behind it — the
// cost being defended against is the hash itself.
//
// It also slows down guessing. That matters less than it sounds for the
// recovery code, which has 125 bits behind it, and rather more for a password
// somebody chose themselves.

const (
	// A burst covers the ordinary case: somebody mistyping a password a few
	// times, or copying a recovery code off paper and getting a character
	// wrong. Nobody legitimate exceeds this in a hurry.
	authBurst = 5

	// After the burst, one attempt every ten seconds. Six a minute is
	// unremarkable to a person and useless to a script.
	authRefill = 10 * time.Second

	// Buckets idle for this long are forgotten, so the map cannot grow without
	// bound on a machine somebody is spraying with requests from spoofed
	// addresses.
	authBucketIdle = 30 * time.Minute
)

type bucket struct {
	tokens float64
	seen   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	refill  time.Duration
	now     func() time.Time
}

func newRateLimiter(burst int, refill time.Duration) *rateLimiter {
	return &rateLimiter{
		buckets: make(map[string]*bucket),
		burst:   float64(burst),
		refill:  refill,
		now:     time.Now,
	}
}

// allow reports whether this key may make an attempt now, and how long to wait
// if not.
func (l *rateLimiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, seen: now}
		l.buckets[key] = b
	}

	b.tokens += now.Sub(b.seen).Seconds() / l.refill.Seconds()
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.seen = now

	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) * float64(l.refill))
		return false, wait
	}

	b.tokens--
	return true, 0
}

// refund returns a token to a key.
//
// Used when an attempt succeeded. Rationing correct sign-ins would punish the
// household rather than the attacker: somebody moving between rooms, or a
// browser reconnecting, is not what this defends against. Only attempts that
// failed leave a mark.
func (l *rateLimiter) refund(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok {
		return
	}
	if b.tokens += 1; b.tokens > l.burst {
		b.tokens = l.burst
	}
}

// prune forgets buckets nobody has used recently.
func (l *rateLimiter) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().Add(-authBucketIdle)
	for key, b := range l.buckets {
		if b.seen.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
}

// clientKey identifies the caller for limiting purposes.
//
// Deliberately the transport address and never X-Forwarded-For or any other
// header: those are supplied by the client, and a limiter keyed on something an
// attacker chooses is a limiter an attacker turns off. Homebase is reached
// directly on the local network, so there is no proxy whose word would be worth
// trusting anyway.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// MaintainRateLimits forgets idle buckets until the context is cancelled.
func (s *Server) MaintainRateLimits(ctx context.Context) {
	ticker := time.NewTicker(authBucketIdle)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.authLimit.prune()
		}
	}
}

// limited wraps a handler so failed attempts from one address are rationed.
func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := clientKey(r)

		ok, wait := s.authLimit.allow(key)
		if !ok {
			seconds := int(wait.Seconds()) + 1
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
			s.writeError(w, r, http.StatusTooManyRequests, apiError{
				Code:        "auth.too_many_attempts",
				Message:     "Too many attempts. Please wait a moment and try again.",
				Detail:      "try again in about " + strconv.Itoa(seconds) + " seconds",
				Recoverable: true,
				Recovery:    "Wait about " + strconv.Itoa(seconds) + " seconds, then try again.",
			})
			return
		}

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next(recorder, r)

		if recorder.status < http.StatusBadRequest {
			s.authLimit.refund(key)
		}
	}
}

// statusRecorder remembers what a handler answered, so the limiter can tell an
// attempt that failed from one that worked.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
