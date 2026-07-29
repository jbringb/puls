package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// sseWriteTimeout and sseKeepaliveInterval are var, not const, solely so
// tests can shorten them instead of waiting out real 10-15s timers; neither
// is meant to be tuned at runtime.
var (
	// sseWriteTimeout bounds a single write to an SSE client. Without it, a
	// client that stops reading — network drop, laptop sleep, anything short
	// of a clean TCP close — leaves the write blocked on a full send buffer
	// indefinitely. r.Context() only cancels once the server notices the
	// connection is gone, which a stuck write prevents it from ever doing —
	// so the goroutine, and its Broadcaster subscription, leak for the life
	// of the process.
	sseWriteTimeout = 10 * time.Second

	// sseKeepaliveInterval bounds how long a dead-but-idle connection can go
	// undetected: with no event to send, no write is ever attempted, so
	// sseWriteTimeout never gets a chance to fire. A periodic comment line
	// (SSE clients ignore lines starting with ':') forces a write often
	// enough to catch that case, and incidentally keeps intermediate proxies
	// from treating the connection as idle and closing it themselves.
	sseKeepaliveInterval = 15 * time.Second
)

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	// This is a long-lived stream; clear the server's blanket WriteTimeout
	// for this connection so it doesn't terminate an idle-but-healthy
	// stream. write below sets its own bounded deadline around each
	// individual write instead.
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.broadcaster.Subscribe()
	defer unsub()

	write := func(line string) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		if _, err := fmt.Fprint(w, line); err != nil {
			return false
		}
		return rc.Flush() == nil
	}

	keepalive := time.NewTicker(sseKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if !write(": keepalive\n\n") {
				return
			}
		case e := <-ch:
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if !write(fmt.Sprintf("data: %s\n\n", data)) {
				return
			}
			keepalive.Reset(sseKeepaliveInterval)
		}
	}
}
