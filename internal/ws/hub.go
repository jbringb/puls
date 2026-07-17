package ws

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client
	logger  *slog.Logger
	// wg tracks every Client's Run goroutine from NewClient until Run returns
	// (after its unregister + offline-status cleanup completes), so Wait can
	// tell shutdown when that cleanup has actually finished.
	wg sync.WaitGroup
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		logger:  logger,
	}
}

// Register adds c as the active connection for its device, replacing and
// closing any prior connection for the same device. The replaced
// connection's own Run goroutine notices the close, and — because Unregister
// checks it's still the map's current entry before deleting — correctly
// skips writing an offline status for a device that's actually still online
// via c.
func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	existing, hadExisting := h.clients[c.DeviceID]
	h.clients[c.DeviceID] = c
	total := len(h.clients)
	h.mu.Unlock()

	// Close outside the lock: the underlying WebSocket close handshake can
	// block for several seconds waiting on a possibly-dead peer, and holding
	// h.mu across that would stall every other connection's Send/Unregister.
	if hadExisting {
		h.logger.Info("replacing existing connection", "device_id", c.DeviceID)
		existing.Close()
	}
	h.logger.Info("device connected", "device_id", c.DeviceID, "total", total)
}

// Unregister removes c if it is still the registered connection for its
// device, and reports whether it did. A connection that's already been
// superseded by Register (e.g. the device reconnected before this one
// noticed its own connection was closed) gets false here — its caller uses
// that to avoid marking a device offline that a newer connection has already
// marked online.
func (h *Hub) Unregister(c *Client) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c.DeviceID] == c {
		delete(h.clients, c.DeviceID)
		h.logger.Info("device disconnected", "device_id", c.DeviceID, "total", len(h.clients))
		return true
	}
	return false
}

func (h *Hub) Send(ctx context.Context, deviceID string, msg []byte) error {
	h.mu.RLock()
	c, ok := h.clients[deviceID]
	h.mu.RUnlock()

	if !ok {
		return fmt.Errorf("hub: device %s not connected", deviceID)
	}
	return c.send(ctx, msg)
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (h *Hub) IsConnected(deviceID string) bool {
	h.mu.RLock()
	_, ok := h.clients[deviceID]
	h.mu.RUnlock()
	return ok
}

func (h *Hub) ConnectedDeviceIDs() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.clients))
	for id := range h.clients {
		ids = append(ids, id)
	}
	return ids
}

// CloseAll signals every connected client to close. It deliberately leaves
// map removal to each client's own Run goroutine (via Unregister, same as a
// normal disconnect) rather than clearing h.clients itself — if it removed
// entries up front, each Run goroutine's later Unregister(c) would find
// itself already gone and (correctly, per that guard) skip writing the
// device offline, so shutdown would never mark anyone offline. It does not
// wait for those Run goroutines to finish — call Wait for that.
func (h *Hub) CloseAll() {
	h.mu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	// Close concurrently and outside the lock: each Close can block for
	// several seconds on the WebSocket close handshake, and neither should
	// happen while holding h.mu (see Register) nor serialize behind one
	// another — a slow/dead peer shouldn't extend every other client's
	// shutdown.
	var closing sync.WaitGroup
	for _, c := range clients {
		closing.Add(1)
		go func(c *Client) {
			defer closing.Done()
			c.Close()
		}(c)
	}
	closing.Wait()
}

// Wait blocks until every Client registered so far has finished its Run
// goroutine — including the unregister and offline-status write that happen
// after its connection closes — or until ctx is done, whichever comes first.
func (h *Hub) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
