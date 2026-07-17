package ws

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func testHub() *Hub {
	return NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestUnregisterOnlyRemovesCurrentClient is the core regression test for the
// device-stuck-offline bug: a connection that has already been superseded by
// a newer one for the same device must not be able to remove (or, via its
// caller, mark offline) the newer connection's registration.
func TestUnregisterOnlyRemovesCurrentClient(t *testing.T) {
	h := testHub()
	c1 := &Client{DeviceID: "d1"}
	c2 := &Client{DeviceID: "d1"}

	h.mu.Lock()
	h.clients["d1"] = c1
	h.mu.Unlock()

	if h.Unregister(c2) {
		t.Fatal("Unregister(c2) = true, want false — c2 was never the registered client")
	}
	if h.clients["d1"] != c1 {
		t.Fatal("Unregister(c2) incorrectly removed c1's registration")
	}

	if !h.Unregister(c1) {
		t.Fatal("Unregister(c1) = false, want true — c1 was the registered client")
	}
	if _, ok := h.clients["d1"]; ok {
		t.Fatal("expected d1 to be removed after Unregister(c1)")
	}
}

func TestWaitReturnsOnceAllClientsDone(t *testing.T) {
	h := testHub()
	h.wg.Add(1)

	done := make(chan error, 1)
	go func() { done <- h.Wait(context.Background()) }()

	select {
	case <-done:
		t.Fatal("Wait returned before wg.Done was called")
	case <-time.After(50 * time.Millisecond):
	}

	h.wg.Done()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after wg.Done")
	}
}

func TestWaitRespectsContextTimeout(t *testing.T) {
	h := testHub()
	h.wg.Add(1) // never Done — simulates a client whose cleanup hangs
	defer h.wg.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := h.Wait(ctx); err == nil {
		t.Fatal("Wait() = nil, want context deadline error")
	}
}
