# Puls

[![CI](https://github.com/jbringb/puls/actions/workflows/ci.yml/badge.svg)](https://github.com/jbringb/puls/actions/workflows/ci.yml)

Device uptime and status monitoring server. Devices register with the server, maintain a persistent WebSocket connection, and send periodic heartbeats containing basic system diagnostics. The server can also query connected devices for detailed, on-demand diagnostic data.

## Getting Started

### Prerequisites

- Go 1.25+

### Build

```bash
go build -o puls-server ./cmd/puls-server
```

### Run

```bash
export PULS_JWT_SECRET="at-least-32-characters-long-signing-key"
export PULS_ADMIN_SECRET="separate-admin-password-min-16-chars"
./puls-server
```

`PULS_JWT_SECRET` is the HMAC signing key for issued tokens; `PULS_ADMIN_SECRET`
is the password presented to mint an admin token. They must differ — reusing the
signing key as the admin password would let an admin forge arbitrary tokens.

The server starts on `:8080` with an in-memory SQLite database by default. Data is lost on restart - set `PULS_DB_PATH` to a file path for persistence.

### Docker

Build the image:

```bash
docker build -t puls-server .
```

Run it:

```bash
docker run -p 8080:8080 \
  -e PULS_JWT_SECRET="your-signing-key-at-least-32-chars" \
  -e PULS_ADMIN_SECRET="your-admin-password-min-16-chars" \
  puls-server
```

With a persistent database:

```bash
docker run -p 8080:8080 \
  -e PULS_JWT_SECRET="your-signing-key-at-least-32-chars" \
  -e PULS_ADMIN_SECRET="your-admin-password-min-16-chars" \
  -e PULS_DB_PATH=/data/puls.db \
  -v /path/to/data:/data \
  puls-server
```

Or use `docker-compose.yml` to run Puls alongside a Postgres backend:

```bash
docker compose up
```

### Admin Endpoints

All admin endpoints require an `Authorization: Bearer <admin-jwt>` header. Obtain an
admin token by presenting `PULS_ADMIN_SECRET` to the token endpoint:

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/admin-token \
  -H 'Content-Type: application/json' \
  -d '{"secret":"separate-admin-password-min-16-chars"}'
```

The response is `{"token": "<admin-jwt>"}`, valid for `PULS_ADMIN_TOKEN_EXPIRY` (default 24h).

### puls-agent — watch your own machine show up

`cmd/puls-agent` is a real device client: it registers with a running Puls server,
holds a WebSocket connection, and reports actual CPU/memory/disk/uptime from
whatever machine it's running on — not simulated data. With a server running
(see above), in another terminal:

```bash
go run ./cmd/puls-agent -secret "a-registration-secret-at-least-16-chars"
```

Then check it's there:

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/admin-token \
  -H 'Content-Type: application/json' \
  -d '{"secret":"separate-admin-password-min-16-chars"}'
# {"token": "<admin-jwt>"}

curl -s http://localhost:8080/api/v1/devices -H "Authorization: Bearer <admin-jwt>"
```

Your device shows up `online`, with `recentHeartbeats` carrying your real CPU,
memory, and disk usage. The agent also answers on-demand diagnostic requests
(`POST /api/v1/devices/{id}/diagnose`) with hostname, network interfaces, disk
partitions, and top processes by CPU.

By default it registers as a new device on first run and saves the resulting
device ID/token to a local state file (`PULS_AGENT_STATE_FILE`, defaulting to
your OS config dir) — restarting the agent reconnects as the same device
instead of registering a new one every time.

Flags (all also settable via `PULS_AGENT_*` env vars — see `-h`):

| Flag | Default | Purpose |
|---|---|---|
| `-server` | `http://localhost:8080` | Puls server base URL |
| `-name` | hostname | device name to register |
| `-secret` | — | registration secret, min 16 chars (only needed on first run) |
| `-interval` | `20s` | heartbeat interval |
| `-state-file` | OS config dir | where the device ID/token are saved |
| `-os` | autodetected | override the reported OS (`windows` or `linux`; non-Windows hosts report as `linux`, since that's all the server models) |

It builds and cross-compiles cleanly for Linux, Windows, and macOS (amd64/arm64) —
see the `build-agent` job in CI.

## Dependencies

| Package | Purpose |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |
| `github.com/golang-jwt/jwt/v5` | JWT creation and validation |
| `github.com/coder/websocket` | Context-aware WebSocket library |
| `golang.org/x/crypto/bcrypt` | Registration secret hashing |
| `github.com/google/uuid` | Request and device ID generation |
| `github.com/shirou/gopsutil/v4` | Real host stats for `puls-agent` (CPU, memory, disk, processes) |
