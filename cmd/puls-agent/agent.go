package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/jbringb/puls/internal/model"
	"github.com/jbringb/puls/internal/ws"
)

type Config struct {
	ServerURL string
	Name      string
	OS        string
	Arch      string
	Secret    string
	Interval  time.Duration
	StateFile string
	Insecure  bool
}

type Agent struct {
	cfg    Config
	logger *slog.Logger
	http   *http.Client
}

func NewAgent(cfg Config, logger *slog.Logger) (*Agent, error) {
	cfg.ServerURL = strings.TrimSuffix(cfg.ServerURL, "/")
	if cfg.ServerURL == "" {
		return nil, errors.New("server URL is required")
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("heartbeat interval must be positive, got %s", cfg.Interval)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	if cfg.Insecure {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // opt-in dev flag
	}

	return &Agent{cfg: cfg, logger: logger, http: client}, nil
}

// Run registers (or reuses a saved registration), then connects and stays
// connected until ctx is canceled, reconnecting with backoff on drops.
func (a *Agent) Run(ctx context.Context) error {
	st, err := a.ensureRegistered(ctx)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	// A connection that stayed up for a while was healthy; don't let one old
	// failure keep inflating the delay for unrelated future blips.
	const backoffResetAfter = 2 * maxBackoff

	for {
		connectedAt := time.Now()
		err := a.connectAndServe(ctx, st.Token)
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(connectedAt) > backoffResetAfter {
			backoff = time.Second
		}

		if errors.Is(err, errUnauthorized) {
			a.logger.Warn("device token rejected, re-registering")
			if clearErr := clearState(a.cfg.StateFile); clearErr != nil {
				a.logger.Warn("clear state", "err", clearErr)
			}
			st, err = a.ensureRegistered(ctx)
			if err != nil {
				return fmt.Errorf("re-register: %w", err)
			}
		} else {
			a.logger.Warn("connection lost, reconnecting", "err", err, "in", backoff)
		}

		// Always wait before retrying, even after a successful re-registration —
		// a server that keeps rejecting freshly issued tokens must not be
		// hammered with an unthrottled register-dial-401 loop.
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}
}

// ensureRegistered loads a saved device registration or creates a new one.
func (a *Agent) ensureRegistered(ctx context.Context) (*state, error) {
	if st, err := loadState(a.cfg.StateFile); err != nil {
		a.logger.Warn("load state", "err", err)
	} else if st != nil {
		a.logger.Info("reusing saved registration", "device_id", st.DeviceID)
		return st, nil
	}

	if a.cfg.Secret == "" {
		return nil, errors.New("no saved registration found; -secret (or PULS_AGENT_SECRET) is required for first-time registration")
	}

	device, token, err := a.register(ctx)
	if err != nil {
		return nil, err
	}

	st := &state{DeviceID: device.ID, Token: token}
	if err := saveState(a.cfg.StateFile, st); err != nil {
		a.logger.Warn("save state", "err", err)
	}
	a.logger.Info("registered new device", "device_id", device.ID, "name", device.Name, "state_file", a.cfg.StateFile)
	return st, nil
}

func (a *Agent) register(ctx context.Context) (*model.Device, string, error) {
	body, err := json.Marshal(model.RegisterRequest{
		Name:   a.cfg.Name,
		OS:     model.DeviceOS(a.cfg.OS),
		Arch:   a.cfg.Arch,
		Secret: a.cfg.Secret,
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode request: %w", err)
	}

	url := a.cfg.ServerURL + "/api/v1/devices/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var apiErr struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&apiErr)
		return nil, "", fmt.Errorf("server returned %d: %s", resp.StatusCode, apiErr.Error)
	}

	var out model.RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("decode response: %w", err)
	}

	return &model.Device{ID: out.DeviceID, Name: a.cfg.Name, OS: model.DeviceOS(a.cfg.OS), Arch: a.cfg.Arch}, out.Token, nil
}

var errUnauthorized = errors.New("device token rejected by server")

