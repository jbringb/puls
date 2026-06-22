package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "ready") {
		t.Errorf("readyz body = %q, want 'ready'", body)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	s := newTestServer(t)

	// Fire a request through the server so the counter has at least one entry.
	pingReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.http.Handler.ServeHTTP(httptest.NewRecorder(), pingReq)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "puls_http_requests_total") {
		t.Errorf("/metrics missing puls_http_requests_total:\n%s", body)
	}
}

func TestMetricsRouteLabel(t *testing.T) {
	s := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	s.http.Handler.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)

	// r.Pattern should be set by ServeMux, giving a labelled route rather than "unmatched"
	if strings.Contains(string(body), `route="unmatched"`) {
		t.Errorf("route label fell back to 'unmatched'; r.Pattern not propagated:\n%s", body)
	}
	if !strings.Contains(string(body), `route="GET /healthz"`) {
		t.Errorf("expected route=\"GET /healthz\" in metrics output:\n%s", body)
	}
}
