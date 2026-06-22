package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr    string // PULS_HTTP_ADDR, default ":8080"
	TLSCertFile string // PULS_TLS_CERT
	TLSKeyFile  string // PULS_TLS_KEY

	// DBPath is the SQLite database path.
	// Use ":memory:" (default) for an in-memory database or a file path for persistence.
	DBPath string // PULS_DB_PATH, default ":memory:"

	JWTSecret         string        // PULS_JWT_SECRET   — HMAC signing key for tokens
	AdminSecret       string        // PULS_ADMIN_SECRET — password presented to mint an admin token
	DeviceTokenExpiry time.Duration // PULS_DEVICE_TOKEN_EXPIRY, default 90d
	AdminTokenExpiry  time.Duration // PULS_ADMIN_TOKEN_EXPIRY,  default 24h

	HeartbeatTimeout time.Duration // PULS_HEARTBEAT_TIMEOUT, default 90s

	// AllowedOrigins restricts browser WebSocket upgrades. Empty (default) means
	// same-origin only; non-browser clients send no Origin and are unaffected.
	AllowedOrigins []string // PULS_ALLOWED_ORIGINS, comma-separated

	LogFormat string // PULS_LOG_FORMAT: "json" | "text", default "json"
	LogLevel  string // PULS_LOG_LEVEL:  "debug" | "info" | "warn" | "error", default "info"

	// OTelEndpoint enables OTLP/HTTP tracing when set. Accepts host:port (e.g.
	// "localhost:4318", uses HTTP) or a full URL (e.g. "https://collector:4318",
	// scheme determines TLS). Also honoured via OTEL_EXPORTER_OTLP_ENDPOINT.
	OTelEndpoint string // PULS_OTEL_ENDPOINT
}

func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:       env("PULS_HTTP_ADDR", ":8080"),
		TLSCertFile:    env("PULS_TLS_CERT", ""),
		TLSKeyFile:     env("PULS_TLS_KEY", ""),
		DBPath:         env("PULS_DB_PATH", ":memory:"),
		JWTSecret:      env("PULS_JWT_SECRET", ""),
		AdminSecret:    env("PULS_ADMIN_SECRET", ""),
		LogFormat:      env("PULS_LOG_FORMAT", "json"),
		LogLevel:       env("PULS_LOG_LEVEL", "info"),
		AllowedOrigins: splitAndTrim(env("PULS_ALLOWED_ORIGINS", "")),
		OTelEndpoint:   env("PULS_OTEL_ENDPOINT", env("OTEL_EXPORTER_OTLP_ENDPOINT", "")),
	}

	var err error

	c.DeviceTokenExpiry, err = envDuration("PULS_DEVICE_TOKEN_EXPIRY", 90*24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: PULS_DEVICE_TOKEN_EXPIRY: %w", err)
	}

	c.AdminTokenExpiry, err = envDuration("PULS_ADMIN_TOKEN_EXPIRY", 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("config: PULS_ADMIN_TOKEN_EXPIRY: %w", err)
	}

	c.HeartbeatTimeout, err = envDuration("PULS_HEARTBEAT_TIMEOUT", 90*time.Second)
	if err != nil {
		return nil, fmt.Errorf("config: PULS_HEARTBEAT_TIMEOUT: %w", err)
	}

	if err := c.validate(); err != nil {
		return nil, err
	}

	return c, nil
}

func (c *Config) validate() error {
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("config: PULS_TLS_CERT and PULS_TLS_KEY must both be set or both be empty")
	}
	if len(c.AdminSecret) < 16 {
		return fmt.Errorf("config: PULS_ADMIN_SECRET must be at least 16 characters")
	}
	if c.AdminSecret == c.JWTSecret {
		return fmt.Errorf("config: PULS_ADMIN_SECRET must not equal PULS_JWT_SECRET")
	}
	return nil
}

func (c *Config) TLSEnabled() bool { return c.TLSCertFile != "" }

// splitAndTrim splits a comma-separated value into trimmed, non-empty entries.
// Returns nil for an empty input.
func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", v, err)
	}
	return d, nil
}
