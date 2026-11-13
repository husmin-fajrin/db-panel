-- Start marks (spec 5.5): per server and start-alert kind, the start_id
-- (the agent's identity of a judged start, opaque) of the last start the
-- panel has accounted for, by opening an alert. It outlives the alerts
-- (which are pruned) so a start result the agent keeps reporting never
-- opens the same alert twice. Ids are compared for equality only.
CREATE TABLE start_marks (
    server_id INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    kind      TEXT    NOT NULL,
    start_id  TEXT    NOT NULL,
    PRIMARY KEY (server_id, kind)
);
