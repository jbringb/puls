# Puls

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
export PULS_JWT_SECRET="secret-key"
./puls-server
```

The server starts on `:8080` with an in-memory SQLite database by default. Data is lost on restart - set `PULS_DB_PATH` to a file path for persistence.

### Docker

Build the image:

```bash
docker build -t puls-server .
```

Run it:

```bash
docker run -p 8080:8080 -e PULS_JWT_SECRET="your-secret-at-least-32-chars" puls-server
```

With a persistent database:

```bash
docker run -p 8080:8080 \
  -e PULS_JWT_SECRET="your-secret-at-least-32-chars" \
  -e PULS_DB_PATH=/data/puls.db \
  -v /path/to/data:/data \
  puls-server
```

### Admin Endpoints

All admin endpoints require an `Authorization: Bearer <admin-jwt>` header. Admin tokens are issued out-of-band (e.g. via `IssueAdminToken` in `internal/auth`).

## Dependencies

| Package | Purpose |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |
| `github.com/golang-jwt/jwt/v5` | JWT creation and validation |
| `github.com/coder/websocket` | Context-aware WebSocket library |
| `golang.org/x/crypto/bcrypt` | Registration secret hashing |
| `github.com/google/uuid` | Request and device ID generation |
