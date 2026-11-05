-- One-time confirmation tokens for destructive actions (stored as SHA-256,
-- bound to user, action and target).
CREATE TABLE confirmations (
    token_hash  TEXT PRIMARY KEY,
    username    TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_kind TEXT NOT NULL DEFAULT '',
    target_id   INTEGER NOT NULL DEFAULT 0,
    expires_at  TEXT NOT NULL,
    used_at     TEXT
);

-- The MySQL server certificate the panel installed on a server.
CREATE TABLE server_tls (
    server_id    INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    cert_serial  TEXT NOT NULL,
    not_after    TEXT NOT NULL,
    installed_at TEXT NOT NULL
);

-- Whether the replica's link to its master was verified over TLS.
ALTER TABLE replicas ADD COLUMN tls_verified INTEGER NOT NULL DEFAULT 0;

-- Settings keys https_mode ('panel'|'custom', default 'panel') and
-- https_extra_names (comma separated) are created on first read, not here.
