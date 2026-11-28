-- Plan 5b-3a (spec 5.1, "Alamat master"): who last set the address
-- replicas reach the master at, and when. source_changed_by is the panel
-- user's name, or 'job:<id>' when a promote job set it. Topologies from
-- before 0015, and addresses the panel took itself ("Siapkan master"),
-- have neither: NULL.
ALTER TABLE topologies ADD COLUMN source_changed_by TEXT;
ALTER TABLE topologies ADD COLUMN source_changed_at TEXT;
