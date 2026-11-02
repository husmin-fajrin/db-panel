-- Latest health snapshot per server, as reported by its agent.
CREATE TABLE server_health (
    server_id     INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    received_at   TEXT    NOT NULL,
    snapshot_json TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT '',
    status_since  TEXT    NOT NULL,
    lock_ok       INTEGER NOT NULL DEFAULT 0,
    agent_version TEXT    NOT NULL DEFAULT ''
);

-- Status transitions, kept for the history view and the weekly summary.
CREATE TABLE status_events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    topology_id INTEGER NOT NULL DEFAULT 0,
    ts          TEXT    NOT NULL,
    kind        TEXT    NOT NULL,
    from_status TEXT    NOT NULL DEFAULT '',
    to_status   TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX status_events_server_ts ON status_events(server_id, ts);

CREATE TABLE alerts (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    kind             TEXT    NOT NULL,
    server_id        INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    topology_id      INTEGER NOT NULL DEFAULT 0,
    opened_at        TEXT    NOT NULL,
    closed_at        TEXT,
    last_notified_at TEXT,
    notify_count     INTEGER NOT NULL DEFAULT 0,
    message          TEXT    NOT NULL DEFAULT '',
    telegram_state   TEXT    NOT NULL DEFAULT 'pending',
    telegram_error   TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX alerts_server_kind_closed ON alerts(server_id, kind, closed_at);

-- At most one open alert per (kind, server).
CREATE UNIQUE INDEX alerts_open_unique ON alerts(kind, server_id) WHERE closed_at IS NULL;

-- Append-only record of actions. target_id deliberately has no foreign key:
-- the triggers below forbid UPDATE and DELETE, so a cascade from servers
-- would make every server delete fail, and the trail must outlive its target.
CREATE TABLE audit_log (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ts          TEXT    NOT NULL,
    username    TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL,
    target_kind TEXT    NOT NULL DEFAULT '',
    target_id   INTEGER NOT NULL DEFAULT 0,
    detail_json TEXT    NOT NULL DEFAULT '',
    outcome     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX audit_log_target_ts ON audit_log(target_id, ts);

CREATE TRIGGER audit_log_no_update BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER audit_log_no_delete BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

ALTER TABLE replicas ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN snooze_until TEXT;
