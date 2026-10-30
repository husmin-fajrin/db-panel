CREATE TABLE mysql_inventory (
    server_id           INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    connected           INTEGER NOT NULL DEFAULT 0,
    version             TEXT    NOT NULL DEFAULT '',
    series              TEXT    NOT NULL DEFAULT '',
    databases_json      TEXT    NOT NULL DEFAULT '[]',
    missing_grants_json TEXT    NOT NULL DEFAULT '[]',
    master_ready        INTEGER NOT NULL DEFAULT 0,
    preflight_json      TEXT    NOT NULL DEFAULT '',
    updated_at          TEXT    NOT NULL
);

CREATE TABLE topologies (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    master_server_id  INTEGER NOT NULL UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
    repl_user         TEXT    NOT NULL,
    repl_password_enc BLOB    NOT NULL,
    source_host       TEXT    NOT NULL,
    source_port       INTEGER NOT NULL,
    created_at        TEXT    NOT NULL
);

CREATE TABLE replicas (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    topology_id    INTEGER NOT NULL REFERENCES topologies(id) ON DELETE CASCADE,
    server_id      INTEGER NOT NULL UNIQUE REFERENCES servers(id) ON DELETE CASCADE,
    databases_json TEXT    NOT NULL DEFAULT '[]',
    state          TEXT    NOT NULL DEFAULT 'creating',
    last_error     TEXT    NOT NULL DEFAULT '',
    created_at     TEXT    NOT NULL
);

CREATE INDEX replicas_topology_id ON replicas(topology_id);

CREATE TABLE jobs (
    id           TEXT    PRIMARY KEY,
    kind         TEXT    NOT NULL,
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    topology_id INTEGER NOT NULL DEFAULT 0,
    status      TEXT    NOT NULL DEFAULT 'queued',
    current_step INTEGER NOT NULL DEFAULT 0,
    error       TEXT    NOT NULL DEFAULT '',
    params_json TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    started_at  TEXT,
    finished_at TEXT
);

CREATE INDEX jobs_server_created ON jobs(server_id, created_at);

CREATE TABLE job_steps (
    job_id      TEXT    NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    idx         INTEGER NOT NULL,
    name        TEXT    NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'pending',
    message     TEXT    NOT NULL DEFAULT '',
    started_at  TEXT,
    finished_at TEXT,
    PRIMARY KEY (job_id, idx)
);

CREATE TABLE job_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id  TEXT    NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    ts      TEXT    NOT NULL,
    level   TEXT    NOT NULL,
    message TEXT    NOT NULL
);

CREATE INDEX job_log_job_id ON job_log(job_id, id);
