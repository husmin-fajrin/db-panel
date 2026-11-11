-- When a server's "Jangan ganggu" began (spec 5.7, R-043): the digest sent
-- when the snooze ends covers the alerts opened from then on. Stored in the
-- database, not in memory, so a panel restart during the snooze does not
-- lose the digest. NULL means no digest is owed: a server never snoozed,
-- one snoozed before this migration, and one whose digest was sent.
ALTER TABLE servers ADD COLUMN snooze_from TEXT;
