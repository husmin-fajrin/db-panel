# Agent-side audit of plan 5b-1 — controller's notes of the auditor's report (head 8ade0e0)

Findings A-1..A-9 with evidence and required fixes: see final-fix-findings.md ("From the agent security audit"). Severity: A-1 High; A-2, A-3, A-4 Medium; A-5, A-6 (= F-1/F-2), A-7, A-8 (= F-6) Low; A-9 Informational.

## Verdict (auditor)
The strict gate itself is sound: one dispatch path (session.go:99 → registry.go:139); unknown commands and unreadable pins fail closed; the direct SA-1 path (releaseRole) is closed; approval code, clocks, file trust checks and CLI re-checks done carefully. Weak points: what one approval buys (A-1), an allowed command that is not harmless (A-2), a new input from the MySQL service account to root (A-3). Merging acceptable after a fix wave addressing A-1..A-3; A-4 at least recorded accurately (spec §8 understates it).

## Checked and found sound (auditor)
- Gate completeness: one dispatch path; gate after the local policy, before the handler (registry.go:129-143); commands missing from the table are protected (strict.go:130-135); unreadable pins allow only allowed commands (strict.go:120-126); the test registers the real command set (strict_commands_test.go:18-38); no other Register sites.
- Parameter-dependent commands: setReadOnly{off} and configure{master} refused on a quarantined server (pins.go:877, 883); releaseRole (pins.go:771, 601); requarantine only for the pinned source (quarantine.go:177-188); guard interval fixed (monitor.go:118); recordReplicas takes no parameters.
- Approval as a credential: code from crypto/rand (rebuild_approval.go:64-71), constant-time compare (cli.go:444); 5-minute future slack and 24 h cap (rebuild_approval.go:88-92, 117-128) — a clock moved back gains at most 5 minutes; request and source re-checked after `ya` (cli.go:481-485); rollback on a failed later step (cli.go:488-503). The code is not a secret (the panel receives it; the file is 0644 in a 0711 directory): it only ties root's approval to a live request; only root can write the approval, so guessing gains nothing. Files cleared by SetRole (pins.go:457), entering quarantine (pins.go:515-519), settle (quarantine.go:462).
- File handling as root: writePin temp+chmod+fsync+rename+dir fsync (pins.go:977-1008); readTrustedFile Lstat, regular, no group/other write, owner root, size bound (pins.go:324-353); JSON records ≤ 64 KiB; start-lock file through O_NOFOLLOW (startlock.go:401-452); error log O_NOFOLLOW|O_NONBLOCK + regular-file check (startlock_guard.go:369-390) — SA-6/SA-7 intact; /etc/dbpanel and /var/lib/dbpanel root-owned 0711 (scripts/install.sh:34-35).
- Process execution: only --print-defaults with at most --defaults-file (startlock.go:571-576), no plugin loads; a changed `user` in ~/.my.cnf makes the check refuse (startlock.go:622-624); measured: mysqld ignores a world-writable .my.cnf; errors for !include of a secret file name file and line without content.
- CLI output: source hosts pass sourceHostRe (snapshot.go:26, pins.go:362-383); file contents in errors printed with %q; set-role asks, re-checks, exits 1 on cancel/empty stdin (cli.go:346-372).
- DoS through events: queue bounded at 64, drops start reports first (event_queue.go:22, 51-74); guard never sends under promoteMu (promote_lock.go:562-570); monitor.configure intervals bounded (monitor.go:107-114).
- Start-result file: bounded, cutInitFile, retried each guard round (start_result.go:187-246).

## Residual risks (auditor)
- A running rebuild job continues after the approval is revoked or expires.
- The approval rests on root reading the screen (spec §8).
- The start-lock check does not see systemd `User=` differing from the `user` option, `MYSQL_HOME` in the service environment, `--init-file` on the service command line (root-only configuration; the guard reports the start as bypassed or late within about one round).
- The MySQL service account owns the data directory: truthfulness against it is best effort.
- A failed start-result write followed by an agent restart loses that start.
- `mysql.salvageDelete` is allowed without approval (R-019's salvage-then-download procedure covers it).
- DNS availability is a dependency of every approved command.
- (controller, from A-4) the rebuild and the replication link connect by name and re-resolve; TLS verification authenticates the peer when the new master has the panel's certificate.

## Risk inventory impact (auditor, agent side)
R-109 (SA-1) narrowed (direct releaseRole path closed; A-1 a remaining path behind one approval — closed by the fix wave if A-1 lands); R-110 (SA-2) narrowed (dropping/replacing data needs root's approval; quarantining a healthy master still possible; A-2 a further variant); R-019 narrowed by the approval (the code fence "refuse while unsalvaged leftovers exist" not built); R-113 largely closed (service HOME read) but opens A-3; open for systemd User=/MYSQL_HOME; R-116 closed (write-failure+restart residual); R-117 closed; R-121 closed; R-079 worsened in effect (A-2) — closed if the locality rule lands; R-039 unchanged; R-097, R-112, R-122 unchanged. No regression against SA-4, SA-6, SA-7, SA-12.

## Unconfirmed (auditor)
- `!include /dev/watchdog` in the account's ~/.my.cnf opened by root's mysqld (removed by running as the service uid).
- The >1 MiB snapshot reconnect loop end to end.
- Plugin options in the account's ~/.my.cnf turning read-only off after the start check (the snapshot's flags would show it).
- `mysql.salvage` runs mysqlbinlog as root on binary logs the service account owns (pre-existing; parser-bug risk).
