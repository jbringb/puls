// Command puls-agent is a reference device client for Puls. It registers
// (or reuses a saved registration), connects over WebSocket, reports real
// host stats as periodic heartbeats, and answers on-demand diagnostic
// requests — everything a real monitored device is expected to do.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"
)

func main() {
	var (
		serverURL  = flag.String("server", envOr("PULS_AGENT_SERVER", "http://localhost:8080"), "Puls server base URL")
		name       = flag.String("name", envOr("PULS_AGENT_NAME", hostnameOrDefault()), "device name to register")
		secret     = flag.String("secret", os.Getenv("PULS_AGENT_SECRET"), "registration secret, min 16 chars (required on first run)")
		osOverride = flag.String("os", envOr("PULS_AGENT_OS", ""), `device OS reported to the server: "windows" or "linux" (default: autodetected; non-Windows platforms report as "linux")`)
		interval   = flag.Duration("interval", envDurationOr("PULS_AGENT_INTERVAL", 20*time.Second), "heartbeat interval")
		stateFile  = flag.String("state-file", envOr("PULS_AGENT_STATE_FILE", defaultStateFile()), "path to save the device ID/token so restarts reuse the same device")
		insecure   = flag.Bool("insecure", envBoolOr("PULS_AGENT_INSECURE", false), "skip TLS certificate verification (self-signed dev servers only)")
		logFormat  = flag.String("log-format", envOr("PULS_AGENT_LOG_FORMAT", "text"), `log output format: "text" or "json"`)
	)
	flag.Parse()

	logger := buildLogger(*logFormat)

	cfg := Config{
		ServerURL: *serverURL,
		Name:      *name,
		OS:        resolveOS(*osOverride),
		Arch:      runtime.GOARCH,
		Secret:    *secret,
		Interval:  *interval,
		StateFile: *stateFile,
		Insecure:  *insecure,
	}

	agent, err := NewAgent(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "puls-agent: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := agent.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "puls-agent: %v\n", err)
		os.Exit(1)
	}
}

func buildLogger(format string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		handler = slog.NewTextHandler(os.Stderr, opts)
	}
	return slog.New(handler)
}

func hostnameOrDefault() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "puls-agent"
	}
	return h
}

// resolveOS maps the host platform to one of the two values the server
// accepts. Puls only models "windows" and "linux" devices, so anything else
// (macOS, BSD, ...) is reported as "linux" for demo purposes unless the
// caller overrides it explicitly with -os.
func resolveOS(override string) string {
	if override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		return "windows"
	}
	return "linux"
}

func defaultStateFile() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ".puls-agent-state.json"
	}
	return dir + string(os.PathSeparator) + "puls-agent" + string(os.PathSeparator) + "state.json"
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDurationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func envBoolOr(key string, fallback bool) bool {
	switch os.Getenv(key) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	default:
		return fallback
	}
}
