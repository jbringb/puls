package config

import (
	"os"
	"testing"
)

// allEnvKeys are every variable Load reads; we clear them before each case so a
// developer's shell environment can't leak into the test.
var allEnvKeys = []string{
	"PULS_HTTP_ADDR", "PULS_TLS_CERT", "PULS_TLS_KEY", "PULS_DB_PATH",
	"PULS_JWT_SECRET", "PULS_ADMIN_SECRET",
	"PULS_DEVICE_TOKEN_EXPIRY", "PULS_ADMIN_TOKEN_EXPIRY", "PULS_HEARTBEAT_TIMEOUT",
	"PULS_DIAGNOSTIC_TIMEOUT",
	"PULS_LOG_FORMAT", "PULS_LOG_LEVEL",
}

func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if old, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, old) })
		}
	}
}

func TestLoad(t *testing.T) {
	const validJWT = "this-is-a-signing-key-thirty-two+chars"
	const validAdmin = "admin-secret-16-chars"

	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{
			name: "valid",
			env:  map[string]string{"PULS_JWT_SECRET": validJWT, "PULS_ADMIN_SECRET": validAdmin},
		},
		{
			name:    "admin secret too short",
			env:     map[string]string{"PULS_JWT_SECRET": validJWT, "PULS_ADMIN_SECRET": "short"},
			wantErr: true,
		},
		{
			name:    "admin secret equals jwt secret",
			env:     map[string]string{"PULS_JWT_SECRET": validJWT, "PULS_ADMIN_SECRET": validJWT},
			wantErr: true,
		},
		{
			name:    "tls cert without key",
			env:     map[string]string{"PULS_JWT_SECRET": validJWT, "PULS_ADMIN_SECRET": validAdmin, "PULS_TLS_CERT": "cert.pem"},
			wantErr: true,
		},
		{
			name:    "invalid duration",
			env:     map[string]string{"PULS_JWT_SECRET": validJWT, "PULS_ADMIN_SECRET": validAdmin, "PULS_HEARTBEAT_TIMEOUT": "not-a-duration"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t, allEnvKeys...)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Load() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	clearEnv(t, allEnvKeys...)
	t.Setenv("PULS_JWT_SECRET", "this-is-a-signing-key-thirty-two+chars")
	t.Setenv("PULS_ADMIN_SECRET", "admin-secret-16-chars")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.DBPath != ":memory:" {
		t.Errorf("DBPath = %q, want :memory:", cfg.DBPath)
	}
	if cfg.AdminTokenExpiry.Hours() != 24 {
		t.Errorf("AdminTokenExpiry = %v, want 24h", cfg.AdminTokenExpiry)
	}
	if cfg.DiagnosticTimeout.Seconds() != 60 {
		t.Errorf("DiagnosticTimeout = %v, want 60s", cfg.DiagnosticTimeout)
	}
	if cfg.TLSEnabled() {
		t.Errorf("TLSEnabled() = true, want false")
	}
}
