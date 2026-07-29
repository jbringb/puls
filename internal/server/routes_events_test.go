package server

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withShortSSETimers overrides the package-level SSE timing vars for the
// duration of a test, so tests don't have to wait out real 10-15s timers.
// Safe because this package's tests never run in parallel with each other.
func withShortSSETimers(t *testing.T, write, keepalive time.Duration) {
	t.Helper()
	origWrite, origKeepalive := sseWriteTimeout, sseKeepaliveInterval
	sseWriteTimeout, sseKeepaliveInterval = write, keepalive
	t.Cleanup(func() { sseWriteTimeout, sseKeepaliveInterval = origWrite, origKeepalive })
}

func TestHandleEventsDeliversPublishedEvent(t *testing.T) {
	withShortSSETimers(t, 200*time.Millisecond, 20*time.Millisecond)
	s := newTestServer(t)
	ts := httptest.NewServer(http.HandlerFunc(s.handleEvents))
	defer ts.Close()

	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	br := bufio.NewReader(resp.Body)

	// Each SSE message is a content line followed by a blank terminator
	// line; skip the blank ones so callers only see lines worth asserting on.
	nextLine := func() string {
		t.Helper()
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read line: %v", err)
			}
			if line == "\n" {
				continue
			}
			return line
		}
	}

	// The keepalive interval is far shorter than any delay below, so the
	// first line is guaranteed to be a keepalive — which also proves the
	// handler has already subscribed by the time we publish.
	if first := nextLine(); !strings.HasPrefix(first, ":") {
		t.Fatalf("first line = %q, want a keepalive comment", first)
	}

	s.broadcaster.Publish(Event{Type: "device.online", Payload: map[string]string{"id": "abc"}})

	for {
		line := nextLine()
		if strings.HasPrefix(line, ":") {
			continue // another keepalive raced the publish
		}
		if !strings.HasPrefix(line, "data: ") {
			t.Fatalf("line = %q, want a data: line", line)
		}
		if !strings.Contains(line, `"device.online"`) {
			t.Errorf("event line missing expected type: %s", line)
		}
		break
	}
}

// TestHandleEventsStuckWriteIsCleanedUp covers the resource leak this fix
// closes: a client that stops reading — without closing the connection —
// must not be allowed to block the handler's write forever. It dials raw so
// it can read the response headers and then simply stop, leaving a real
// stuck write for sseWriteTimeout to detect, and asserts the Broadcaster
// subscription this handler holds is released once it does.
func TestHandleEventsStuckWriteIsCleanedUp(t *testing.T) {
	// Keepalive must stay on (and short) or nothing ever triggers a first
	// write, headers never flush, and the status-line read below blocks
	// forever before we even get to publish. Its tiny lines add negligible
	// bytes next to the 4MB publish below, so it doesn't threaten the test.
	withShortSSETimers(t, 150*time.Millisecond, 20*time.Millisecond)

	s := newTestServer(t)
	ts := httptest.NewServer(http.HandlerFunc(s.handleEvents))
	defer ts.Close()

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: puls-test\r\n\r\n")); err != nil {
		t.Fatalf("send request: %v", err)
	}

	// Read only the status line, then stop reading entirely — without
	// closing the connection — so nothing ever drains the socket and the
	// server's next big write has nowhere to go once kernel buffers fill.
	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read status line: %v", err)
	}

	if s.broadcaster.Count() == 0 {
		t.Fatal("handler hadn't subscribed yet — test setup race")
	}

	// Large enough to exceed default OS socket buffers many times over, so
	// the write can't complete while nothing is reading it.
	huge := strings.Repeat("x", 4<<20) // 4MB
	s.broadcaster.Publish(Event{Type: "big", Payload: huge})

	deadline := time.After(5 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if s.broadcaster.Count() == 0 {
			return // handler noticed the stuck write and cleaned up
		}
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("broadcaster subscription was never cleaned up — stuck write was not detected")
		}
	}
}
