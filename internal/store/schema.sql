PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS devices (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    os            TEXT NOT NULL,
    arch          TEXT NOT NULL,
    secret_hash   BLOB NOT NULL,
    status        TEXT NOT NULL DEFAULT 'unknown',
    registered_at TEXT NOT NULL,
    last_seen_at  TEXT
);

CREATE TABLE IF NOT EXISTS heartbeats (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id      TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    received_at    TEXT    NOT NULL,
    cpu_percent    REAL,
    memory_percent REAL,
    disk_percent   REAL,
    uptime_seconds INTEGER,
    os_version     TEXT
);

CREATE TABLE IF NOT EXISTS diagnostic_results (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id    TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    request_id   TEXT NOT NULL UNIQUE,
    scope        TEXT NOT NULL DEFAULT 'full',
    requested_at TEXT NOT NULL,
    received_at  TEXT,
    payload      TEXT
);
