package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/jbringb/puls/internal/model"
)

func newTestStore(t *testing.T) *SQLite {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1) // shared in-memory DB requires a single connection
	t.Cleanup(func() { db.Close() })

	st, err := NewSQLite(db)
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	return st
}

func createDevice(t *testing.T, st *SQLite) *model.Device {
	t.Helper()
	d, err := st.CreateDevice(context.Background(), &model.RegisterRequest{
		Name: "laptop", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	return d
}

func TestMigrationsSetUserVersion(t *testing.T) {
	st := newTestStore(t)
	var v int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if v != len(migrations) {
		t.Fatalf("user_version = %d, want %d", v, len(migrations))
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	st := newTestStore(t)
	// Re-running against an already-migrated DB must be a clean no-op.
	if _, err := NewSQLite(st.db); err != nil {
		t.Fatalf("re-running NewSQLite: %v", err)
	}
}

func TestPragmasApplied(t *testing.T) {
	st := newTestStore(t)
	var fk int
	if err := st.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1 (on)", fk)
	}
}

func TestCreateAndGetDevice(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	created := createDevice(t, st)
	if created.Status != model.StatusUnknown {
		t.Errorf("new device status = %q, want unknown", created.Status)
	}

	got, err := st.GetDevice(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDevice: %v", err)
	}
	if got.Name != "laptop" || got.OS != model.OSLinux || got.Arch != "amd64" {
		t.Errorf("GetDevice returned %+v", got)
	}
}

func TestGetDeviceNotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.GetDevice(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDevice error = %v, want ErrNotFound", err)
	}
}

func TestSetDeviceStatus(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	d := createDevice(t, st)

	if err := st.SetDeviceStatus(ctx, d.ID, model.StatusOffline); err != nil {
		t.Fatalf("SetDeviceStatus: %v", err)
	}
	got, _ := st.GetDevice(ctx, d.ID)
	if got.Status != model.StatusOffline {
		t.Errorf("status = %q, want offline", got.Status)
	}

	if err := st.SetDeviceStatus(ctx, "missing", model.StatusOffline); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetDeviceStatus(missing) = %v, want ErrNotFound", err)
	}
}

func TestHeartbeats(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	d := createDevice(t, st)

	hb := &model.Heartbeat{DeviceID: d.ID, CPUPercent: 12.5, MemoryPercent: 40, DiskPercent: 60, UptimeSeconds: 3600}
	if err := st.InsertHeartbeat(ctx, hb); err != nil {
		t.Fatalf("InsertHeartbeat: %v", err)
	}

	hbs, err := st.ListHeartbeats(ctx, d.ID, 10)
	if err != nil {
		t.Fatalf("ListHeartbeats: %v", err)
	}
	if len(hbs) != 1 {
		t.Fatalf("len(heartbeats) = %d, want 1", len(hbs))
	}
	if hbs[0].CPUPercent != 12.5 {
		t.Errorf("CPUPercent = %v, want 12.5", hbs[0].CPUPercent)
	}
}

func TestDiagnosticLifecycle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	d := createDevice(t, st)

	const reqID = "req-1"
	if _, err := st.CreateDiagnosticRequest(ctx, d.ID, reqID, model.ScopeFull); err != nil {
		t.Fatalf("CreateDiagnosticRequest: %v", err)
	}

	pending, err := st.GetDiagnosticResult(ctx, reqID)
	if err != nil {
		t.Fatalf("GetDiagnosticResult: %v", err)
	}
	if pending.ReceivedAt != nil || pending.Payload != nil {
		t.Errorf("pending request should have no result yet: %+v", pending)
	}

	payload := json.RawMessage(`{"disks":2}`)
	if err := st.SaveDiagnosticResult(ctx, reqID, payload); err != nil {
		t.Fatalf("SaveDiagnosticResult: %v", err)
	}

	done, _ := st.GetDiagnosticResult(ctx, reqID)
	if done.ReceivedAt == nil {
		t.Error("ReceivedAt should be set after SaveDiagnosticResult")
	}
	if done.Payload == nil || string(*done.Payload) != `{"disks":2}` {
		t.Errorf("Payload = %v, want {\"disks\":2}", done.Payload)
	}

	if err := st.SaveDiagnosticResult(ctx, "unknown-req", payload); !errors.Is(err, ErrNotFound) {
		t.Errorf("SaveDiagnosticResult(unknown) = %v, want ErrNotFound", err)
	}
}
