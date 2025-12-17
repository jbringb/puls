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
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		logger:  logger,
	}
}

func (h *Hub) Register(c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if existing, ok := h.clients[c.DeviceID]; ok {
		h.logger.Info("replacing existing connection", "device_id", c.DeviceID)
		existing.Close()
	}

	h.clients[c.DeviceID] = c
	h.logger.Info("device connected", "device_id", c.DeviceID, "total", len(h.clients))
}

func (h *Hub) Unregister(deviceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, deviceID)
	h.logger.Info("device disconnected", "device_id", deviceID, "total", len(h.clients))
}

func (h *Hub) Send(ctx context.Context, deviceID string, msg []byte) error {
	h.mu.RLock()
	defer h.mu.RUnlock()

	c, ok := h.clients[deviceID]
	if !ok {
		return fmt.Errorf("hub: device %s not connected", deviceID)
	}
	return c.send(ctx, msg)
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

func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, c := range h.clients {
		c.Close()
		delete(h.clients, id)
	}
}

