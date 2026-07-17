package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/jbringb/puls/internal/model"
	"github.com/jbringb/puls/internal/store"
)

type HandlerFunc func(ctx context.Context, c *Client, env Envelope) error

type Client struct {
	DeviceID string

	conn             *websocket.Conn
	hub              *Hub
	store            store.Store
	logger           *slog.Logger
	heartbeatTimeout time.Duration
	onMessage        HandlerFunc
}

func NewClient(
	deviceID string,
	conn *websocket.Conn,
	hub *Hub,
	s store.Store,
	logger *slog.Logger,
	heartbeatTimeout time.Duration,
	onMessage HandlerFunc,
) *Client {
	c := &Client{
		DeviceID:         deviceID,
		conn:             conn,
		hub:              hub,
		store:            s,
		logger:           logger,
		heartbeatTimeout: heartbeatTimeout,
		onMessage:        onMessage,
	}
	hub.wg.Add(1)
	hub.Register(c)
	return c
}

func (c *Client) Run(ctx context.Context) {
	// Declared first so it runs last (defers are LIFO): Wait must not
	// observe this client as "done" until the unregister + offline-status
	// write below has actually completed.
	defer c.hub.wg.Done()
	defer func() {
		// Only the client that's still the hub's active entry for this
		// device gets to mark it offline — a connection superseded by a
		// newer one for the same device (Hub.Register's replace path) must
		// not clobber the newer connection's online status.
		if !c.hub.Unregister(c) {
			return
		}
		offlineCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.store.SetDeviceStatus(offlineCtx, c.DeviceID, model.StatusOffline); err != nil {
			c.logger.Warn("failed to set device offline", "device_id", c.DeviceID, "err", err)
		}
	}()

	for {
		// Reset deadline on every read to enforce the heartbeat timeout.
		readCtx, cancel := context.WithTimeout(ctx, c.heartbeatTimeout)
		_, raw, err := c.conn.Read(readCtx)
		cancel()

		if err != nil {
			c.logger.Info("connection closed", "device_id", c.DeviceID, "err", err)
			return
		}

		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			c.logger.Warn("malformed message", "device_id", c.DeviceID, "err", err)
			errMsg, _ := EncodeError("", "malformed message")
			_ = c.send(ctx, errMsg)
			continue
		}

		if err := c.onMessage(ctx, c, env); err != nil {
			c.logger.Warn("message handler error", "device_id", c.DeviceID, "type", env.Type, "err", err)
		}
	}
}

func (c *Client) send(ctx context.Context, msg []byte) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.conn.Write(writeCtx, websocket.MessageText, msg); err != nil {
		return fmt.Errorf("ws send: %w", err)
	}
	return nil
}

func (c *Client) Close() {
	_ = c.conn.Close(websocket.StatusGoingAway, "server shutdown")
}
