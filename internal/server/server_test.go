package server

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jbringb/puls/internal/auth"
	"github.com/jbringb/puls/internal/config"
	"github.com/jbringb/puls/internal/observability"
	"github.com/jbringb/puls/internal/store"
	"github.com/jbringb/puls/internal/ws"
)

const (
	testJWTSecret   = "test-signing-secret-at-least-32-chars!"
	testAdminSecret = "test-admin-secret-16+"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	st, err := store.NewSQLite(db)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}

	jwtMgr, err := auth.NewManager(testJWTSecret)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	cfg := &config.Config{
		HTTPAddr:          ":0",
		JWTSecret:         testJWTSecret,
		AdminSecret:       testAdminSecret,
		DeviceTokenExpiry: 90 * 24 * time.Hour,
		AdminTokenExpiry:  24 * time.Hour,
		HeartbeatTimeout:  90 * time.Second,
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := ws.NewHub(logger)
	srv, err := New(cfg, st, hub, jwtMgr, logger)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func TestRequireAuth(t *testing.T) {
	s := newTestServer(t)
	deviceTok, _ := s.jwtMgr.IssueDeviceToken("device-1", time.Hour)
	adminTok, _ := s.jwtMgr.IssueAdminToken("admin", time.Hour)

	// A trivial handler guarded by the admin-role middleware.
	guarded := requireAuth(s.jwtMgr, auth.RoleAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"missing bearer prefix", "Token " + adminTok, http.StatusUnauthorized},
		{"garbage token", "Bearer not-a-jwt", http.StatusUnauthorized},
		{"device token lacks admin role", "Bearer " + deviceTok, http.StatusForbidden},
		{"valid admin token", "Bearer " + adminTok, http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			guarded.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

// TestWrapMiddlewareRecordsRealStatusAfterPanic exercises the exact
// production middleware ordering (via wrapMiddleware, not a hand-rolled
// substitute) against a panicking handler. recoveryMiddleware must sit
// inside metrics.Middleware/loggingMiddleware so a panic's real 500 status
// is what gets recorded and logged — not the 200 zero-value the response
// writer had before the panic unwound.
func TestWrapMiddlewareRecordsRealStatusAfterPanic(t *testing.T) {
	m, err := observability.NewMetrics(func() int { return 0 })
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	handler := wrapMiddleware(panicking, m, logger)

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("client-facing status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	metricsRec := httptest.NewRecorder()
	m.HTTPHandler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsRec.Body.String()

	if !strings.Contains(body, `status="500"`) {
		t.Errorf("expected a status=\"500\" sample in metrics output after a panic, got:\n%s", body)
	}
	if strings.Contains(body, `status="200"`) {
		t.Errorf("found an incorrect status=\"200\" sample for a panicking request in:\n%s", body)
	}
}

// TestWrapMiddlewareLogsRealStatusAfterPanic covers the other consumer that
// used to be broken the same way: loggingMiddleware's "http" access-log line
// isn't behind a defer, so a panic previously unwound straight past it and
// the line was silently never written at all for a panicking request. With
// recoveryMiddleware innermost, logging's next.ServeHTTP call now returns
// normally (recovery already handled the panic), so the line fires — and
// must show the real 500, not a stale 200.
func TestWrapMiddlewareLogsRealStatusAfterPanic(t *testing.T) {
	m, err := observability.NewMetrics(func() int { return 0 })
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))

	panicking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	handler := wrapMiddleware(panicking, m, logger)
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "msg=http") {
		t.Fatalf("expected an 'http' access-log line for the panicking request, got:\n%s", logOutput)
	}
	// Isolate just the access-log line — the panic-recovery error log line
	// (logged separately by recoveryMiddleware) legitimately contains other
	// text and shouldn't be mistaken for it.
	for _, line := range strings.Split(strings.TrimSpace(logOutput), "\n") {
		if !strings.Contains(line, "msg=http") {
			continue
		}
		if !strings.Contains(line, "status=500") {
			t.Errorf("http access-log line does not show status=500:\n%s", line)
		}
		if strings.Contains(line, "status=200") {
			t.Errorf("http access-log line incorrectly shows status=200 for a panic:\n%s", line)
		}
	}
}
