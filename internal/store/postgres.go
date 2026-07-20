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
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"

	"github.com/jbringb/puls/internal/model"
)

//go:embed schema_postgres.sql
var schemaPostgresV1 string

//go:embed schema_postgres_v2.sql
var schemaPostgresV2 string

// pgMigrations are applied in version order; each is tracked in puls_schema_version.
// Never edit a released migration — append a new one instead.
var pgMigrations = []struct {
	version    int
	stmts      string
	concurrent bool // true if stmts must run outside a transaction block (e.g. CREATE INDEX CONCURRENTLY)
}{
	{version: 1, stmts: schemaPostgresV1},
	{version: 2, stmts: schemaPostgresV2, concurrent: true},
}

// pulsMigrationLockID is a stable Postgres advisory lock key used to serialise
// concurrent startup across multiple instances. "puls" encoded as int64.
const pulsMigrationLockID = int64(0x70756c73)

// Postgres implements Store against a PostgreSQL database.
type Postgres struct {
	db *sql.DB
}

// NewPostgres applies pending migrations and returns a ready Postgres store.
func NewPostgres(db *sql.DB) (*Postgres, error) {
	s := &Postgres{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// migrate serialises concurrent startup with a Postgres session advisory lock,
// creates the version-tracking table if absent, then applies any unapplied
// migrations each in its own transaction.
func (s *Postgres) migrate(ctx context.Context) error {
	// Pin all migration work to one connection so the advisory lock, DDL, and
	// version tracking share the same session.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: get migration conn: %w", err)
	}
	defer conn.Close()

	// Block until no other instance is migrating. Session advisory locks are
	// scoped to the connection's lifetime, but we unlock explicitly below
	// (defers run LIFO, so unlock fires before conn.Close returns it to the pool).
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, pulsMigrationLockID); err != nil {
		return fmt.Errorf("store: acquire migration lock: %w", err)
	}
	// Use Background so a cancelled ctx doesn't leave the lock held.
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, pulsMigrationLockID)
	}()

	if _, err := conn.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS puls_schema_version (version INT PRIMARY KEY)`,
	); err != nil {
		return fmt.Errorf("store: create version table: %w", err)
	}

	var current int
	if err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM puls_schema_version`,
	).Scan(&current); err != nil {
		return fmt.Errorf("store: read schema version: %w", err)
	}

	for _, m := range pgMigrations {
		if m.version <= current {
			continue
		}
		if m.concurrent {
			if err := applyConcurrentMigration(ctx, conn, m.version, m.stmts); err != nil {
				return err
			}
			continue
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: begin migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx, m.stmts); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: apply migration %d: %w", m.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO puls_schema_version (version) VALUES ($1)`, m.version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: record version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("store: commit migration %d: %w", m.version, err)
		}
	}
	return nil
}

// applyConcurrentMigration runs stmts outside a transaction block, for
// migrations whose statements Postgres refuses to run inside one — notably
// CREATE INDEX CONCURRENTLY, which trades the SHARE lock a plain CREATE INDEX
// would hold against all writes for the whole build (blocking every writer
// fleet-wide until it finishes) for a weaker lock that only briefly waits on
// in-flight transactions at a couple of sync points.
//
// The DDL and the version-tracking insert are consequently two separate
// autocommit statements rather than one atomic transaction: if the process
// dies between them, the next startup just retries this migration, and
// IF NOT EXISTS makes that safe — unless the earlier attempt died mid-build,
// which Postgres leaves behind as an INVALID index that IF NOT EXISTS treats
// as already-there. Recovering from that needs an operator to DROP INDEX the
// invalid one and restart.
func applyConcurrentMigration(ctx context.Context, conn *sql.Conn, version int, stmts string) error {
	if _, err := conn.ExecContext(ctx, stmts); err != nil {
		return fmt.Errorf("store: apply migration %d: %w", version, err)
	}
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO puls_schema_version (version) VALUES ($1)`, version,
	); err != nil {
		return fmt.Errorf("store: record version %d: %w", version, err)
	}
	return nil
}

func (s *Postgres) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Postgres) CreateDevice(ctx context.Context, req *model.RegisterRequest) (*model.Device, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Secret), 12)
	if err != nil {
		return nil, fmt.Errorf("store: hash secret: %w", err)
	}

	id, now := uuid.New().String(), time.Now().UTC()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO devices (id, name, os, arch, secret_hash, registered_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, req.Name, req.OS, req.Arch, hash, now,
	)
	if err != nil {
		return nil, fmt.Errorf("store: create device: %w", err)
	}

	return &model.Device{
		ID: id, Name: req.Name, OS: model.DeviceOS(req.OS), Arch: req.Arch,
		Status: model.StatusUnknown, RegisteredAt: now,
	}, nil
}

func (s *Postgres) GetDevice(ctx context.Context, id string) (*model.Device, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, os, arch, status, registered_at, last_seen_at FROM devices WHERE id = $1`, id)
	d, err := scanPGDevice(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

func (s *Postgres) ListDevices(ctx context.Context, limit int, cursor string) ([]model.Device, string, error) {
	query := `SELECT id, name, os, arch, status, registered_at, last_seen_at FROM devices`
	var args []any

	if cursor != "" {
		registeredAt, id, err := decodeDeviceCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		query += ` WHERE registered_at < $1 OR (registered_at = $2 AND id < $3)`
		args = append(args, registeredAt, registeredAt, id)
	}
	// Fetch one extra row to detect whether a next page exists without a
	// separate COUNT query.
	query += fmt.Sprintf(` ORDER BY registered_at DESC, id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("store: list devices: %w", err)
	}
	devices, err := collect(rows, scanPGDevice)
	if err != nil {
		return nil, "", err
	}

	var nextCursor string
	if len(devices) > limit {
		devices = devices[:limit]
		last := devices[len(devices)-1]
		nextCursor = encodeDeviceCursor(last.RegisteredAt, last.ID)
	}
	return devices, nextCursor, nil
}

func (s *Postgres) SetDeviceStatus(ctx context.Context, id string, status model.DeviceStatus) error {
	res, err := s.db.ExecContext(ctx, `UPDATE devices SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("store: set device status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) UpdateLastSeen(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE devices SET last_seen_at = $1, status = 'online' WHERE id = $2`,
		time.Now().UTC(), id)
	return err
}

func (s *Postgres) InsertHeartbeat(ctx context.Context, hb *model.Heartbeat) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO heartbeats (device_id, received_at, cpu_percent, memory_percent, disk_percent, uptime_seconds, os_version)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		hb.DeviceID, time.Now().UTC(),
		hb.CPUPercent, hb.MemoryPercent, hb.DiskPercent, hb.UptimeSeconds, hb.OSVersion,
	)
	return err
}