// connectAndServe opens the WebSocket connection and blocks, sending
// heartbeats and answering diagnostic requests until the connection drops
// or ctx is canceled.
func (a *Agent) connectAndServe(ctx context.Context, token string) error {
	wsURL, err := toWebSocketURL(a.cfg.ServerURL)
	if err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(dialCtx, wsURL+"/api/v1/ws", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		HTTPClient: a.http,
	})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return errUnauthorized
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow() //nolint:errcheck

	a.logger.Info("connected", "server", a.cfg.ServerURL)

	msgCtx, cancelRead := context.WithCancel(ctx)
	defer cancelRead()

	incoming := make(chan ws.Envelope)
	readErrCh := make(chan error, 1)
	go a.readLoop(msgCtx, conn, incoming, readErrCh)

	ticker := time.NewTicker(a.cfg.Interval)
	defer ticker.Stop()

	// Send an immediate heartbeat so the device shows up online right away
	// instead of waiting a full interval.
	if err := a.sendHeartbeat(ctx, conn); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "shutting down")
			return nil

		case err := <-readErrCh:
			return err

		case env := <-incoming:
			a.handleMessage(ctx, conn, env)

		case <-ticker.C:
			if err := a.sendHeartbeat(ctx, conn); err != nil {
				return err
			}
		}
	}
}

func (a *Agent) readLoop(ctx context.Context, conn *websocket.Conn, out chan<- ws.Envelope, errCh chan<- error) {
	for {
		_, raw, err := conn.Read(ctx)
		if err != nil {
			errCh <- err
			return
		}
		var env ws.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			a.logger.Warn("malformed message from server", "err", err)
			continue
		}
		select {
		case out <- env:
		case <-ctx.Done():
			return
		}
	}
}

func (a *Agent) sendHeartbeat(ctx context.Context, conn *websocket.Conn) error {
	data := heartbeatData(ctx)
	msg, err := ws.Encode(ws.TypeHeartbeat, "", data)
	if err != nil {
		return fmt.Errorf("encode heartbeat: %w", err)
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("write heartbeat: %w", err)
	}
	a.logger.Debug("heartbeat sent", "cpu", data.CPUPercent, "mem", data.MemoryPercent, "disk", data.DiskPercent)
	return nil
}

func (a *Agent) handleMessage(ctx context.Context, conn *websocket.Conn, env ws.Envelope) {
	switch env.Type {
	case ws.TypeDiagRequest:
		// Gathering diagnostics (e.g. walking every process) can take a while;
		// run it off the connectAndServe select loop so it can't delay the
		// heartbeat ticker or the next incoming message. Conn.Write is safe
		// for concurrent use (only Read/Reader isn't), so this is safe
		// alongside sendHeartbeat's writes.
		go a.handleDiagRequest(ctx, conn, env)
	case ws.TypeError:
		var data ws.ErrorData
		_ = json.Unmarshal(env.Data, &data)
		a.logger.Warn("server sent error", "message", data.Message)
	default:
		a.logger.Debug("unhandled message type", "type", env.Type)
	}
}

func (a *Agent) handleDiagRequest(ctx context.Context, conn *websocket.Conn, env ws.Envelope) {
	var req ws.DiagRequestData
	if err := json.Unmarshal(env.Data, &req); err != nil {
		a.logger.Warn("malformed diag_request", "err", err)
		return
	}

	a.logger.Info("diagnostic request received", "request_id", env.RequestID, "scope", req.Scope)
	payload := diagnosticPayload(ctx, model.DiagnosticScope(req.Scope))

	msg, err := ws.Encode(ws.TypeDiagResponse, env.RequestID, payload)
	if err != nil {
		a.logger.Error("encode diag_response", "err", err)
		return
	}

	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := conn.Write(writeCtx, websocket.MessageText, msg); err != nil {
		a.logger.Error("send diag_response", "err", err)
	}
}

func toWebSocketURL(serverURL string) (string, error) {
	switch {
	case strings.HasPrefix(serverURL, "https://"):
		return "wss://" + strings.TrimPrefix(serverURL, "https://"), nil
	case strings.HasPrefix(serverURL, "http://"):
		return "ws://" + strings.TrimPrefix(serverURL, "http://"), nil
	default:
		return "", fmt.Errorf("server URL must start with http:// or https://: %q", serverURL)
	}
}
