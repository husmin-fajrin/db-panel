-- Start marks (spec 5.5): per server and start-alert kind, the decided time
-- of the newest start of MySQL the panel has accounted for, by opening an
-- alert or by deciding on purpose that none is due. It outlives the alerts
-- (which are pruned) so a start result the agent keeps reporting never
-- opens the same alert twice. It only ever moves forward.
CREATE TABLE start_marks (
    server_id  INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL,
    decided_at TEXT    NOT NULL,
    PRIMARY KEY (server_id, kind)
);
