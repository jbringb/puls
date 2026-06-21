package server

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jbringb/puls/internal/auth"
	"github.com/jbringb/puls/internal/config"
	"github.com/jbringb/puls/internal/store"
	"github.com/jbringb/puls/internal/ws"
)

//go:embed openapi.json
var openapiSpec []byte

type Server struct {
	cfg         *config.Config
	store       store.Store
	hub         *ws.Hub
	jwtMgr      *auth.Manager
	logger      *slog.Logger
	http        *http.Server
	broadcaster *Broadcaster
}

func New(cfg *config.Config, st store.Store, hub *ws.Hub, jwtMgr *auth.Manager, logger *slog.Logger) *Server {
	s := &Server{
		cfg:         cfg,
		store:       st,
		hub:         hub,
		jwtMgr:      jwtMgr,
		logger:      logger,
		broadcaster: NewBroadcaster(),
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	var handler http.Handler = mux
	handler = maxBytesMiddleware(handler)
	handler = loggingMiddleware(logger, handler)
	handler = recoveryMiddleware(logger, handler)

	s.http = &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: handler,
		// Bound how long a client may dawdle, to blunt Slowloris-style attacks.
		// WriteTimeout is safe for the streaming endpoints: the WebSocket handler
		// hijacks the connection and the SSE handler clears its write deadline.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	adminAuth := requireAuth(s.jwtMgr, auth.RoleAdmin)

	// The two unauthenticated endpoints are throttled per client IP: admin-token
	// gates a brute-forceable secret and register runs bcrypt on every call.
	publicLimiter := newIPRateLimiter(1, 5) // ~1 req/s, burst of 5
	mux.Handle("POST /api/v1/auth/admin-token", rateLimit(publicLimiter, http.HandlerFunc(s.handleAdminToken)))
	mux.Handle("POST /api/v1/devices/register", rateLimit(publicLimiter, http.HandlerFunc(s.handleRegister)))

	mux.Handle("GET /api/v1/devices", adminAuth(http.HandlerFunc(s.handleListDevices)))
	mux.Handle("GET /api/v1/devices/{id}", adminAuth(http.HandlerFunc(s.handleGetDevice)))

	mux.Handle("POST /api/v1/devices/{id}/diagnose", adminAuth(http.HandlerFunc(s.handleRequestDiagnostics)))
	mux.Handle("GET /api/v1/devices/{id}/diagnostics", adminAuth(http.HandlerFunc(s.handleListDiagnostics)))

	mux.HandleFunc("GET /api/v1/ws", s.handleWebSocket)
	mux.Handle("GET /api/v1/events", adminAuth(http.HandlerFunc(s.handleEvents)))

	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(openapiSpec)
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
}

func (s *Server) Start() error {
	if s.cfg.TLSEnabled() {
		s.logger.Info("starting HTTPS server", "addr", s.cfg.HTTPAddr)
		if err := s.http.ListenAndServeTLS(s.cfg.TLSCertFile, s.cfg.TLSKeyFile); err != http.ErrServerClosed {
			return fmt.Errorf("server: %w", err)
		}
		return nil
	}
	s.logger.Info("starting HTTP server", "addr", s.cfg.HTTPAddr)
	if err := s.http.ListenAndServe(); err != http.ErrServerClosed {
		return fmt.Errorf("server: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.logger.Info("shutting down server")
	s.hub.CloseAll()
	return s.http.Shutdown(ctx)
}
