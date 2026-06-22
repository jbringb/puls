package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/jbringb/puls/internal/model"
	ws "github.com/jbringb/puls/internal/ws"
)

// wsBearerSubprotocol is the sentinel a browser client offers alongside its
// token: `Sec-WebSocket-Protocol: puls.bearer, <jwt>`. Only the sentinel is
// echoed back, so the token never lands in a response header or proxy log.
const wsBearerSubprotocol = "puls.bearer"

// wsToken extracts the device token, preferring mechanisms that keep it out of
// URLs and access logs:
//  1. Authorization: Bearer <token>                 — non-browser clients
//  2. Sec-WebSocket-Protocol: puls.bearer, <token>  — browsers can't set Authorization
//  3. ?token=<token>                                — fallback; leaks into logs/history, discouraged
func wsToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if tok := subprotocolToken(r); tok != "" {
		return tok
	}
	return r.URL.Query().Get("token")
}

func subprotocolToken(r *http.Request) string {
	var protocols []string
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			protocols = append(protocols, strings.TrimSpace(p))
		}
	}
	for i, p := range protocols {
		if p == wsBearerSubprotocol && i+1 < len(protocols) {
			return protocols[i+1]
		}
	}
	return ""
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	tokenStr := wsToken(r)
	if tokenStr == "" {
		writeError(w, http.StatusUnauthorized, "missing token")
		return
	}

	claims, err := s.jwtMgr.Validate(tokenStr)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	if claims.Role != "device" {
		writeError(w, http.StatusForbidden, "device token required")
		return
	}

	deviceID := claims.Subject

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Empty OriginPatterns keeps the library's same-origin default; operators
		// can widen it via PULS_ALLOWED_ORIGINS. Subprotocols lets browsers pass
		// the token via Sec-WebSocket-Protocol (see wsToken).
		OriginPatterns: s.cfg.AllowedOrigins,
		Subprotocols:   []string{wsBearerSubprotocol},
	})
	if err != nil {
		s.logger.Error("websocket accept", "err", err, "device_id", deviceID)
		return
	}

	client := ws.NewClient(
		deviceID,
		conn,
		s.hub,
		s.store,
		s.logger,
		s.cfg.HeartbeatTimeout,
		s.handleWSMessage,
	)

	ctx := r.Context()
	if err := s.store.SetDeviceStatus(ctx, deviceID, model.StatusOnline); err != nil {
		s.logger.Warn("set device online", "device_id", deviceID, "err", err)
	}
	s.broadcaster.Publish(Event{Type: "device.connected", Payload: map[string]string{"id": deviceID}})

	client.Run(ctx)

	s.broadcaster.Publish(Event{Type: "device.disconnected", Payload: map[string]string{"id": deviceID}})
}

func (s *Server) handleWSMessage(ctx context.Context, c *ws.Client, env ws.Envelope) error {
	switch env.Type {
	case ws.TypeHeartbeat:
		return s.handleHeartbeat(ctx, c, env)
	case ws.TypeDiagResponse:
		return s.handleDiagResponse(ctx, c, env)
	default:
		s.logger.Warn("unknown ws message type", "device_id", c.DeviceID, "type", env.Type)
	}
	return nil
}

func (s *Server) handleHeartbeat(ctx context.Context, c *ws.Client, env ws.Envelope) error {
	var data ws.HeartbeatData
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return err
	}

	hb := &model.Heartbeat{
		DeviceID:      c.DeviceID,
		CPUPercent:    data.CPUPercent,
		MemoryPercent: data.MemoryPercent,
		DiskPercent:   data.DiskPercent,
		UptimeSeconds: data.UptimeSeconds,
		OSVersion:     data.OSVersion,
	}

	if err := s.store.InsertHeartbeat(ctx, hb); err != nil {
		s.logger.Error("insert heartbeat", "device_id", c.DeviceID, "err", err)
		return err
	}
	s.metrics.IncHeartbeat()

	if err := s.store.UpdateLastSeen(ctx, c.DeviceID); err != nil {
		s.logger.Warn("update last seen", "device_id", c.DeviceID, "err", err)
	}

	s.broadcaster.Publish(Event{Type: "device.heartbeat", Payload: map[string]any{
		"id":            c.DeviceID,
		"cpuPercent":    data.CPUPercent,
		"memoryPercent": data.MemoryPercent,
		"diskPercent":   data.DiskPercent,
	}})

	s.logger.Debug("heartbeat received",
		"device_id", c.DeviceID,
		"cpu", data.CPUPercent,
		"mem", data.MemoryPercent,
		"disk", data.DiskPercent,
	)
	return nil
}

func (s *Server) handleDiagResponse(ctx context.Context, c *ws.Client, env ws.Envelope) error {
	if env.RequestID == "" {
		s.logger.Warn("diag_response missing request_id", "device_id", c.DeviceID)
		return nil
	}

	if err := s.store.SaveDiagnosticResult(ctx, env.RequestID, env.Data); err != nil {
		s.logger.Error("save diagnostic result", "request_id", env.RequestID, "err", err)
		return err
	}

	s.logger.Info("diagnostic result received",
		"device_id", c.DeviceID,
		slog.String("request_id", env.RequestID),
	)
	return nil
}
