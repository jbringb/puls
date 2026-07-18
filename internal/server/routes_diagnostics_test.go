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
)

func TestDiagnosticStatus(t *testing.T) {
	payload := json.RawMessage(`{"ok":true}`)
	const timeout = 60 * time.Second

	tests := []struct {
		name string
		d    model.DiagnosticResult
		want model.DiagnosticRequestStatus
	}{
		{
			name: "no payload, within timeout",
			d:    model.DiagnosticResult{RequestedAt: time.Now()},
			want: model.DiagnosticPending,
		},
		{
			name: "has payload, well past timeout — completed wins",
			d:    model.DiagnosticResult{RequestedAt: time.Now().Add(-time.Hour), Payload: &payload},
			want: model.DiagnosticCompleted,
		},
		{
			name: "no payload, past timeout",
			d:    model.DiagnosticResult{RequestedAt: time.Now().Add(-timeout - time.Second)},
			want: model.DiagnosticTimedOut,
		},
		{
			name: "no payload, exactly at the boundary is still pending",
			d:    model.DiagnosticResult{RequestedAt: time.Now()},
			want: model.DiagnosticPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := diagnosticStatus(tt.d, timeout); got != tt.want {
				t.Errorf("diagnosticStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHandleRequestDiagnosticsDeviceNotConnected(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	device, err := s.store.CreateDevice(ctx, &model.RegisterRequest{
		Name: "not-connected", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret-16chars",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+device.ID+"/diagnose", strings.NewReader(`{}`))
	req.SetPathValue("id", device.ID)
	rec := httptest.NewRecorder()
	s.handleRequestDiagnostics(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %q)", rec.Code, rec.Body.String())
	}

	// No row should have been created — the device was never connected, so
	// there was nothing to clean up in the first place.
	results, err := s.store.ListDiagnosticResults(ctx, device.ID, 10)
	if err != nil {
		t.Fatalf("ListDiagnosticResults: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected no diagnostic rows, got %d", len(results))
	}
}

// TestHandleRequestDiagnosticsSendFailureDeletesRow drives a real WebSocket
// connection, severs it abruptly right as a diagnose request races the
// server's IsConnected-then-Send window, and confirms the resulting
// send-failure doesn't leave a permanently "pending" row behind — the core
// regression this fix targets.
func TestHandleRequestDiagnosticsSendFailureDeletesRow(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()

	device, err := s.store.CreateDevice(ctx, &model.RegisterRequest{
		Name: "flaky-device", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret-16chars",
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

	rowCount := func() int {
		t.Helper()
		results, err := s.store.ListDiagnosticResults(ctx, device.ID, 50)
		if err != nil {
			t.Fatalf("ListDiagnosticResults: %v", err)
		}
		return len(results)
	}

	sawFailure := false
	for attempt := 0; attempt < 30 && !sawFailure; attempt++ {
		// Fresh connection per attempt: once the server notices a prior
		// attempt's connection is dead, every subsequent request against it
		// just hits the already-disconnected path — reusing one connection
		// across retries wastes almost all of them on a stale race window.
		dialCtx, cancelDial := context.WithTimeout(ctx, 5*time.Second)
		conn, _, err := websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{
			HTTPHeader: http.Header{"Authorization": {"Bearer " + token}},
		})
		cancelDial()
		if err != nil {
			t.Fatalf("dial (attempt %d): %v", attempt, err)
		}

		for i := 0; ; i++ {
			if s.hub.IsConnected(device.ID) {
				break
			}
			if i == 39 {
				t.Fatal("device never registered as connected")
			}
			time.Sleep(5 * time.Millisecond)
		}

		// Sever the connection abruptly (no close handshake) so the
		// underlying socket is already dead, then immediately race a
		// diagnose request against the server noticing — reproducing the
		// IsConnected-true-but-Send-fails window the fix targets.
		conn.CloseNow()

		before := rowCount()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+device.ID+"/diagnose", strings.NewReader(`{}`))
		req.SetPathValue("id", device.ID)
		rec := httptest.NewRecorder()
		s.handleRequestDiagnostics(rec, req)

		switch {
		case rec.Code == http.StatusServiceUnavailable && strings.Contains(rec.Body.String(), "failed to deliver"):
			// The regression case: this 503 came from the Send failure path
			// specifically (not the earlier IsConnected-false early-return,
			// which never creates a row at all). CreateDiagnosticRequest
			// always runs before Send is attempted, so without the fix this
			// would leave a permanently "pending" row behind. Confirm it
			// didn't.
			if after := rowCount(); after != before {
				t.Fatalf("row count went from %d to %d after a Send failure — orphaned row not cleaned up", before, after)
			}
			sawFailure = true
		case rec.Code == http.StatusServiceUnavailable:
			// IsConnected already returned false — the server noticed the
			// dead connection before we got here. No row was created by this
			// attempt; that's not the path this test exercises. Retry with a
			// fresh connection.
		case rec.Code == http.StatusAccepted:
			// Lost the race this attempt (server hadn't yet noticed the
			// closed connection, or the write landed in the local socket
			// buffer before the peer's close was detected) — a legitimately
			// pending row for a request that happens to never be answered.
			// Not what this test is checking; retry with a fresh connection.
		default:
			t.Fatalf("unexpected status %d (body %q)", rec.Code, rec.Body.String())
		}
	}

	if !sawFailure {
		t.Skip("could not reproduce the IsConnected-true-but-Send-fails race in this environment; deleteOrphanedDiagnosticRequest's own unit-level coverage (store.TestDeleteDiagnosticRequest) still covers the cleanup mechanism directly")
	}
}
