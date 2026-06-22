package store

import (
	"context"
	"errors"

	"github.com/jbringb/puls/internal/model"
)

var ErrNotFound = errors.New("store: not found")

type Store interface {
	// Ping verifies the store is reachable; used by the readiness endpoint.
	Ping(ctx context.Context) error

	CreateDevice(ctx context.Context, req *model.RegisterRequest) (*model.Device, error)
	GetDevice(ctx context.Context, id string) (*model.Device, error)
	ListDevices(ctx context.Context) ([]model.Device, error)
	SetDeviceStatus(ctx context.Context, id string, status model.DeviceStatus) error
	UpdateLastSeen(ctx context.Context, id string) error

	InsertHeartbeat(ctx context.Context, hb *model.Heartbeat) error
	ListHeartbeats(ctx context.Context, deviceID string, limit int) ([]model.Heartbeat, error)

	CreateDiagnosticRequest(ctx context.Context, deviceID, requestID string, scope model.DiagnosticScope) (*model.DiagnosticResult, error)
	SaveDiagnosticResult(ctx context.Context, requestID string, payload []byte) error
	ListDiagnosticResults(ctx context.Context, deviceID string, limit int) ([]model.DiagnosticResult, error)
	GetDiagnosticResult(ctx context.Context, requestID string) (*model.DiagnosticResult, error)
}
