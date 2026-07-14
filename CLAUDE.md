# Puls

Device uptime and status monitoring server, written in Go 1.25+. Devices register,
hold a persistent WebSocket connection, and send periodic heartbeats with basic
system diagnostics. The server can also query connected devices for detailed,
on-demand diagnostics and streams events to subscribers over SSE.

**Module:** `github.com/jbringb/puls`

## Build, run, test

```bash
go build -o puls-server ./cmd/puls-server   # build
go test ./...                               # run all tests
go vet ./...                                # vet

# run (listens on :8080) — JWT_SECRET signs tokens; ADMIN_SECRET mints admin tokens (must differ)
PULS_JWT_SECRET="at-least-32-characters-long" PULS_ADMIN_SECRET="separate-admin-password" ./puls-server
```

In-memory SQLite by default (data lost on restart). Set `PULS_DB_PATH` for file-backed
SQLite, or `DATABASE_URL` (standard Postgres URL) to use the Postgres backend instead.

## Key dependencies

- `modernc.org/sqlite` — pure-Go SQLite driver (no CGO)
- `github.com/jackc/pgx/v5` — Postgres driver (pgx/stdlib for database/sql)
- `github.com/golang-jwt/jwt/v5` — JWT creation and validation (HS256)
- `github.com/coder/websocket` — context-aware WebSocket library
- `golang.org/x/crypto/bcrypt` — registration secret hashing
- `github.com/google/uuid` — request and device ID generation
- `github.com/shirou/gopsutil/v4` — real host stats for `puls-agent` (CPU, memory, disk, processes)

## Structure

```
cmd/puls-server/main.go          Entry point — wires everything together
cmd/puls-agent/                  Reference device client (real host stats via gopsutil)
internal/
  config/config.go               Env-based config struct
  auth/jwt.go                    HS256 JWT issuance + validation
  model/device.go                Shared domain types
  store/store.go                 Store interface + shared scan helpers
  store/cursor.go                Opaque cursor encode/decode for device list pagination
  store/sqlite.go                SQLite implementation (PRAGMA user_version migrations)
  store/schema.sql               SQLite schema, embedded as migration v1
  store/schema_v2.sql            SQLite migration v2 — device list pagination index
  store/postgres.go              Postgres implementation (puls_schema_version migrations)
  store/schema_postgres.sql      Postgres schema, embedded as migration v1
  store/schema_postgres_v2.sql   Postgres migration v2 — device list pagination index
  ws/hub.go                      WebSocket connection registry
  ws/client.go                   Per-connection lifecycle
  ws/message.go                  Typed JSON message envelope
  observability/metrics.go       Prometheus registry, collectors, HTTP middleware
  observability/tracing.go       OTel TracerProvider setup (OTLP/HTTP, opt-in)
  server/server.go               HTTP server + route wiring
  server/middleware.go           Auth, logging, recovery, body cap
  server/broadcaster.go          Fan-out hub for server-sent events
  server/routes_device.go        Device registration, list, detail
  server/routes_ws.go            WebSocket upgrade handler
  server/routes_diagnostics.go   Diagnostics request/response
  server/routes_events.go        SSE event stream
  server/openapi.json            OpenAPI spec
```

## Conventions

### General
- Use `log/slog` everywhere — JSON in production, text in development
- Wrap public errors with `fmt.Errorf("context: %w", err)`
- No global state — pass dependencies explicitly via constructors
- All database calls take a `context.Context` with a deadline
- Prefer table-driven tests in `_test.go` files alongside the code
- Schema changes: append a new entry to both `migrations` in `store/sqlite.go`
  (tracked by `PRAGMA user_version`) and `pgMigrations` in `store/postgres.go`
  (tracked by `puls_schema_version` table). Never edit a released migration.
  SQLite runs in WAL mode. Postgres uses `TIMESTAMPTZ` and `BYTEA` for SQLite's
  `TEXT`-timestamps and `BLOB` respectively.

### Naming
- Exported types `PascalCase`; unexported `camelCase`
- Constructors: `NewXxx(deps...) (*Xxx, error)`
- HTTP handler methods on a struct: `func (s *Server) handleXxx(w, r)`
- No hyphens in identifiers, JSON keys, URL path segments, or file names.
  camelCase for JSON fields, lowercase-no-separator URL segments
  (`/api/v1/devices`), underscores allowed in file names (`routes_device.go`)

### HTTP
- Standard `net/http` `ServeMux` with Go 1.22+ method+path patterns
- All JSON responses use `application/json`
- Non-2xx responses return `{"error": "message"}`
- Decode bodies with `json.NewDecoder` + `DisallowUnknownFields()`
- List endpoints use cursor-based pagination (`?limit=&cursor=`), not offset —
  see `GET /api/v1/devices` and `internal/store/cursor.go`. The cursor encodes
  the last row's sort key(s), base64'd and opaque to clients.

### WebSocket
- Messages are JSON envelopes: `{"type":"...","requestId":"...","data":{...}}`
- Message types: `heartbeat`, `diag_request`, `diag_response`, `error`
- Clients must heartbeat within 90 seconds or the connection is closed
- Auth on upgrade, in order of preference: `Authorization: Bearer`, the
  `puls.bearer` subprotocol (`Sec-WebSocket-Protocol: puls.bearer, <token>`),
  then `?token=` (fallback only — leaks into logs)
- Browser origins restricted via `PULS_ALLOWED_ORIGINS` (default: same-origin only)

### Observability
- Prometheus metrics at `GET /metrics` (unauthenticated, rate-limited 2 req/s per IP)
  — custom registry in `internal/observability`; isolated from the default registry
- `GET /healthz` — liveness (process up); `GET /readyz` — readiness (DB ping)
- OTel tracing via `PULS_OTEL_ENDPOINT` (also `OTEL_EXPORTER_OTLP_ENDPOINT`); no-op
  if unset so local runs require no collector. `otelhttp.NewHandler` wraps the server
  when the endpoint is set.
- Key metrics: `puls_http_requests_total{method,route,status}`,
  `puls_http_request_duration_seconds{method,route}`, `puls_heartbeats_total`,
  `puls_devices_connected` (GaugeFunc backed by `hub.Count()`)

### Security
- JWT signing: HS256. Signing key from `PULS_JWT_SECRET` (min 32 chars)
- Admin tokens are minted by presenting `PULS_ADMIN_SECRET` (min 16 chars) — a
  password distinct from the signing key, compared with `subtle.ConstantTimeCompare`.
  Never authenticate against the signing key itself; that would let an admin forge tokens.
- Device JWTs expire after 90 days; admin tokens after 24 hours
- Registration secrets hashed with bcrypt (cost 12) before storage
- Always validate the `Origin` header on WebSocket upgrades

## Workflow

- Do **not** run `git push` — the user pushes manually.
