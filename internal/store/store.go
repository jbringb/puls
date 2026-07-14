package store

import (
	"context"
	"errors"

	"github.com/jbringb/puls/internal/model"
)

var ErrNotFound = errors.New("store: not found")

// ErrInvalidCursor is returned by ListDevices when the caller-supplied cursor
// is malformed — callers should surface this as a 400, not a 500.
var ErrInvalidCursor = errors.New("store: invalid cursor")

type Store interface {
	// Ping verifies the store is reachable; used by the readiness endpoint.
	Ping(ctx context.Context) error

	CreateDevice(ctx context.Context, req *model.RegisterRequest) (*model.Device, error)
	GetDevice(ctx context.Context, id string) (*model.Device, error)
	// ListDevices returns up to limit devices ordered by registration time,
	// newest first. Pass the empty string as cursor for the first page; a
	// non-empty nextCursor return value means more pages remain.
	ListDevices(ctx context.Context, limit int, cursor string) (devices []model.Device, nextCursor string, err error)
	SetDeviceStatus(ctx context.Context, id string, status model.DeviceStatus) error
	UpdateLastSeen(ctx context.Context, id string) error

	InsertHeartbeat(ctx context.Context, hb *model.Heartbeat) error
	ListHeartbeats(ctx context.Context, deviceID string, limit int) ([]model.Heartbeat, error)

	CreateDiagnosticRequest(ctx context.Context, deviceID, requestID string, scope model.DiagnosticScope) (*model.DiagnosticResult, error)
	SaveDiagnosticResult(ctx context.Context, requestID string, payload []byte) error
	ListDiagnosticResults(ctx context.Context, deviceID string, limit int) ([]model.DiagnosticResult, error)
	GetDiagnosticResult(ctx context.Context, requestID string) (*model.DiagnosticResult, error)
}
