package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jbringb/puls/internal/model"
	"github.com/jbringb/puls/internal/ws"
)

func TestWSToken(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(r *http.Request)
		wantToken string
	}{
		{
			name:      "authorization header preferred",
			setup:     func(r *http.Request) { r.Header.Set("Authorization", "Bearer abc.def") },
			wantToken: "abc.def",
		},
		{
			name:      "subprotocol sentinel",
			setup:     func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", "puls.bearer, jwt.token.here") },
			wantToken: "jwt.token.here",
		},
		{
			name:      "query fallback",
			setup:     func(r *http.Request) { r.URL.RawQuery = "token=from.query" },
			wantToken: "from.query",
		},
		{
			name: "header wins over query",
			setup: func(r *http.Request) {
				r.Header.Set("Authorization", "Bearer from.header")
				r.URL.RawQuery = "token=from.query"
			},
			wantToken: "from.header",
		},
		{
			name:      "subprotocol without trailing token",
			setup:     func(r *http.Request) { r.Header.Set("Sec-WebSocket-Protocol", "puls.bearer") },
			wantToken: "",
		},
		{
			name:      "no token",
			setup:     func(r *http.Request) {},
			wantToken: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/ws", nil)
			tt.setup(req)
			if got := wsToken(req); got != tt.wantToken {
				t.Fatalf("wsToken() = %q, want %q", got, tt.wantToken)
			}
		})
	}
}

// TestWSMessageRateLimit drives a real WebSocket connection through the full
// handler stack and confirms a device flooding messages past its per-device
// bucket (burst 20, see server.go) gets rejected with a rate-limit error
// envelope, and that the rejection is counted in metrics.
func TestWSMessageRateLimit(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	device, err := s.store.CreateDevice(ctx, &model.RegisterRequest{
		Name: "rl-device", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret-16chars",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	token, err := s.jwtMgr.IssueDeviceToken(device.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	httpSrv := httptest.NewServer(s.http.Handler)
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws"

	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// Burst is 20; send well past it in a tight loop so the server's
	// sequential read loop drains its bucket before any tokens refill.
	const sent = 30
	for i := 0; i < sent; i++ {
		msg, err := ws.Encode(ws.TypeHeartbeat, "", ws.HeartbeatData{CPUPercent: 1})
		if err != nil {
			t.Fatalf("encode heartbeat: %v", err)
		}
		writeCtx, cancelWrite := context.WithTimeout(ctx, 2*time.Second)
		err = conn.Write(writeCtx, websocket.MessageText, msg)
		cancelWrite()
		if err != nil {
			t.Fatalf("write heartbeat %d: %v", i, err)
		}
	}

	// Only rejected messages get a reply (accepted heartbeats aren't acked),
	// so drain responses until the rate-limit error envelope appears.
	readCtx, cancelRead := context.WithTimeout(ctx, 5*time.Second)
	defer cancelRead()

	var gotRejection bool
	for !gotRejection {
		_, raw, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("read (waiting for rate-limit rejection): %v", err)
		}
		var env ws.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		if env.Type != ws.TypeError {
			continue
		}
		var data ws.ErrorData
		_ = json.Unmarshal(env.Data, &data)
		if data.Message == "rate limit exceeded" {
			gotRejection = true
		}
	}

	rec := httptest.NewRecorder()
	s.metrics.HTTPHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "puls_ws_messages_rejected_total") {
		t.Error("expected puls_ws_messages_rejected_total in /metrics output")
	}
}

