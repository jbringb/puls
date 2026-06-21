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

### Admin Endpoints

All admin endpoints require an `Authorization: Bearer <admin-jwt>` header. Obtain an
admin token by presenting `PULS_ADMIN_SECRET` to the token endpoint:

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/admin-token \
  -H 'Content-Type: application/json' \
  -d '{"secret":"separate-admin-password-min-16-chars"}'
```

The response is `{"token": "<admin-jwt>"}`, valid for `PULS_ADMIN_TOKEN_EXPIRY` (default 24h).

## Dependencies

| Package | Purpose |
|---|---|
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |
| `github.com/golang-jwt/jwt/v5` | JWT creation and validation |
| `github.com/coder/websocket` | Context-aware WebSocket library |
| `golang.org/x/crypto/bcrypt` | Registration secret hashing |
| `github.com/google/uuid` | Request and device ID generation |
