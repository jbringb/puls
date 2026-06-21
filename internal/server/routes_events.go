package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	// This is a long-lived stream; clear the server's WriteTimeout for this
	// connection so the deadline doesn't terminate an idle-but-healthy stream.
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsub := s.broadcaster.Subscribe()
	defer unsub()

	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			_ = rc.Flush()
		}
	}
}
