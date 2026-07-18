package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIPRateLimiterBurstThenDeny(t *testing.T) {
	// Very slow refill so the burst is effectively all we get within the test.
	l := newRateLimiter(0.0001, 3)

	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d within burst should be allowed", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("request beyond burst should be denied")
	}
}

func TestIPRateLimiterPerKey(t *testing.T) {
	l := newRateLimiter(0.0001, 1)

	if !l.allow("1.1.1.1") {
		t.Fatal("first key should be allowed")
	}
	if !l.allow("2.2.2.2") {
		t.Fatal("a different key has its own bucket and should be allowed")
	}
	if l.allow("1.1.1.1") {
		t.Fatal("first key is now exhausted")
	}
}

func TestIPRateLimiterRefills(t *testing.T) {
	l := newRateLimiter(1000, 1) // 1000 tokens/sec refills quickly
	if !l.allow("9.9.9.9") {
		t.Fatal("first request allowed")
	}
	if l.allow("9.9.9.9") {
		t.Fatal("immediate second request denied")
	}
	time.Sleep(5 * time.Millisecond) // ~5 tokens refill
	if !l.allow("9.9.9.9") {
		t.Fatal("request after refill should be allowed")
	}
}

func TestAdminTokenEndpointIsRateLimited(t *testing.T) {
	s := newTestServer(t)

	// Drive requests through the full handler chain (mux + middleware), which is
	// where the per-IP limiter is wired. httptest gives every request the same
	// RemoteAddr, so they share a bucket.
	var got429 bool
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/admin-token", strings.NewReader(`{"secret":"wrong"}`))
		rec := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a 429 once the per-IP burst was exhausted")
	}
}

func TestServerTimeoutsConfigured(t *testing.T) {
	s := newTestServer(t)
	if s.http.ReadHeaderTimeout == 0 {
		t.Error("ReadHeaderTimeout not set")
	}
	if s.http.ReadTimeout == 0 {
		t.Error("ReadTimeout not set")
	}
	if s.http.IdleTimeout == 0 {
		t.Error("IdleTimeout not set")
	}
}
