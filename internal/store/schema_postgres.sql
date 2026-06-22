-- Migration v1: initial schema.
-- Tracked by the puls_schema_version table (managed by NewPostgres.migrate).

CREATE TABLE IF NOT EXISTS devices (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    os            TEXT NOT NULL,
    arch          TEXT NOT NULL,
    secret_hash   BYTEA NOT NULL,
    status        TEXT NOT NULL DEFAULT 'unknown',
    registered_at TIMESTAMPTZ NOT NULL,
    last_seen_at  TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS heartbeats (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id      TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    received_at    TIMESTAMPTZ NOT NULL,
    cpu_percent    REAL,
    memory_percent REAL,
    disk_percent   REAL,
    uptime_seconds BIGINT,
    os_version     TEXT
);

CREATE TABLE IF NOT EXISTS diagnostic_results (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    request_id   TEXT NOT NULL UNIQUE,
    scope        TEXT NOT NULL DEFAULT 'full',
    requested_at TIMESTAMPTZ NOT NULL,
    received_at  TIMESTAMPTZ,
    payload      TEXT
);
