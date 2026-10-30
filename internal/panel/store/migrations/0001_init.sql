CREATE TABLE servers (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    name          TEXT    NOT NULL UNIQUE,
    address       TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL DEFAULT 'unassigned',
    paired        INTEGER NOT NULL DEFAULT 0,
    cert_serial   TEXT    NOT NULL DEFAULT '',
    probe_json    TEXT    NOT NULL DEFAULT '',
    agent_version TEXT    NOT NULL DEFAULT '',
    last_seen_at  TEXT,
    created_at    TEXT    NOT NULL
);

CREATE TABLE pair_tokens (
    token      TEXT    PRIMARY KEY,
    server_id  INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    expires_at TEXT    NOT NULL,
    used_at    TEXT
);

CREATE INDEX pair_tokens_server_id ON pair_tokens(server_id);

CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
