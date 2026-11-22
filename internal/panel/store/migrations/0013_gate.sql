-- Plan 5b-2b (spec 5.7-5.13): what the pre-flight gate and the
-- backup_stale alert read. backup_dir is the master's backup folder its
-- agent scans ('' means none set: the scan is off); backup_max_age_hours
-- is the age past which its newest file is stale. account is the MySQL
-- account the agent works with (CURRENT_USER(), user@host), as its last
-- mysql.connect or mysql.inventory answered; '' until one does.
ALTER TABLE servers ADD COLUMN backup_dir TEXT NOT NULL DEFAULT '';
ALTER TABLE servers ADD COLUMN backup_max_age_hours INTEGER NOT NULL DEFAULT 48;
ALTER TABLE mysql_inventory ADD COLUMN account TEXT NOT NULL DEFAULT '';
