package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/jbringb/puls/internal/model"
)

//go:embed schema.sql
var schema string

type rfc3339Time time.Time

func (t *rfc3339Time) Scan(src any) error {
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("rfc3339Time: expected string, got %T", src)
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("rfc3339Time: %w", err)
	}
	*t = rfc3339Time(parsed)
	return nil
}

type nullRFC3339Time struct{ V *time.Time }

func (t *nullRFC3339Time) Scan(src any) error {
	if src == nil {
		return nil
	}
	s, ok := src.(string)
	if !ok {
		return fmt.Errorf("nullRFC3339Time: expected string or nil, got %T", src)
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fmt.Errorf("nullRFC3339Time: %w", err)
	}
	t.V = &parsed
	return nil
}

type nullJSON struct{ V *json.RawMessage }

func (n *nullJSON) Scan(src any) error {
	if src == nil {
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("nullJSON: unexpected type %T", src)
	}
	if s == "" {
		return nil
	}
	rm := json.RawMessage(s)
	n.V = &rm
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func collect[T any](rows *sql.Rows, fn func(rowScanner) (*T, error)) ([]T, error) {
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := fn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *v)
	}
	return out, rows.Err()
}

type SQLite struct {
	db *sql.DB
}

func NewSQLite(db *sql.DB) (*SQLite, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("store: init schema: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (s *SQLite) CreateDevice(ctx context.Context, req *model.RegisterRequest) (*model.Device, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Secret), 12)
	if err != nil {
		return nil, fmt.Errorf("store: hash secret: %w", err)
	}

	id, now := uuid.New().String(), time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO devices (id, name, os, arch, secret_hash, registered_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, req.Name, req.OS, req.Arch, hash, now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("store: create device: %w", err)
	}

	return &model.Device{
		ID: id, Name: req.Name, OS: model.DeviceOS(req.OS), Arch: req.Arch,
		Status: model.StatusUnknown, RegisteredAt: now,
	}, nil
}

func (s *SQLite) GetDevice(ctx context.Context, id string) (*model.Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, os, arch, status, registered_at, last_seen_at FROM devices WHERE id = ?`, id)
	d, err := scanDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *SQLite) ListDevices(ctx context.Context) ([]model.Device, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, os, arch, status, registered_at, last_seen_at FROM devices ORDER BY registered_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list devices: %w", err)
	}
	return collect(rows, scanDevice)
}

func (s *SQLite) SetDeviceStatus(ctx context.Context, id string, status model.DeviceStatus) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("store: set device status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) UpdateLastSeen(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = ?, status = 'online' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (s *SQLite) InsertHeartbeat(ctx context.Context, hb *model.Heartbeat) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO heartbeats (device_id, received_at, cpu_percent, memory_percent, disk_percent, uptime_seconds, os_version)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hb.DeviceID, time.Now().UTC().Format(time.RFC3339),
		hb.CPUPercent, hb.MemoryPercent, hb.DiskPercent, hb.UptimeSeconds, hb.OSVersion,
	)
	if err != nil {
		return err
	}
	return nil
}

func (s *SQLite) ListHeartbeats(ctx context.Context, deviceID string, limit int) ([]model.Heartbeat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, device_id, received_at, cpu_percent, memory_percent, disk_percent, uptime_seconds, os_version
		 FROM heartbeats WHERE device_id = ? ORDER BY received_at DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list heartbeats: %w", err)
	}
	return collect(rows, scanHeartbeat)
}

func (s *SQLite) CreateDiagnosticRequest(ctx context.Context, deviceID, requestID string, scope model.DiagnosticScope) (*model.DiagnosticResult, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO diagnostic_results (device_id, request_id, scope, requested_at) VALUES (?, ?, ?, ?)`,
		deviceID, requestID, scope, now.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("store: create diagnostic request: %w", err)
	}
	return &model.DiagnosticResult{DeviceID: deviceID, RequestID: requestID, Scope: scope, RequestedAt: now}, nil
}

func (s *SQLite) SaveDiagnosticResult(ctx context.Context, requestID string, payload []byte) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_results SET received_at = ?, payload = ? WHERE request_id = ?`,
		time.Now().UTC().Format(time.RFC3339), string(payload), requestID,
	)
	if err != nil {
		return fmt.Errorf("store: save diagnostic result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *SQLite) ListDiagnosticResults(ctx context.Context, deviceID string, limit int) ([]model.DiagnosticResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, device_id, request_id, scope, requested_at, received_at, payload
		 FROM diagnostic_results WHERE device_id = ? ORDER BY requested_at DESC LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list diagnostics: %w", err)
	}
	return collect(rows, scanDiagnostic)
}

func (s *SQLite) GetDiagnosticResult(ctx context.Context, requestID string) (*model.DiagnosticResult, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, device_id, request_id, scope, requested_at, received_at, payload
		 FROM diagnostic_results WHERE request_id = ?`, requestID)
	d, err := scanDiagnostic(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func scanDevice(s rowScanner) (*model.Device, error) {
	var d model.Device
	var reg rfc3339Time
	var seen nullRFC3339Time
	if err := s.Scan(&d.ID, &d.Name, &d.OS, &d.Arch, &d.Status, &reg, &seen); err != nil {
		return nil, fmt.Errorf("store: scan device: %w", err)
	}
	d.RegisteredAt = time.Time(reg)
	d.LastSeenAt = seen.V
	return &d, nil
}

func scanHeartbeat(s rowScanner) (*model.Heartbeat, error) {
	var hb model.Heartbeat
	var received rfc3339Time
	if err := s.Scan(&hb.ID, &hb.DeviceID, &received,
		&hb.CPUPercent, &hb.MemoryPercent, &hb.DiskPercent,
		&hb.UptimeSeconds, &hb.OSVersion); err != nil {
		return nil, fmt.Errorf("store: scan heartbeat: %w", err)
	}
	hb.ReceivedAt = time.Time(received)
	return &hb, nil
}

func scanDiagnostic(s rowScanner) (*model.DiagnosticResult, error) {
	var d model.DiagnosticResult
	var requested rfc3339Time
	var received nullRFC3339Time
	var payload nullJSON
	if err := s.Scan(&d.ID, &d.DeviceID, &d.RequestID, &d.Scope,
		&requested, &received, &payload); err != nil {
		return nil, fmt.Errorf("store: scan diagnostic: %w", err)
	}
	d.RequestedAt = time.Time(requested)
	d.ReceivedAt = received.V
	d.Payload = payload.V
	return &d, nil
}
