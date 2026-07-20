package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/jbringb/puls/internal/model"
)

func newTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping Postgres integration tests")
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	st, err := NewPostgres(db)
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}

	t.Cleanup(func() {
		db.Exec("DELETE FROM diagnostic_results")
		db.Exec("DELETE FROM heartbeats")
		db.Exec("DELETE FROM devices")
	})

	return st
}

func createPGDevice(t *testing.T, st *Postgres) *model.Device {
	t.Helper()
	d, err := st.CreateDevice(context.Background(), &model.RegisterRequest{
		Name: "laptop", OS: model.OSLinux, Arch: "amd64", Secret: "registration-secret",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	return d
}

func TestPostgresMigrationsSetVersion(t *testing.T) {
	st := newTestPostgres(t)
	var v int
	if err := st.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM puls_schema_version").Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if v != len(pgMigrations) {
		t.Fatalf("schema version = %d, want %d", v, len(pgMigrations))
	}
}

func TestPostgresMigrationsIdempotent(t *testing.T) {
	st := newTestPostgres(t)
	if _, err := NewPostgres(st.db); err != nil {
		t.Fatalf("re-running NewPostgres: %v", err)
	}
}

// TestPostgresIndexMigrationBuildsValidIndex checks that the v2 migration's
// CREATE INDEX CONCURRENTLY actually completed successfully against a real
// server. A regression that wrapped it back in a transaction (the way
// non-concurrent migrations run) would fail migrate() outright, since
// Postgres refuses CONCURRENTLY inside a transaction block — but if some
// other change instead let a concurrent build get interrupted, Postgres
// leaves the index behind marked INVALID rather than failing loudly, so this
// also checks indisvalid directly rather than only checking existence.
func TestPostgresIndexMigrationBuildsValidIndex(t *testing.T) {
	st := newTestPostgres(t)

	var isValid bool
	if err := st.db.QueryRow(
		`SELECT indisvalid FROM pg_index WHERE indexrelid = 'idx_devices_registered_at_id'::regclass`,
	).Scan(&isValid); err != nil {
		t.Fatalf("query pg_index: %v", err)
	}
	if !isValid {
		t.Error("idx_devices_registered_at_id exists but is INVALID — its CONCURRENTLY build was interrupted")
	}
}

func TestPostgresPing(t *testing.T) {
	st := newTestPostgres(t)
	if err := st.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPostgresCreateAndGetDevice(t *testing.T) {
	st := newTestPostgres(t)
	ctx := context.Background()

	created := createPGDevice(t, st)
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

func TestPostgresGetDeviceNotFound(t *testing.T) {
	st := newTestPostgres(t)
	_, err := st.GetDevice(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDevice error = %v, want ErrNotFound", err)
	}
}

func TestPostgresSetDeviceStatus(t *testing.T) {
	st := newTestPostgres(t)
	ctx := context.Background()
	d := createPGDevice(t, st)

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

func TestPostgresHeartbeats(t *testing.T) {
	st := newTestPostgres(t)
	ctx := context.Background()
	d := createPGDevice(t, st)

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

func TestPostgresListDevicesPagination(t *testing.T) {
	st := newTestPostgres(t)
	ctx := context.Background()

	const total = 5
	want := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		want[createPGDevice(t, st).ID] = true
	}

	seen := make(map[string]bool, total)
	var order []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatalf("pagination did not terminate after %d pages", pages)
		}
		page, next, err := st.ListDevices(ctx, 2, cursor)
		if err != nil {
			t.Fatalf("ListDevices(cursor=%q): %v", cursor, err)
		}
		if cursor == "" && len(page) == 0 {
			t.Fatal("first page is empty")
		}
		for _, d := range page {
			if seen[d.ID] {
				t.Fatalf("device %s returned twice across pages", d.ID)
			}
			seen[d.ID] = true
			order = append(order, d.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("saw %d devices across pages, want %d", len(seen), total)
	}
	for id := range want {
		if !seen[id] {
			t.Errorf("device %s never appeared in any page", id)
		}
	}

	full, next, err := st.ListDevices(ctx, total, "")
	if err != nil {
		t.Fatalf("ListDevices(unpaginated): %v", err)
	}
	if next != "" {
		t.Errorf("unpaginated fetch (limit=total) got a nextCursor, want none")
	}
	var fullOrder []string
	for _, d := range full {
		fullOrder = append(fullOrder, d.ID)
	}
	if len(fullOrder) != len(order) {
		t.Fatalf("unpaginated order has %d ids, paginated walk has %d", len(fullOrder), len(order))
	}
	for i := range fullOrder {
		if fullOrder[i] != order[i] {
			t.Errorf("order mismatch at index %d: unpaginated=%s paginated=%s", i, fullOrder[i], order[i])
		}
	}
}

func TestPostgresListDevicesInvalidCursor(t *testing.T) {
	st := newTestPostgres(t)
	_, _, err := st.ListDevices(context.Background(), 10, "not-a-valid-cursor")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("ListDevices(bad cursor) error = %v, want ErrInvalidCursor", err)
	}
}

func TestPostgresDiagnosticLifecycle(t *testing.T) {
	st := newTestPostgres(t)
	ctx := context.Background()
	d := createPGDevice(t, st)

	const reqID = "req-pg-1"
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

func TestPostgresDeleteDiagnosticRequest(t *testing.T) {
	st := newTestPostgres(t)
	ctx := context.Background()
	d := createPGDevice(t, st)

	const reqID = "req-pg-to-delete"
	if _, err := st.CreateDiagnosticRequest(ctx, d.ID, reqID, model.ScopeFull); err != nil {
		t.Fatalf("CreateDiagnosticRequest: %v", err)
	}

	if err := st.DeleteDiagnosticRequest(ctx, reqID); err != nil {
		t.Fatalf("DeleteDiagnosticRequest: %v", err)
	}

	if _, err := st.GetDiagnosticResult(ctx, reqID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDiagnosticResult after delete = %v, want ErrNotFound", err)
	}

	if err := st.DeleteDiagnosticRequest(ctx, "never-existed"); err != nil {
		t.Errorf("DeleteDiagnosticRequest(unknown) = %v, want nil", err)
	}
}
