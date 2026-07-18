package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// keyedRateLimiter is a token-bucket rate limiter shared across arbitrary keys —
// client IP for HTTP endpoints, device ID for WebSocket messages. Each key gets
// its own bucket that refills at `rate` tokens/second up to `burst`. Buckets are
// created lazily and idle ones are swept periodically so the map can't grow
// unbounded.
type keyedRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	rate      float64 // tokens per second
	burst     float64 // bucket capacity
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

const (
	rateLimitSweepInterval = time.Minute
	rateLimitBucketTTL     = 10 * time.Minute
)

func newRateLimiter(ratePerSec float64, burst int) *keyedRateLimiter {
	return &keyedRateLimiter{
		buckets:   make(map[string]*bucket),
		rate:      ratePerSec,
		burst:     float64(burst),
		lastSweep: time.Now(),
	}
}

func (l *keyedRateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}

	// Lazily refill based on elapsed time, capped at burst.
	b.tokens += now.Sub(b.last).Seconds() * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweep evicts buckets that have been idle longer than the TTL. Must be called
// while holding l.mu.
func (l *keyedRateLimiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < rateLimitSweepInterval {
		return
	}
	for k, b := range l.buckets {
		if now.Sub(b.last) > rateLimitBucketTTL {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}

// rateLimit rejects HTTP requests from a client IP that has exhausted its bucket.
func rateLimit(l *keyedRateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the remote IP. It deliberately does not trust X-Forwarded-For,
// which is client-spoofable unless terminated behind a known proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