func (s *Postgres) ListHeartbeats(ctx context.Context, deviceID string, limit int) ([]model.Heartbeat, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, device_id, received_at, cpu_percent, memory_percent, disk_percent, uptime_seconds, os_version
		 FROM heartbeats WHERE device_id = $1 ORDER BY received_at DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list heartbeats: %w", err)
	}
	return collect(rows, scanPGHeartbeat)
}

func (s *Postgres) CreateDiagnosticRequest(ctx context.Context, deviceID, requestID string, scope model.DiagnosticScope) (*model.DiagnosticResult, error) {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO diagnostic_results (device_id, request_id, scope, requested_at) VALUES ($1, $2, $3, $4)`,
		deviceID, requestID, scope, now,
	)
	if err != nil {
		return nil, fmt.Errorf("store: create diagnostic request: %w", err)
	}
	return &model.DiagnosticResult{DeviceID: deviceID, RequestID: requestID, Scope: scope, RequestedAt: now}, nil
}

func (s *Postgres) DeleteDiagnosticRequest(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM diagnostic_results WHERE request_id = $1`, requestID)
	if err != nil {
		return fmt.Errorf("store: delete diagnostic request: %w", err)
	}
	return nil
}

func (s *Postgres) SaveDiagnosticResult(ctx context.Context, requestID string, payload []byte) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE diagnostic_results SET received_at = $1, payload = $2 WHERE request_id = $3`,
		time.Now().UTC(), string(payload), requestID,
	)
	if err != nil {
		return fmt.Errorf("store: save diagnostic result: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Postgres) ListDiagnosticResults(ctx context.Context, deviceID string, limit int) ([]model.DiagnosticResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, device_id, request_id, scope, requested_at, received_at, payload
		 FROM diagnostic_results WHERE device_id = $1 ORDER BY requested_at DESC LIMIT $2`, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: list diagnostics: %w", err)
	}
	return collect(rows, scanPGDiagnostic)
}

func (s *Postgres) GetDiagnosticResult(ctx context.Context, requestID string) (*model.DiagnosticResult, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, device_id, request_id, scope, requested_at, received_at, payload
		 FROM diagnostic_results WHERE request_id = $1`, requestID)
	d, err := scanPGDiagnostic(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// Postgres returns native time.Time from TIMESTAMPTZ; we normalise to UTC for
// consistent API output regardless of the database server's local timezone.

func scanPGDevice(s rowScanner) (*model.Device, error) {
	var d model.Device
	var lastSeen *time.Time
	if err := s.Scan(&d.ID, &d.Name, &d.OS, &d.Arch, &d.Status, &d.RegisteredAt, &lastSeen); err != nil {
		return nil, fmt.Errorf("store: scan device: %w", err)
	}
	d.RegisteredAt = d.RegisteredAt.UTC()
	if lastSeen != nil {
		u := lastSeen.UTC()
		d.LastSeenAt = &u
	}
	return &d, nil
}

func scanPGHeartbeat(s rowScanner) (*model.Heartbeat, error) {
	var hb model.Heartbeat
	if err := s.Scan(&hb.ID, &hb.DeviceID, &hb.ReceivedAt,
		&hb.CPUPercent, &hb.MemoryPercent, &hb.DiskPercent,
		&hb.UptimeSeconds, &hb.OSVersion); err != nil {
		return nil, fmt.Errorf("store: scan heartbeat: %w", err)
	}
	hb.ReceivedAt = hb.ReceivedAt.UTC()
	return &hb, nil
}

func scanPGDiagnostic(s rowScanner) (*model.DiagnosticResult, error) {
	var d model.DiagnosticResult
	var received *time.Time
	var payload sql.NullString
	if err := s.Scan(&d.ID, &d.DeviceID, &d.RequestID, &d.Scope,
		&d.RequestedAt, &received, &payload); err != nil {
		return nil, fmt.Errorf("store: scan diagnostic: %w", err)
	}
	d.RequestedAt = d.RequestedAt.UTC()
	if received != nil {
		u := received.UTC()
		d.ReceivedAt = &u
	}
	if payload.Valid && payload.String != "" {
		rm := json.RawMessage(payload.String)
		d.Payload = &rm
	}
	return &d, nil
}