// TestWSReconnectDoesNotLeaveDeviceStuckOffline drives two real WebSocket
// connections for the same device through the full handler stack. It used to
// be possible for the first connection's delayed cleanup (after being
// replaced by the second) to write "offline" after the second connection had
// already written "online", leaving the device stuck offline despite an
// active connection — see Hub.Unregister's replaced-connection guard.
func TestWSReconnectDoesNotLeaveDeviceStuckOffline(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	device, err := s.store.CreateDevice(ctx, &model.RegisterRequest{
		Name: "reconnect-device", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret-16chars",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	token, err := s.jwtMgr.IssueDeviceToken(device.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	httpSrv := httptest.NewServer(s.http.Handler)
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws"

	dial := func() *websocket.Conn {
		t.Helper()
		dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return conn
	}
	sendHeartbeat := func(conn *websocket.Conn) {
		t.Helper()
		msg, err := ws.Encode(ws.TypeHeartbeat, "", ws.HeartbeatData{CPUPercent: 1})
		if err != nil {
			t.Fatalf("encode heartbeat: %v", err)
		}
		writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := conn.Write(writeCtx, websocket.MessageText, msg); err != nil {
			t.Fatalf("write heartbeat: %v", err)
		}
	}
	waitOnline := func() {
		t.Helper()
		for i := 0; i < 40; i++ {
			d, err := s.store.GetDevice(ctx, device.ID)
			if err == nil && d.Status == model.StatusOnline {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatal("device never reached online status")
	}

	connA := dial()
	defer connA.CloseNow()
	sendHeartbeat(connA)
	waitOnline()

	// Reconnect as the same device without explicitly closing connA first —
	// this triggers Hub.Register's replace-and-close path on the server side
	// for connA, which is exactly the scenario that used to race.
	connB := dial()
	defer connB.CloseNow()
	sendHeartbeat(connB)
	waitOnline()

	// connA's superseded Run() goroutine's cleanup can land at any point from
	// here on. With the bug, its offline write could land after connB's
	// online write, above. If status ever flips to offline while connB is
	// still the active connection, the fix failed.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		d, err := s.store.GetDevice(ctx, device.ID)
		if err != nil {
			t.Fatalf("GetDevice: %v", err)
		}
		if d.Status != model.StatusOnline {
			t.Fatalf("device status = %q while connB is still connected, want online (connA's stale cleanup likely clobbered it)", d.Status)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestShutdownWaitsForDeviceCleanup asserts that by the time Server.Shutdown
// returns, a connected device's offline-status write has actually landed —
// not just been signaled. There's deliberately no polling here: Shutdown
// itself must be the thing that blocks until cleanup is done.
func TestShutdownWaitsForDeviceCleanup(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	device, err := s.store.CreateDevice(ctx, &model.RegisterRequest{
		Name: "shutdown-device", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret-16chars",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	token, err := s.jwtMgr.IssueDeviceToken(device.ID, time.Hour)
	if err != nil {
		t.Fatalf("IssueDeviceToken: %v", err)
	}

	httpSrv := httptest.NewServer(s.http.Handler)
	defer httpSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http") + "/api/v1/ws"

	dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
	defer cancelDial()
	conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()

	// A real device (e.g. puls-agent) has a background read loop; that's
	// what lets the server's graceful Close() complete its close handshake
	// in milliseconds instead of blocking on its ~5s wait-for-peer timeout.
	// Simulate that here so this test measures Shutdown's own behavior, not
	// an idle test connection's inability to participate in the handshake.
	go func() {
		for {
			if _, _, err := conn.Read(context.Background()); err != nil {
				return
			}
		}
	}()

	var lastStatus model.DeviceStatus
	for i := 0; i < 40; i++ {
		d, err := s.store.GetDevice(ctx, device.ID)
		if err != nil {
			t.Fatalf("GetDevice: %v", err)
		}
		lastStatus = d.Status
		if lastStatus == model.StatusOnline {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if lastStatus != model.StatusOnline {
		t.Fatalf("device never reached online status (last seen: %q)", lastStatus)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, 5*time.Second)
	defer cancelShutdown()
	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	d, err := s.store.GetDevice(ctx, device.ID)
	if err != nil {
		t.Fatalf("GetDevice after Shutdown: %v", err)
	}
	if d.Status != model.StatusOffline {
		t.Fatalf("device status immediately after Shutdown() returned = %q, want offline", d.Status)
	}
}
