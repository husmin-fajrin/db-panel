-- The names the MySQL server certificate holds (comma separated), so a
-- replica job can tell that the master's address changed after it was
-- issued. Empty for a certificate installed before this column existed.
ALTER TABLE server_tls ADD COLUMN hosts TEXT NOT NULL DEFAULT '';
