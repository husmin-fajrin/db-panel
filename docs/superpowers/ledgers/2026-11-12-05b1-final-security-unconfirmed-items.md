# Plan 5b-1 — the four "unconfirmed" audit items, and A-9

Read-only investigation of commit `549cd1c` (agent code unchanged since `0b1c760`). Code read with `git show 549cd1c:<path>`; no test, make or mysqld was run. Statements about MySQL behaviour that the code does not settle are marked **[unmeasured]** with the measurement that would settle them. Line numbers are at `549cd1c`; paths are under `internal/agent/drivers/mysql/` unless stated.

## Summary

| # | Item | Verdict | Severity |
|---|------|---------|----------|
| 1a | Option files the service account writes make MySQL **come up** writable after the check said "locked" | NOT a vulnerability beyond the recorded trust limit (caught at the start by the guard, reported `late`/`bypassed`) | — (recorded risk) |
| 1b | MySQL **becomes** writable later in the same run (event, plugin, init_connect, a lifted read-only whose restore failed, an admin's SET) | CONFIRMED: no enforcement and no alert while mysqld stays up | Low |
| 2 | `mysql.salvage` / `mysql.leftover` run `mysqlbinlog` as root on files the service account controls | CONFIRMED: root opens any path the service account plants (symlink, FIFO, device) | Low |
| 2-new | Salvage bound is void on a quarantined server without a source pin | CONFIRMED (new) | Low |
| 3 | Does the agent refuse, mid-job, a rebuild command that names another source? | NOT a vulnerability: every source-naming command is compared with the pin and the approval, per command | — (residuals already recorded; one informational gap: `startReplica`/`startUntil`) |
| 4 | `!include /dev/watchdog` (device/FIFO) through the root `--print-defaults` run | NOT a vulnerability at 549cd1c: the root run never reads a file the service account names unless a root-owned system option file includes it | — |
| 5 | A-9 | Cannot be reconstructed from the workspace or the code | — |

A correction for the audit draft: `draft-audit-plan-5b1.md:630` (SEC-192) says "penjaga karantina menegakkan lagi dalam satu putaran". That is wrong for a flip that happens while mysqld stays up: the guard re-asserts read-only only on transitions (item 1b).

---

## Item 1 — option files that defeat the start lock

### How the agent decides the lock holds

- `checkStartLock` (startlock.go:895-945) requires: the agent's `init_file=<path>` line in the managed config (909), the file at `path` holding exactly `startLockContent` (913-919), every `--print-defaults` run judged "locked" by `judgeInitFiles` (920-936), and MAC allowing the path (937-942).
- `readEffectiveOptions` (startlock.go:606-646): run 1 as root with `HOME=/nonexistent` (612); with `--defaults-file` that is the only run (620-622); otherwise run 2 as the service account's uid/gid/groups with `HOME=<its home>` (633-637); a different `user` in run 2 is an error (641-643).
- `printDefaults` (startlock.go:703-755): 10 s timeout (705, const 45), 256 KiB output cap (707, const 49); it parses **only** `init_file`, `user` and `log_error` (738-745). Every other option `--print-defaults` prints is ignored.
- `judgeInitFiles` (startlock.go:952-988): any `maximum-` prefix fails (955-959); the agent's line must be the last `init_file` argument with no prefix (962-979); the lock file must be readable by `opts.User` by mode bits (980-986).
- The lock file (startlock.go:41): `SET GLOBAL super_read_only=ON; SET PERSIST read_only=ON; SET PERSIST super_read_only=ON;`. It runs at bootstrap, after option files and persisted variables are applied and before the first client handshake; the comment at startlock.go:24-29 says so too.

### What happens at and after a start (the guard)

- `GuardTick` (promote_lock.go:544-571) runs every `GuardEvery` = 5 s (internal/agent/monitor/monitor.go:118). On a quarantined server it calls `guardQuarantined`, then `guardStartLock` (567-570).
- `guardQuarantined` (quarantine.go:252-333) acts only on a transition. These are: MySQL answering after it did not, or agent start (`!d.qUp`, 261-264); Uptime shorter than predicted, which means a restart between ticks (284-301); the end of a rebuild load (`takeRecheck`, 279-283). On any other tick it returns at quarantine.go:302 (`if d.guard.up && sameRun { return nil }`) **without reading the flags**. On a transition it reads `@@super_read_only, @@read_only, @@init_file` (306-309), PERSISTs both ON (311), and reports the start: `late` if a flag was off, `bypassed` if `@@init_file` is not the agent's file and that file predates the mysqld start (startlock_guard.go:457-479).
- So the guard is confirmed to restore read-only only on transitions, not continuously.
- `checkStartLock` (startlock_guard.go:296-302) re-runs `--print-defaults` at most once per `startLockRetry` = 1 min.

### What the health snapshot shows and what the panel does with it

- The agent reports `read_only` and `super_read_only` in every health snapshot (health.go:152-161; proto/health.go:17-18) together with `StartLock` (proto/health.go:52).
- The panel ignores those flags for a quarantined server. `DeriveQuarantined` (internal/panel/monitor/status.go:290-299, called from state.go:412) builds the status from the store and always says "dikarantina (hanya-baca)". `LockLost` is computed only for replicas (status.go:177), and `KindReplicaUnlocked` needs `isReplica`, i.e. `Role == replica` (alerts.go:171-181, 541). `KindMasterLocked` needs a master.
- The only quarantine alerts come from `StartLock.Locked` (`KindMasterStartUnguarded`, alerts.go:353-372) and from the start events (`late`/`bypassed`/`failed`), and those events are sent only on transitions.

### Option-file contents, one by one

"Check" means `checkStartLock`. "Guard at start" means `guardQuarantined` on the next transition.

| Content (service-account `~/.my.cnf`, its `!include`s, or `mysqld-auto.cnf`) | Check | Effect on a real start | Seen / repaired? |
|---|---|---|---|
| A later `init_file=` (any prefix: `loose-`, `skip-`, `disable-`, `enable-`) in `~/.my.cnf` or its includes | **Caught** (startlock.go:972-979) | — | Locked=false → `KindMasterStartUnguarded` |
| `maximum-init-file` anywhere | **Caught** (955-959) | mysqld refuses to start | Alert |
| `user=` different in `~/.my.cnf` | **Caught** (641-643) | — | Alert |
| `!include` of a FIFO, or a huge file | Run fails on timeout or size → not locked | — | Alert (fails closed) |
| `read_only=0` / `super_read_only=0` / `transaction_read_only` | Not looked at | Harmless: applied before `init_file`, which turns read-only on. `transaction_read_only` only sets a session default | — |
| `persisted_globals_load=OFF` | Not looked at | Harmless for the lock: `init_file` still runs and its SET PERSIST still sets the globals **[unmeasured; well documented]** | — |
| `skip-grant-tables` | Not looked at | `init_file` still runs as far as I know, and `super_read_only` binds even with all privileges; it also forces `skip_networking` **[unmeasured]** | — |
| Persisted `read_only=OFF` in `mysqld-auto.cnf` | Not read by `--print-defaults` | Applied before `init_file`, so overridden | — |
| A persisted **static** `init_file` written straight into `mysqld-auto.cnf` (datadir, owned by uid mysql on every distro) | **Not caught**: `--print-defaults` never reads `mysqld-auto.cnf` | If mysqld honours it, persisted read-only variables are appended after the option files, so it would win and the agent's lock file would not run. I believe `init_file` is flagged non-persistable or persist-restricted in `sys_vars.cc`, and I do not know whether loading re-checks that **[unmeasured]** | Guard at start: `@@init_file` ≠ agent file → `bypassed`, flags off → `late`, read-only PERSISTed back within one tick (≤5 s after MySQL answers). The attacker's `init_file` has already run SQL with full privileges at bootstrap |
| `plugin_dir=<dir uid mysql can write>` + `plugin-load-add=x.so` | **Not caught**: printed, but not parsed (738-745) | Arbitrary code inside mysqld. It can flip read-only after the guard's start tick, or swallow the guard's SQL | Not repaired; flags in snapshot show it; **panel shows nothing** (1b) |
| `plugin-load-add` of a stock plugin with the stock `plugin_dir` | Not caught | Community plugins (`rewriter` rewrites DML only, `connection_control`, …) cannot unset read-only. Enterprise `audit_log` filtering can abort statements, including the guard's SET **[unmeasured]** | as above |
| `init_connect='SET GLOBAL super_read_only=OFF; …'` | Not caught | Runs for every client **without** CONNECTION_ADMIN/SUPER. It succeeds only if that client holds SYSTEM_VARIABLES_ADMIN, and such a client could run the SET itself | Same as 1b |
| `event_scheduler=ON` (the 8.0 default) + a stored event doing `SET GLOBAL super_read_only=OFF, read_only=OFF` | Not caught (the event is data, not an option) | Runs as its definer after start. Whether the scheduler runs an event body under `super_read_only=ON` (its `LAST_EXECUTED` update is a dictionary write) is **[unmeasured]** | Not repaired; panel shows nothing (1b) |
| `skip-…` of other options (`skip-log-bin`, `skip-networking`, …) | Not caught | Do not affect read-only | — |

Measurements that would settle the [unmeasured] rows (one mysqld, MySQL 8.0 and 8.4):

- (a) Put `"mysql_static_variables": {"init_file": {...}}` by hand into `mysqld-auto.cnf`, start the server, and read `@@init_file`.
- (b) Run `SET PERSIST_ONLY init_file=…` and note the error.
- (c) Start with the lock and an enabled event that does `SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF` every second. Read the flags after 10 s.
- (d) Put `plugin_dir`/`plugin-load-add` in `~/.my.cnf` with a trivial daemon plugin whose thread sets the flags off after 30 s.
- (e) Run `skip-grant-tables` together with the lock.

### 1a — comes up writable: NOT a vulnerability beyond the recorded trust limit

Scenario: uid mysql writes `~/.my.cnf` (on RHEL-family systems its home is the datadir) or `mysqld-auto.cnf`. If it makes MySQL start without the lock (row "static init_file"), the guard sees the start on its first tick after MySQL answers. It reports `late`/`bypassed` (`KindMasterStartLate`/`Bypassed`) and PERSISTs read-only ON. The window is about 5 s plus connection time.

A hostile service account has stronger levers anyway:
- it owns the data files;
- it can kill mysqld and start its own `mysqld --read-only=0` as uid mysql, on the same datadir, while systemd restarts the service;
- it can ptrace its own processes, including run 2 of `--print-defaults`, so it can forge that run's output.

That limit is already recorded: "The MySQL service account owns the data directory: truthfulness against it is best effort" (audit-agent-notes.md, residual risks), and R-113 is noted in the inventory draft. Recorded-risk wording to add:

> The start-lock check reads only `init_file`, `user` and `log_error` from the option files and never reads `mysqld-auto.cnf`. A service account that writes its own option files or `mysqld-auto.cnf` (for example a persisted static `init_file`, or `plugin_dir` plus `plugin-load-add`) can make a quarantined MySQL start without the lock. The guard then reports the start as `late`/`bypassed` and puts read-only back at its first tick. The start lock protects against an unattended or accidental start, not against the service account.

### 1b — becomes writable while mysqld stays up: CONFIRMED, Low

- **Who and what:** any of the following.
  - A local MySQL account with SYSTEM_VARIABLES_ADMIN, directly or through an event it defined.
  - uid mysql, through a plugin it loads (above).
  - The agent's own short read-only lifts with no mark: `dropDatabases` (databases.go:101), `configure` (configure.go:449), account creation (users.go:315). If their `restore` fails, read-only stays off.
- **What is lost:** an old master that applications can write to while the panel shows "dikarantina (hanya-baca)". This is the split-brain the quarantine exists to prevent.
- **Evidence:** guard early return at quarantine.go:302; the comment at quarantine.go:244-249 ("While MySQL stays up it does nothing … A MySQL that starts writable … stays so until the guard's next tick" — true only for a start); panel status from the store, not the snapshot (status.go:290-299, state.go:412); no alert rule for a quarantined server with `super_read_only` off (alerts.go:171-181, 353-372, 541-542).
- **What limits it:**
  - The panel cannot cause it: `mysql.setReadOnly{off}` is refused on quarantined (pins.go refusedByRole `setReadOnly`), and `mysql.connect` is local-only after A-2.
  - The flags are in every snapshot, and the next MySQL restart triggers the guard.
  - Whoever can flip them already holds high MySQL privileges or uid mysql.
- **Smallest fix (either):**
  - (Panel, smaller) An alert rule: `Role == quarantined && Snap.MySQLOK && !Snap.SuperReadOnly` and no active rebuild job for the server. It opens a Crit alert ("master lama bisa ditulis"), and the derived status should not say "hanya-baca" while the snapshot says otherwise.
  - (Agent) In `guardQuarantined`, read the two flags on every tick (one cheap SELECT). Re-PERSIST them and report when they are off and no rebuild mark defers. This needs marks on the three nil-mark `liftReadOnly` callers, or the guard would race `dropDatabases` and account creation.
- **Also:** correct SEC-192 in the draft, which says enforcement happens within one round.

---

## Item 2 — `mysql.salvage` (and `mysql.leftover`) run `mysqlbinlog` as root

### Facts

- **User:** root. `h.Exec` without a credential (salvage.go:303 → realhost.go:239-265). Arguments: `--no-defaults --include-gtids=<set> --skip-gtids --verbose <paths>` (salvage.go:302), so no option file is read. `mysql.leftover` does the same with `--no-defaults --base64-output=DECODE-ROWS --verbose` (leftover.go:135, run through `streamBinlog` → `h.Exec`, leftover.go:~297-302, timeout 120 s).
- **Binary:** `findClientTool`. It looks next to `mysqld` first, then on PATH (dump.go:277-289); both are root-owned locations.
- **Paths — agent-derived, never panel-given:** `binlogFilesFor` (leftover.go:156-237).
  - Directory: `filepath.Dir(@@global.log_bin_basename)` (157-163, 231). The server reports it from its options, which the service account can set through `~/.my.cnf` `log-bin=` at the next start.
  - File names: from `SHOW BINARY LOGS` (`binaryLogs`, 240-266), which mysqld reads from its index file. uid mysql owns that index file.
  - Validation: only `^[A-Za-z0-9._-]+$` (148), so no `/`, but `.` and `..` pass and resolve to a directory, which mysqlbinlog then fails on.
  - The file range is picked from `SHOW BINLOG EVENTS IN '<name>' LIMIT 2`, which mysqld runs as uid mysql (`previousGTIDs`, 270ff).
- **Symlinks / special files:** no check at all before root's `mysqlbinlog` opens the paths. mysqld reading each file first (as uid mysql) does not protect anything: uid mysql owns the directory and can swap a file for a symlink between the `SHOW BINLOG EVENTS` reads and the `mysqlbinlog` run (TOCTOU).
- **Panel input:** only the GTID set. It must be non-empty (salvage.go:88-94), a subset of `@@gtid_executed`, and a subset of the last `mysql.leftover` result (salvage.go:229-248).
- **Output:** a temp file created `O_EXCL|O_NOFOLLOW` 0600 (salvage.go:288) inside `/var/lib/dbpanel/salvage`. That directory is opened as an `os.Root` and must be root-owned 0700, not a link (salvage.go:192-213). The temp file is renamed to `<time>-<server_id>.sql` (319, 448-458). Caps: 2 GiB per file, 8 GiB per directory (47-52, 268-275, 301). The panel reads it in 512 KiB chunks through `mysql.salvageRead` (123-138, 532-571), and `regularSalvage`/`O_NOFOLLOW` keep that to regular files in the directory.
- **Error text to the panel:** `"salvage: mysqlbinlog gagal: <exit>: <last 4096 bytes of stderr>"` (salvage.go:310; realhost.go:261-263, 237).
- **Role rules:**
  - `mysql.leftover` and `mysql.salvage` only on `quarantined` (pins.go:865-868).
  - `salvageList`/`Read`/`Delete` on `quarantined` or `replica` (pins.go:869-873).
  - All five are `strictAllowed`, so no approval is needed (strict.go:51-55).
  - `allowSalvage` refuses without pins (salvage.go:164-169). One salvage runs at a time (`salvageRun.TryLock`, 102-105).

### Verdict: CONFIRMED, Low

**Scenario A — hostile service account.** uid mysql renames `binlog.000123` and puts a symlink in its place, or puts a FIFO there, or sets `log-bin` to a directory it prepared. It does this after `SHOW BINLOG EVENTS`, or at any time for files outside the range the panel asks for. When an operator runs "Amankan" or "Periksa data tertinggal", root opens the target:
- **Device:** opening `/dev/watchdog` (or `/dev/watchdog0`) arms a watchdog that is not already held (a held one answers EBUSY). mysqlbinlog then fails the magic check and closes it without the magic `V`, and the machine reboots after the watchdog timeout. Other device opens with side effects (tape rewind, `/dev/sr0` tray) are similar in kind. This raises the service account's reach from "stop MySQL" to "reboot the host". It needs a free watchdog device and an operator-triggered salvage/leftover.
- **FIFO:** root's `mysqlbinlog` blocks until the context kills it: 30 min for salvage (salvage.go:62, 299), 120 s for leftover (leftover.go:36). It holds `salvageRun`, not `promoteMu`, so it only stops the rescue.
- **Regular root-only file:** mysqlbinlog checks the 4-byte binlog magic. A non-binlog fails with a generic error (`File is not a binary log file` / `Could not read entry at offset …`) that does not echo content **[unmeasured; matches my reading of mysqlbinlog]**. Content comes out only if the target is itself a binlog-format file root can read and uid mysql cannot, and only for events inside the allowed GTID set. That is negligible disclosure.
- **Crafted binlog:** a parser bug in `mysqlbinlog` would run as root. I know of no specific bug; it is a generic exposure of a large C++ parser to input from a less-trusted account.
- **Crafted content:** the service account can also put arbitrary events (GTIDs in the allowed set) into the salvage file, which the operator then replays on the new master by hand. The salvage file is "what this server holds", and the service account controls that anyway. This is a recorded-risk point for the operator guide ("read before replaying"), not a code issue.

**Scenario B — compromised panel alone, with a source pin:** it cannot steer paths or read arbitrary files. Paths come from MySQL, names are validated, `--no-defaults` blocks `--defaults-extra-file` tricks, and the GTID set is bounded. No disclosure beyond the leftover transactions, which is the feature's purpose.

**Smallest fix:** run both `mysqlbinlog` invocations as the service account. `accountCredential(serviceHome(user))` already exists (startlock.go:533-549, 650-672), and `ExecEnvAs` (realhost.go:247) can carry it. uid mysql can read its own binlogs, and root's writer still receives stdout through the pipe. If that is not wanted: open every path in the agent with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK`, require a regular file owned by the service account inside the binlog directory, and feed it to `mysqlbinlog` as `/dev/fd/N` (or one `-` on stdin per run).

### New finding 2-new: the salvage bound is void on a quarantined server without a source pin (Low)

- `mysql.leftover` calls `pinnedNewMaster` (leftover.go:82-94). **Without a source pin it returns the panel's `NewMaster` unchanged** (88-90). `sourceOf` does not list `mysql.leftover` (pins.go:829-860), so the unsourced-quarantine refusal (pins.go:804) does not apply to it.
- Sequence: root has run `set-role quarantined` without `set-source` (reachable: cli.go:368-382 accepts it, `SetRole` keeps no source after `master`). Then a compromised panel does three things:
  - It sends `mysql.leftover{new_master: <its own MySQL answering gtid_executed=''>}`. leftover.go:110-122 computes `GTID_SUBTRACT(@@gtid_executed, '')`, which is everything, and `recordLeftover` stores it (leftover.go:66).
  - It sends `mysql.salvage{gtid_set: <that>}`, which extracts every transaction still in the binary log, with row data (no `DECODE-ROWS` in salvage.go:302).
  - It downloads the result with `mysql.salvageRead`.
- All three commands are `strictAllowed`. This contradicts the bound promised at salvage.go:30-37 ("a compromised panel cannot read this server's binary log history through it"). For hospital data this is patient-record disclosure.
- **Limit:** it needs root to have left the server quarantined without a source; `SetQuarantinedAt` always writes one.
- **Fix:** refuse `mysql.leftover` (and `mysql.salvage`) on a quarantined server without a source pin, with `unsourcedQuarantineText`. For example, add `cmd == "mysql.leftover"` beside `mysql.dropDatabases` in the pins.go:804 condition.

---

## Item 3 — does the agent refuse a rebuild command that names another source?

### Trace

Every panel command passes `Registry.Dispatch` → gate `strictGate` (driver.go:192; registry.go:139-140) → the handler, which calls `d.allow` → `Pins.Allow` (driver.go:222-228; pins.go:771-827).

- **Gate (strict.go:123-160), for every `strictProtected` command:**
  - It needs a source pin (138-140).
  - `CheckRebuildApproval` (rebuild_approval.go:282-311) reloads the approval and the pins on each call. It resolves the **pinned** host now (DNS), and `checkApproval` (141-175) requires `approval.Source == pinned source` (case-insensitive host, numeric port) and **every** resolved address == `approval.Addr`, within its validity.
- **Allow:** `sourceOf` (pins.go:829-860) gives host:port for `configureReplication`, `dumpFrom`, `cloneFrom` (and `promote`). `Allow` refuses when the pin differs (pins.go:819-822) and refuses source-naming commands on an unsourced quarantined server (pins.go:804-806).

| Command a rebuild sends to the old master | Gate (approval vs pin, DNS vs approved addr) | Allow (command host:port vs pin) | Dials |
|---|---|---|---|
| `mysql.dropDatabases` | yes | role only (local) | local |
| `mysql.dumpFrom` (job) | yes, at submission | **yes** (snapshot.go:56) | name `p.Host` (dump.go:107) |
| `mysql.cloneFrom` (job) | yes, at submission | **yes** (snapshot.go:56) | name, `clone_valid_donor_list` + `CLONE INSTANCE FROM` `p.Host:p.Port` (clone.go:70-71, 115) |
| `mysql.configureReplication` | yes | **yes** (replication.go:76) | name, `SOURCE_HOST` (replication.go:349) |
| `mysql.setReplicationFilter`, `resetReplica`, `configure`, `restart`, `createReplUser`, `createDumpUser`, `dropUser`, `sweepDumpUsers`, `tlsRequest`, `tlsInstall` | yes | role rules; local | local (or the panel for TLS) |
| `mysql.startReplica`, `mysql.startUntil` | yes | role only, **no source comparison** (replication.go:123-137, 212-235; `StartReplica` 507-522) | the channel already configured in MySQL |
| `mysql.probeSource`, `mysql.leftover` (allowed class) | — | — | the pinned source at its verified address (`SourceDial`/`dialAt`, promote.go:154, leftover.go:82-94) |
| `mysql.waitSynced` | — | — | local |

### Can the source change underneath?

- **Panel between approval and command:** no. The panel cannot write the pins of a quarantined server. `claimSource` writes only when no pin exists (pins.go:740-768), and Allow already refused source-naming commands there (804). `mysql.quarantine` refuses another source (quarantine.go:180-188).
- **Root between approval and command:** if root changes the pin with `set-source` (pins.go `SetSource`), every later protected command fails the gate (`InvalidSource`). If root changes the role with `set-role`, `ClearRebuild` runs (pins.go `SetRole`).
- **Between the copy and the configure step:** both commands are compared with the same pin. Settling also requires a load recorded from the same host:port (`loadedFrom`, quarantine.go:444-458) and, after a dump, that no uncopied databases remain (459-479, A-1 fix).
- **Approval consumed and kept correctly:**
  - The approval is kept for its 24 h and covers every protected command (spec 7.3).
  - It is cleared on settle (quarantine.go:491-495), on any `set-role`, and on entering quarantine from another role (pins.go `SetQuarantinedAt`).
  - It is inert on any role other than quarantined (`checkApproval` InvalidRole).

### Verdict: NOT a vulnerability

The agent compares every command that names a source, at each command, with the pin and the approval. Residuals already on record:
- the loads and the replication link connect by name and re-resolve (spec §8; A-4c), and for the replication link that holds for its whole life;
- a running dump/clone job continues after the approval is revoked or expires (audit-agent-notes residuals);
- the approval does not bind TLS settings, so peer authentication comes only from `ssl`/verify parameters the panel chooses.

Informational gap, which needs no change unless the owner wants it: `mysql.startReplica`/`mysql.startUntil` start whatever channel MySQL holds without comparing its `SOURCE_HOST`/`SOURCE_PORT` with the pin. Through the agent, a quarantined server can only get a channel from `configureReplication`, which is compared. A channel left from before the quarantine, or set by a local MySQL admin, would be started from another host under a valid approval. Smallest fix if wanted: in those two handlers, when the role is quarantined, read `SHOW REPLICA STATUS` and refuse unless `Source_Host`/`Source_Port` equal the pin (`sourceRefusal`).

---

## Item 4 — `!include /dev/watchdog` (device/FIFO) and the root run

What the root run reads at 549cd1c (startlock.go:612; realhost.go:247-265):
- **Environment:** the agent's environment with only `HOME` replaced by `/nonexistent` (`replaceEnv`, realhost.go:257, 269-282).
  - Under systemd: `MYSQL_HOME` is not set by `scripts/systemd/dbpanel-agent.service`, so it is not set, and `HOME` would be `/root` anyway (hidden by `ProtectHome=yes`).
  - From the CLI (`sudo dbpanel agent …`): root's environment, so any `MYSQL_HOME` there is root-chosen.
- **Arguments:** `--print-defaults` only, or `--defaults-file=<ConfigFile> --print-defaults` (startlock.go:583-588). No `--defaults-extra-file`.
- **Files read** (MySQL's default search, from my reading of `my_default.cc`): `/etc/my.cnf`, `/etc/mysql/my.cnf`, `SYSCONFDIR/my.cnf`, `$MYSQL_HOME/my.cnf` only if set, `~/.my.cnf` = `/nonexistent/.my.cnf` (absent), and the `!include`/`!includedir` targets of those files.
- **Conclusion:** the service account's home is never consulted by the root run. Root opens a file named by the service account only if a **root-owned** system option file includes a location the service account can write. That is a root misconfiguration; the managed config itself is never in the datadir (startlock.go:186-188 for the lock; the include directory is the package default).
- **`mysqld --validate-config`** (realhost.go:223-229) also runs as root, always with `--defaults-file`, so it reads no home either.

Bounds:
- Each run has 10 s (`printDefaultsTimeout`) and 256 KiB of output. Overflow cancels the run (startlock.go:682-698, 705-717).
- `exec.CommandContext` SIGKILLs mysqld on cancel. A process blocked in `open()` on a FIFO is killable, so the round ends in about 10 s.
- `checkStartLock` re-runs at most once a minute under `promoteMu`, and `watchBootFailure` reads at most once a minute (startlock_guard.go:328-343). Worst case the guard loop stalls about 10 s per run.

The service-uid run (run 2): a FIFO in `~/.my.cnf` blocks it until the timeout, so the result is "not locked" and an alert. That fails closed and costs alert noise only. `/dev/watchdog` is normally `root:root 0600`, so uid mysql cannot open it (EACCES). Any device uid mysql *can* open, it could open itself; there is no escalation.

**Verdict: NOT a vulnerability at 549cd1c.** Part A item A-3(a) closed the root-opens-a-service-account-path route for option files. One related point: the service-uid run's output is only as trustworthy as uid mysql, which can ptrace its own-uid process. That falls under the recorded "best effort against the service account" limit (item 1a).

Optional hardening, not required: run the root run with `MYSQL_HOME` removed from its environment as well, so a stray value in an operator's shell cannot add a directory.

---

## Item 5 — A-9

A-9 cannot be reconstructed. `final-fix-findings.md:129`, `audit-agent-notes.md:3` and `progress.md:187` record only "A-9 Informational / no change". Nothing in the surrounding text or the code points to its subject (`draft-audit-plan-5b1.md:562-564` reaches the same conclusion).
