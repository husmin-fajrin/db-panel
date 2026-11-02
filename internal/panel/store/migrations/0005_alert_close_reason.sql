-- Why an alert closed without recovering: its server stopped being judged
-- (a resync started, the replica left the panel). Empty for a recovery.
ALTER TABLE alerts ADD COLUMN close_reason TEXT NOT NULL DEFAULT '';
