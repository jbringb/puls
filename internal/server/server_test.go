package server

import (
	"database/sql"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jbringb/puls/internal/auth"
	"github.com/jbringb/puls/internal/config"
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
	return New(cfg, st, hub, jwtMgr, logger)
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
