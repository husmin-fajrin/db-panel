-- Promotion of a replica to master: the topology remembers the previous
-- master and whether clients still have to be cut over, and each promotion
-- is recorded so an interrupted one can be resumed or reviewed.
ALTER TABLE topologies ADD COLUMN cutover_pending INTEGER NOT NULL DEFAULT 0;
ALTER TABLE topologies ADD COLUMN old_master_server_id INTEGER NOT NULL DEFAULT 0;

CREATE TABLE promotions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    topology_id INTEGER NOT NULL,
    mode TEXT NOT NULL,                      -- 'planned' | 'emergency'
    old_master_server_id INTEGER NOT NULL,
    new_master_server_id INTEGER NOT NULL,
    started_at TEXT NOT NULL,
    finished_at TEXT,
    cutover_confirmed_at TEXT,
    lock_at TEXT,
    last_master_gtid TEXT NOT NULL DEFAULT '',
    new_master_gtid TEXT NOT NULL DEFAULT '',
    replicas_json TEXT NOT NULL DEFAULT '[]',
    leftover_json TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'running',  -- running | partial | done | failed
    job_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX promotions_topology ON promotions (topology_id, id);
CREATE INDEX promotions_old_master ON promotions (old_master_server_id, id);
