-- Hash chain over the audit log (SEC-45): each row stores the previous
-- row's hash and its own. The Go hook of this migration (migrate.go,
-- backfillAuditChain) lifts the append-only triggers, fills both columns
-- for the existing rows in id order and puts the triggers back, in the
-- same transaction as these statements.
ALTER TABLE audit_log ADD COLUMN prev_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_log ADD COLUMN hash TEXT NOT NULL DEFAULT '';
