-- Lag samples for the dashboard's lag chart (spec 5.7, 8.1): one row per
-- monitor.sample and per health report the panel takes, at the time the
-- agent took the reading. ts is unix seconds (UTC), so the chart query
-- buckets with integer division. lag_sec is NULL when the replica's lag
-- is unknown; io_running and sql_running are as the agent reported them.
-- Pruned daily after samples_retention_days.
CREATE TABLE health_samples (
    server_id   INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    ts          INTEGER NOT NULL,
    lag_sec     INTEGER NULL,
    io_running  TEXT    NOT NULL DEFAULT '',
    sql_running TEXT    NOT NULL DEFAULT '',
    mysql_ok    INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX health_samples_server_ts ON health_samples(server_id, ts);

CREATE INDEX health_samples_ts ON health_samples(ts);
