package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/jbringb/puls/internal/auth"
	"github.com/jbringb/puls/internal/config"
	"github.com/jbringb/puls/internal/observability"
	"github.com/jbringb/puls/internal/server"
	"github.com/jbringb/puls/internal/store"
	"github.com/jbringb/puls/internal/ws"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "puls-server: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := buildLogger(cfg)
	ctx := context.Background()

	tracingShutdown, err := observability.SetupTracing(ctx, cfg.OTelEndpoint)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}

	st, err := initStore(ctx, cfg, logger)
	if err != nil {
		return err
	}

	jwtMgr, err := auth.NewManager(cfg.JWTSecret)
	if err != nil {
		return fmt.Errorf("init jwt manager: %w", err)
	}

	hub := ws.NewHub(logger)
	srv, err := server.New(cfg, st, hub, jwtMgr, logger)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	select {
	case err := <-errCh:
		return err
	case sig := <-quit:
		logger.Info("signal received, shutting down", "signal", sig)
		shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		return tracingShutdown(shutdownCtx)
	}
}

func initStore(ctx context.Context, cfg *config.Config, logger *slog.Logger) (store.Store, error) {
	if cfg.DatabaseURL != "" {
		db, err := sql.Open("pgx", cfg.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		db.SetMaxOpenConns(15)
		db.SetMaxIdleConns(5)
		db.SetConnMaxLifetime(5 * time.Minute)

		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := db.PingContext(pingCtx); err != nil {
			return nil, fmt.Errorf("ping postgres: %w", err)
		}
		logger.Info("database ready", "driver", "postgres")
		return store.NewPostgres(db)
	}

	db, err := sql.Open("sqlite", cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite supports only one concurrent writer; a single connection avoids lock contention.
	db.SetMaxOpenConns(1)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	logger.Info("database ready", "driver", "sqlite", "path", cfg.DBPath)
	return store.NewSQLite(db)
}

func buildLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
