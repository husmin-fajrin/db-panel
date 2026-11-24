# Audit notes — agent — Plan 5b-2 (head b950757)

Auditor: agent part of strict mode (Plans 5b-1, 5b-2a, 5b-2b). Read-only; the one file written is this one.
Severity counts: Kritis 0, Tinggi 0, Sedang 5 (one of them unconfirmed), Rendah 7, Info 2.

## Ground covered (files read in full; what was run)

Read in full, as they are at b950757:

- `internal/agent/drivers/mysql`: `strict.go`, `pins.go`, `rebuild_approval.go`, `startlock.go`, `startlock_guard.go`,
  `quarantine.go`, `lift.go`, `rebuild_load.go`, `dump.go`, `snapshot.go`, `clone.go`, `leftover.go`, `salvage.go`,
  `svcuid.go`, `connect.go`, `promote_lock.go`.
- `internal/agent`: `cli.go`, `cli_admin.go`, `cli_dropdb.go`, `cli_rootsql.go` (every `cli*.go`), `session.go`,
  `runtime.go`, `policy.go`; `commands/registry.go` (Dispatch and logging).
- `internal/agent/monitor`: `backup.go`, `monitor.go`, `changes.go`, `buffer.go`.
- `internal/proto`: all seven files.
- `scripts/install.sh`: the two units (lines 265-333) and the install steps around them.
- Parts only, where another file decided an answer: `configure.go` (setReadOnly, renderConfig), `promote.go` (open
  writes, finishPromote, resume), `databases.go` (dropDatabases), `realhost.go` (Exec, environment, stderr bound),
  `layout.go` (FindMysqld), `conn.go` (DSN), `health.go` (Snapshot), `users.go` (lift site), `jobs/runner.go` (Logf),
  the tests `system_test.go`, `testpaths_test.go`, `monitor/backup_test.go` (how they pick directories), `cli_test.go`
  (layout stubs).
- Documents: audit-common.md; rulings R1-R41; final-review-agent.md; final-fix-report.md; deferred-minors.md; the
  5b-1 audit (SEC-204/205/212/213/214, the DNS and set-role findings); the 5b-2a agent review's "Declined to judge";
  5b-2a spec sections 8 and 11; plan K1-K11 and "Audit keamanan"; Rencana-4 spec 5.1 on the lock; Rencana-5a spec
  "facts" (persisted values); README sections on set-role, lock/unlock-master and guide 9.

Run: `graphify query` (before the first read, and again for the CLI and the panel's redaction); a read of the Go
1.26.10 toolchain source (`os/file_unix.go:455-490`) to check the scan's lstat claim. No test was run, no MySQL was
started, no scratch program was needed. Everything below is confirmed by reading unless it says otherwise.

## Findings

### AG-1 — A compromised panel can use up the database server's memory through the agent (Sedang)

- Attacker: a compromised panel.
- Where: `internal/agent/session.go:82-104` (one goroutine per request, nothing bounds how many run);
  `session.go:17` (each message up to 1 MiB); `internal/agent/monitor/monitor.go:222-237` (`monitor.configure` waits
  on `checkMu` and takes a MySQL reading, up to 5 s when MySQL is slow); `monitor.go:281-284` (the backup folder is
  stored unchecked, any length); `monitor/backup.go:47` (the length check runs after `filepath.Clean` of the whole
  string); `scripts/install.sh:298-327` (the agent unit has no `MemoryMax`).
- Steps: the panel sends requests faster than their handlers end, for example `monitor.configure` with a 1 MiB
  `backup_folder`, or `mysql.salvageRead` (each answer about 700 KiB of JSON waiting on one write lock) while it does
  not read. The agent starts a goroutine for each and keeps its parameters and its answer until it is served. At
  network speed that is hundreds of megabytes per second.
- Impact: the root agent grows until the kernel's OOM killer acts. It may pick mysqld (the largest process) on a
  pinned master, which the pins otherwise never let the panel restart (`pins.go:964-979`). No data is lost (InnoDB
  recovers); the hospital's database stops. `monitor.configure` is `strictAllowed`, so this also works on a
  quarantined server.
- Confirmed: by reading only. A test that floods a fake session and watches the goroutine count would confirm it.
- Not new in this plan: the unbounded dispatch is older. This plan adds the first free-form parameter to an always
  allowed command. No earlier audit or risk row names it (searched the 5b-1 audit and the risk inventory).
- Smallest fix: a counting semaphore in `serve` (for example 64 requests in flight; stop reading the socket while it is
  full); refuse a `backup_folder` over 1024 bytes before it is stored (keep "any folder is accepted" for the rest);
  put the length check first in `ValidBackupFolder`. `MemoryMax=` in the unit is a second line. No spec decision
  changes.

### AG-2 — `set-role quarantined` asks nothing and its output hides what happens next (Sedang)

- Attacker: an operator in a hurry.
- Where: `internal/agent/cli.go:391-449` (the question is asked only when leaving `quarantined`; entering it prints
  only `peran diperbarui; agent membacanya ulang pada perintah berikutnya`, `cli.go:292, 440`);
  `drivers/mysql/promote_lock.go:567-571`, `quarantine.go:276-424` (within one guard round, 5 s, both flags are
  PERSISTed ON), `startlock_guard.go:155-156` (the start lock is written into the MySQL config).
- Steps: the operator runs guide 9's step 4 in the wrong terminal, on the server that is the master now. Nothing is
  asked. Five seconds later that master refuses every write, also after a restart.
- Impact: the hospital's database stops taking writes until someone runs `set-role master` (answer `ya`) and
  `unlock-master`. That pair leaves the saved `read_only` (AG-4), so the same master refuses application writes again
  after its next MySQL restart. No data is lost and no second writer appears.
- What already limits it: README and guide 9 say "Perintah ini tidak bertanya" and put a `hostname` check before it
  (fix-wave item W-5). Ruling R34 allowed the command because it moves toward read-only.
- Smallest fix: when the role read is `master`, print the hostname, "MySQL server ini akan dibuat baca-saja dalam
  beberapa detik dan tetap begitu sesudah restart", and ask for `ya` (`--ya` skips it, as for the other direction).
  At least change the printed line, which today says nothing happens until "the next command". A prompt is new
  operator text: owner's decision, not a spec change.

### AG-3 — Leaving the `master` pin asks nothing, and the agent's own refusal names the command (Sedang)

- Attacker: a compromised panel together with an operator in a hurry.
- Where: `cli.go:395` (no question when the role read is `master`); `pins.go:179-183` (`roleRefusal`: "server ini
  dipin sebagai master; perintah … ditolak. Untuk mengubah peran jalankan di server ini: sudo dbpanel agent set-role
  <peran>"); `pins.go:112-125` (a replica refuses nothing fixed); `dump.go:95-110`, `databases.go:86-91`,
  `clone.go:62-67` (after the pin is gone the only guard left is "no replica is reading the binary log now");
  `strict.go:64` and the role table (`mysql.stopReplica` is allowed everywhere).
- Steps: (1) the panel sends `mysql.dumpFrom` or `mysql.dropDatabases` to the production master; the agent refuses
  with the text above, which the panel shows. (2) The operator runs `sudo dbpanel agent set-role replica` (or
  `unassigned`) on the master; it prints one line and asks nothing. (3) The panel stops the replicas
  (`mysql.stopReplica`), then drops the master's databases, or loads a dump over them after `RESET` of its binary
  logs, or clones over it.
- Impact: the production master's data is lost. Before step 2 the pins stop every one of those commands.
- Why Sedang and not Tinggi: root on the server is trusted, the command is root's own, and a compromised panel can
  show any instruction on its own pages. The gap is that the last check on the server is silent in this direction,
  while the same CLI asks and explains when leaving `quarantined`.
- Smallest fix: ask for `ya` when the role read is `master` and the new role is `replica` or `unassigned`, with one
  sentence like the quarantined ones ("panel boleh membuang data server ini dan menyalin ulang tanpa persetujuan di
  server ini"); reword `roleRefusal` for a master so it does not read as the remedy ("Bila server ini memang bukan
  lagi master …"). New operator text; owner's decision.

### AG-4 — R38: the saved `read_only` after `unlock-master` is an oversight, and the proposed fix is unsafe as written (Sedang)

- Attacker: none needed; an operator who follows the commands' printed lines.
- Where: `promote_lock.go:503-510` (`unlockMaster`), `cli.go:828-829` (the CLI's own copy), `quarantine.go:211-218`
  (`persistReadOnly`), `startlock.go:41` (the start lock file PERSISTs both at every start), `cli.go:352`
  (`setRoleNextMaster`), `cli.go:837` ("master dibuka: MySQL bisa ditulis …").
- Fact 1: a server that was quarantined and is made master again with `set-role master` + `unlock-master` keeps
  `read_only=ON` saved. After the next MySQL restart `read_only` is ON and `super_read_only` OFF: every application
  account without SUPER or CONNECTION_ADMIN gets ERROR 1290. The final agent review found that the panel then shows
  the master healthy (it reads only `super_read_only`; panel side, not checked by me).
- Fact 2: the two other paths that open a server do it cleanly with `SET PERSIST … = OFF` for both flags:
  `configure.go:609-630` (`mysql.setReadOnly{on:false}`, the panel's "Buka kunci master") and `promote.go:374-377`.
  Only `unlockMaster` and the CLI use `RESET PERSIST`.
- Fact 3, new: a promoted replica keeps the replica lines `read_only=ON` and `super_read_only=ON` in its managed
  config (`configure.go:308-310`; `finishPromote`, `promote.go:632-671`, does not rewrite the block; the panel sends
  `mysql.configure` only from create_replica.go:671 and prepare_master.go:103). It stays writable across restarts
  only because the promotion saved both flags OFF (5a spec, "Dari eksperimen": the saved value beats the option
  file). So the fix the final agent review proposed, adding `RESET PERSIST IF EXISTS read_only`, would remove the
  saved `read_only=OFF` of every once-promoted master at its first cancelled or expired planned promote (or
  `lock-master`/`unlock-master`), and the option file's `read_only=ON`/`super_read_only=ON` would apply at the next
  restart: a master that refuses all writes.
- Impact today: an outage at a later MySQL restart, possibly weeks after the operator's action; no data loss, no
  second writer.
- Smallest fix: in both places replace the three statements by `SET PERSIST super_read_only = OFF` and
  `SET PERSIST read_only = OFF` (what `setReadOnly(false)` sends), with one real-mysqld test that restarts MySQL after
  the unlock on three servers: once quarantined, once promoted, never a replica. Then remove the hand-run
  `SET PERSIST` steps from README and guides 5 to 7. Until then, without touching lock code: `unlock-master` reads
  `performance_schema.persisted_variables` and says when a saved `read_only` remains and how to remove it.
- Spec: Rencana-4 spec 5.1 names the statements ("RESET PERSIST super_read_only; SET GLOBAL …"), so the change is a
  spec text change for the owner. The answers to R38 (a) to (d) are under question 7.

### AG-5 — After a lock and unlock on a once-promoted master, the state at the next restart is not measured (Sedang, unconfirmed)

- Attacker: none needed.
- Where: `promote_lock.go:258` (`SET PERSIST super_read_only = ON` replaces the saved OFF of a promoted master),
  `promote_lock.go:504` and `cli.go:828` (`RESET PERSIST IF EXISTS super_read_only` then removes the saved entry, it
  does not put the saved OFF back), `configure.go:309` (the option file still says `super_read_only=ON`).
- Steps: any master that was promoted by the panel; a planned promote is started and cancelled, or its lock expires
  and the guard opens it, or root runs `lock-master` then `unlock-master`. Saved afterwards: `read_only=OFF` only.
  Option file: `read_only=ON`, `super_read_only=ON`.
- What is not known: whether MySQL, applying the saved `read_only=OFF` at start, also clears the option file's
  `super_read_only` (as a `SET GLOBAL read_only = OFF` does while running, plan evidence M1a) or leaves it ON. If it
  leaves it ON, the master comes up refusing all writes after its next restart. The spec's sentence "master kembali
  persis seperti sebelum kunci" is not true for such a master either way.
- Confirmed: the three code facts by reading. The restart behaviour is unconfirmed; the audit rules forbid starting
  MySQL. One testmysql run confirms it: configure as replica, promote, `lockForPromote`, `unlockPromote`, restart,
  read both flags.
- Fix: the same change as AG-4 closes it whatever the measurement says.

### AG-6 — A panel that sends `monitor.configure` often stops the agent's check loop (Rendah)

- Attacker: a compromised panel.
- Where: `monitor.go:290-293` (every accepted configure signals `reconf`), `monitor.go:491-500` (`pause` starts its
  whole wait over on that signal), `monitor.go:338-346` (the tick, the dump-account sweep, the salvage sweep and the
  backup scan all sit behind that pause).
- Steps: send `monitor.configure` every 9 seconds (the shortest interval is 10).
- Impact: no scheduled health check, no backup scan, no sweep of stale dump accounts, and no removal of salvage
  files after their 7 days (they hold row data). Reports still leave as answers to the configures, and the guard loop
  is a separate goroutine and keeps running, so a quarantined server stays locked.
- Smallest fix: keep the deadline across a `reconf`; only shorten it when the new interval ends sooner.

### AG-7 — The backup report tells a compromised panel what exists anywhere outside eight path prefixes (Rendah)

- Attacker: a compromised panel that chooses the backup folder.
- Where: `monitor/backup.go:36-65` (the rule), `:98-110` (reason codes), `:78-84` (age and size), `:147-155`.
- What it learns per path: whether it exists (`missing`), is a directory (`not_dir`), holds a regular file within
  three levels (`none` or `ok`), holds more than 10,000 entries (`limit`, `truncated`), and the newest regular file's
  age to the second and size to the byte. No name, no content.
- About which paths: everything except `/proc`, `/sys`, `/dev`, `/home`, `/root`, `/run/user`, `/tmp`, `/var/tmp`
  and `/`. That includes `/etc`, `/usr`, `/boot`, `/var/lib/<other service>`, `/var/lib/mysql/<database>`,
  `/var/spool/cron/crontabs/<user>`, `/var/lib/dpkg` (when packages were last updated), `/etc/dbpanel`.
- `/var` and `/run` pass the rule although `/var/tmp` and `/run/user` lie two levels below them; only the unit's
  `PrivateTmp` and `ProtectHome` (`install.sh:316-317`) make those empty for the agent.
- Rate: one scan per check round, so at most about six paths a minute (see question 1).
- Impact: reconnaissance of the database server (software, users, patch recency). `probe` and `mysql.inventory`
  already give the panel the OS, kernel and database list, so the gain is small. README "Keamanan" now states the
  list (fix-wave F10).
- Options for the owner, none needed for merge: (a) accept, as documented; (b) refuse `/var` and `/run` themselves
  so the rule does not rest on the unit; (c) refuse the system trees (`/etc`, `/usr`, `/bin`, `/sbin`, `/lib*`,
  `/boot`); (d) send `missing`, `not_dir` and `denied` as one code and keep the detail in the agent log (costs the
  operator the sentence "itu bukan folder"). (b) is one line and costs nothing.

### AG-8 — Database names go raw into the advice that tells the operator to run `drop-database` (Rendah)

- Attacker: someone who can create a database on the old master (a MySQL account with CREATE; a SUPER holder).
- Where: `rebuild_load.go:258-309` (T8a, T8b: the names are joined as they are; no `printable`, no `plainName`
  check), against `cli_dropdb.go:512-536` (the CLI itself quotes or withholds such names, ruling D55).
- Steps: a database is created with a name such as `x;sudo dbpanel agent set-role master --ya` (41 of 64
  characters). A later rebuild that does not copy it ends with "… jalankan di server ini sudo dbpanel agent
  drop-database <nama> untuk tiap nama …" and that name in the list. An operator who types the name unquoted after
  the command runs the second command with a cached sudo.
- Impact: at worst the old master is pinned master without a question (`--ya`), the first step to two masters.
- Why Rendah: the precondition is unlikely (CREATE DATABASE for the attacker, a rebuild by named databases or a
  leftover-only database, and a visibly odd name pasted unquoted). How the panel shows the line (its censoring of
  quoted text, K9) is the web auditor's part; not checked by me.
- Smallest fix: in T8a and T8b list only names that match `plainName`; for the others say how many there are and to
  read them with `mysql -e 'SHOW DATABASES'`.

### AG-9 — The agent's own client programs inherit root's environment and login file when the unit's hardening is absent (Rendah)

- Attacker: none; a stale file or variable of root's.
- Where: `realhost.go:260-283` (`ExecEnvAs` passes the whole environment; `MYSQL_TEST_LOGIN_FILE` is not set),
  against `cli_rootsql.go:172-184` (the CLI strips `MYSQL_*` and points the login file at `/dev/null`; it does not
  strip `LIBMYSQL_*`).
- Steps: the agent runs outside the shipped unit (started by hand, or a unit without `ProtectHome=yes`), and
  `/root/.mylogin.cnf` holds a `[client]` host. `cli_rootsql.go:144-146` records that an 8.0 client reads that file
  even after `--defaults-file`, and its host wins.
- Impact: a rebuild's `mysqldump` copies from the host in that file, not from the pinned and approved source.
- Under the shipped unit: sound (`/root` is empty for the agent).
- Smallest fix: set `MYSQL_TEST_LOGIN_FILE=/dev/null` and drop `MYSQL_*` and `LIBMYSQL_*` for every program
  `ExecEnvAs` starts. This is K9's environment item and the 5b-2a declined item 7.

### AG-10 — The window of the operator commands does not put read-only back when `do` panics (Rendah)

- Where: `cli_rootsql.go:436-452` (`putBack` and `finalCheck` run after `do`, not in a `defer`).
- Steps: a panic between `SET GLOBAL super_read_only = OFF` and step 5.
- Impact: `super_read_only` stays off until the agent's guard puts it back (one round, with a Kritis alert and T7 for
  a running rebuild), or until MySQL restarts when no agent runs. `read_only` stays on throughout.
- Smallest fix: defer the put-back. This is K9's `putBack`/`finalCheck` item; it holds as described there.

### AG-11 — The guard re-reads the listed replicas of a promote lock by name (Rendah; may repeat an earlier residual)

- Attacker: someone who controls DNS, with certificate verification between servers off.
- Where: `promote_lock.go:335-353` (targets keep the host as the processlist showed it, a name unless
  `skip_name_resolve`), `:361-390` (`readListed` dials that name again), against the candidate, which is read at the
  address recorded in the lock (`:626-638`).
- Steps: a planned promote's lock stands on master M; another replica R is promoted in an emergency while M is cut
  off; at the deadline M's guard reads R by name, DNS answers a decoy that accepts any login and reports read-only;
  the guard opens M.
- Impact: two masters once the network heals.
- Unconfirmed against the 5a audit's residual list (R-033, R-108 are near it); I did not read that audit in full.
- Smallest fix: record with each listed replica the address it was read at when the lock was set, and dial that.

### AG-12 — One approval covers every protected command for 24 hours and outlives a failed or cancelled rebuild (Rendah)

- Attacker: a compromised panel, after root approved once.
- Where: `rebuild_approval.go:141-175` (what is checked), `strict.go:123-160` (every protected command passes),
  `quarantine.go:704` and `pins.go:472, 536` (the only removals: a finished rebuild, any `set-role`, a new
  quarantine), `cli.go:461-473` (`approve batal`).
- Steps: root approves; the rebuild fails or the operator cancels it in the panel; within 24 hours the panel sends
  `mysql.dropDatabases` for every database of the old master.
- Impact: what the old master still held, including transactions not yet saved with "Amankan data tertinggal", is
  dropped. The approve screen says this is what an approval allows, and names `approve batal`.
- Not a broken boundary: root approved exactly this. It is listed so that the owner sees that cancelling a rebuild
  in the panel does not end the approval.
- Smallest fix, optional: one sentence where the panel cancels a rebuild ("jalankan sudo dbpanel agent approve
  batal di server itu"); panel side.

### AG-13 — The agent does not bound the size of what it reports (Info, unconfirmed)

- Where: `monitor.go:595-604` (a health report is sent whatever its size), `leftover.go:507-523` (every touched
  table is listed), against the panel's 1 MiB read limit.
- Steps: a SUPER holder gives the server a very large `gtid_executed` (thousands of UUIDs), or a leftover touches
  about ten thousand tables.
- Impact: the panel closes the connection on every report; the agent reconnects in a loop and the server reads as
  offline. A test with a report over 1 MiB would confirm it.

### AG-14 — Test directories stay under `/var/lib` when a run is killed (Info)

- Where: `monitor/backup_test.go:56-73` (`/var/lib/dbpanel-backup-test-*`, else `/srv`, `/opt`, `/mnt`, `/run`),
  panel `e2e_gate_test.go:287-301` (`/var/lib/dbpanel-e2e-backup-*`).
- They are new, uniquely named directories beside `/var/lib/dbpanel`, never inside it; a kill leaves them. Harmless,
  except that a clean-up such as `rm -rf /var/lib/dbpanel*` would also remove the real state directory. See
  question 12.

## Answers to the questions (one by one)

### 1. The backup folder scan under the added attackers

Sound, with AG-6 and AG-7.

- Steered outside the allowed places: no. The configured string is checked (`backup.go:369`), the folder is opened by
  the kernel, the resolved path is read back from `/proc/self/fd/N` and checked again (`:185-194`), and the walk
  descends from that descriptor only (`:195-204`, `SameFile`). A link through `/proc/<pid>/root` resolves to a path
  that is checked the same way. R14 stands: a symlinked folder gives the panel no place it could not name directly.
- Follow a link inside the folder: no. Each subdirectory is opened one component at a time with
  `O_DIRECTORY|O_NOFOLLOW` from the folder's descriptor (`:309-339`); entries are lstat'ed from their directory at
  listing time (checked in the toolchain: `os/file_unix.go:475-482`, `parent.inRoot`), so `Info()` is never a path
  lookup.
- Read file contents: no. `Open` is refused (`:272-274`); only directories are opened.
- Hang the agent: no. One scan goroutine exists at a time; the check loop waits 10 s once and then skips while that
  goroutine is stuck (`:375-378, 399-405`). A hung mount costs one goroutine and one thread until it returns.
- Starve the guard loop: no. The guard loop is its own goroutine (`monitor.go:327-330, 353-364`) and shares no lock
  with the scan or with `checkMu`; `GuardEvery` is a constant the panel cannot change.
- Run as often as the panel likes: no. At most one scan per check round (10 s at the shortest), and each folder
  change restarts the round's wait. The 5-minute rule binds an unchanged folder only. One scan reads at most about
  30,000 entries and opens at most 10,000 directories. A panel that configures too often gets no scan at all (AG-6).
- What the report discloses, and about which paths: AG-7.
- Acceptable for a root agent: yes for merge, as documented: metadata only, bounded, slow, and small beside what
  `probe` and `mysql.inventory` already give the same panel. The owner may tighten it (AG-7 options).
- The unit: `PrivateTmp=yes` and `ProtectHome=yes` are what make the five "hidden" trees empty; the unit has no
  `ProtectSystem` and no `ReadOnlyPaths`, so they change nothing else. Without the unit the path rule still refuses
  the eight prefixes by name; the one gap is that `/var` and `/run` then cover `/var/tmp` and `/run/user` (AG-7 b).
- Someone who can write into the folder can fake a fresh backup (inherent to an age check), hide the newest file
  behind 10,000 earlier names (false red; the fix wave now says so on the card), or stall the scan with a FUSE mount
  that allows root (false red, one stuck goroutine until the agent restarts). None of these opens anything.

### 2. `monitor.configure` as an entry point

Sound, with AG-1 and AG-6.

| Parameter | Bounds | Worst a hostile value does |
|---|---|---|
| `check_every_sec` | 10 to 3600, applied as at most 300 (`monitor.go:250-275`) | Checks every 10 s (light), or every 5 minutes (changes reported later). The guard is not affected. |
| `summary_every_sec` | 10 to 3600 | More or fewer summary reports. |
| `lag_threshold_sec` | 0 (keep) or 10 to 3600 | Moves when a lag crossing is reported at once. |
| `role_hint` | `master`, `replica`, empty; `quarantined` is taken as empty; anything else refused | Pins a role only on a server whose agent process never saw a role file (`pins.go:648-683`), and only as MySQL's state agrees. An existing pin, quarantined included, is never changed. |
| `backup_folder` | none at this point (any string up to the 1 MiB frame); over 1024 bytes is reported `invalid` and never scanned | Question 1; AG-1 for its size. |

- A quarantined server: the command is `strictAllowed` (`strict.go:34`) and safe there. Bootstrap returns before
  writing (`pins.go:657`), nothing it sets reaches the guard, and with unreadable pins it only logs an error per
  check.
- Empty parameters are refused (`monitor.go:224-226`); a parameter error leaves every setting as it was.

### 3. SEC-204, SEC-212, SEC-213, SEC-214 against the code now

- SEC-204 (race between the proof and the listing): **still open, narrowed.** The proof and the listing run on one
  connection (`rebuild_load.go:418-436`), but the proof still reads the grant tables by name while the listing uses
  what the connection got at login. What changed: the guard reads both flags every round (`quarantine.go:337-347`)
  and records a server found writable (`:379, 392, 404`), and the settle reads the flags itself under the lift lock
  (`:667-691`). A SUPER holder must open `super_read_only`, change the grants and close it again inside one 5 s
  round, exactly at the settle. No comparison with the schema directories was built. Outcome if won: old databases
  stay on the replica; no data lost. Now Rendah.
- SEC-212 (`RENAME TABLE` into a copied database during the load): **still open, narrowed.** A lift turns off only
  `super_read_only` (`lift.go:296`), so only an account with CONNECTION_ADMIN or SUPER can write during the load. A
  full dump is cross-checked against the source's two listings (`dump.go:146-153, 190-203`;
  `rebuild_load.go:345-357`), which is per database: a table moved into a database the load created still counts as
  copied.
- SEC-213 (system schemas not compared): **unchanged, by design** (`rebuild_load.go:621`). Worth saying once more
  for the owner: after a rebuild by named databases the old master's `mysql` schema stays, so its accounts stay,
  including any account created there while it was writable; they travel with that server if it is promoted later.
  A full dump or CLONE replaces them.
- SEC-214 (privileges through roles not counted): **unchanged in the rule, easier for the operator.** The proof and
  `grant-admin` decide in one function (`rebuild_load.go:546-559`, `connect.go:213-235`), the refusal names the
  command (T16), and `grant-admin` grants directly. It still fails closed.

### 4. The "Declined to judge" list of the 5b-2a agent review

1. `mysql.quarantine` on a quarantined server and `mysql.setReadOnly{on:true}` persist the flags without the lift
   lock: **holds** (`quarantine.go:193-205`, `configure.go:609-630`; both `strictAllowed`). A compromised panel, or
   a pending quarantine resent when the agent reconnects, can fail a running load with ERROR 1290 after its data was
   partly replaced. The server stays quarantined and read-only; the rebuild is started again. Rendah, safe direction.
2. The older paths that send `read_only = OFF`: judged under questions 7 and 8. All are refused on a quarantined
   server; AG-4 and AG-5 are what I found in them.
3. `--tanpa-cadangan` after any connected dump that exits 2 or 3: **holds as decided.** P1 prints the command
   (`cli_dropdb.go:36, 521-527`), P2 asks for the database's name (`:39, 332-345`). A hurried operator can lose one
   database on a quarantined server after typing its name; accepted by the owner.
4. The guard is quiet while refused logins are expected after a load replaced the accounts: **holds**
   (`quarantine.go:278-287`). A SUPER account that came with the new master's accounts can turn the flags off on the
   old master unseen until the operator reconnects MySQL; the settle then reads the flags itself and refuses (T7).
   If nobody reconnects, nothing on the agent reports it; the panel's "MySQL not connected" state is what shows.
5. A SIGKILLed `drop-database` whose DROP still completes without a system log line: **holds** (the line is written
   after the window, `cli_dropdb.go:382-412`).
6. The group id given to the service-account child: **holds, harmless as built** (`svcuid.go:82-94`). On Debian
   `tail` runs as `mysql:adm`; a planted symlink lets it read an adm-readable file, but its output goes only to the
   agent's scanner, which reports a time and nothing else.
7. No `MYSQL_TEST_LOGIN_FILE` on the agent's own client runs: **holds; sound under the shipped unit, AG-9 without
   it.**
8. A hostile new master lying in the database listing: **agreed, accepted.** The new master is the data being copied.
9. MySQL 8.4 behaviour: **not examined** (I started no MySQL).
10. The panel's wording of T7, T12 and the refusals: the panel auditors' part.

### 5. The 5b-2a spec's sections 8 and 11, agent side

Section 8, in its order. "Holds" means the code at this head still behaves as the risk says.

- SEC-204 narrowed: holds (question 3).
- Writes during the load by CONNECTION_ADMIN or SUPER accounts are not detected: holds (`lift.go:296`;
  `quarantine.go:370-385` only puts `read_only` back).
- The union of the source's two listings, as complete as the dump account's view: holds (`dump.go:282-313`).
- CLONE replaces everything; the unmeasured cases: holds. When the copy replaced the accounts the agent cannot
  persist read-only itself (`clone.go:160-169`); the start lock and the recipient's own saved values do it.
- MySQL 8.4 not measured: not examined.
- Put-back in the load stream only for a full dump; a named dump relies on a kept connection for up to 8 hours:
  holds (`dump.go:182`, `lift.go:39, 451-468`).
- Another agent job open when a full dump ends gets ERROR 1290: holds (`lift.go:404`).
- The guard's statements wait behind `LOCK TABLES … WRITE`; three rounds then report "cannot check": holds
  (`quarantine.go:312, 438-461`).
- The load record lives in memory: holds (`rebuild_load.go:26-49`, `quarantine.go:625-632`).
- A command killed between opening and locking: holds; AG-10 is the same class.
- Root holding the lift lock file leaves `super_read_only` off without a report: holds (`quarantine.go:370-378`).
- An account that can turn `read_only` off ends every rebuild in T7: holds.
- A start report is delayed while a lift is open: holds.
- After a load replaced the accounts, until "Hubungkan MySQL": holds (question 4, item 4).
- "Locked again" events not yet seen are erased with the start result at settle: holds (`quarantine.go:701`).
- A lost settle answer; the first old master after a second emergency promote; relocked in mid-rebuild before the
  copy: panel side, not judged here. On the agent, the refusal of a quarantine under a master that was never its
  replica holds (`quarantine.go:111-125`).
- SEC-213 and SEC-214: question 3.
- A directory root owns: programs run as root there; one others can write is refused with T19c: holds
  (`svcuid.go:82-118`).
- The service account can make Periksa, Amankan and the error log read fail, never make root run them; at most
  about 7 s of the guard's round: holds (`svcuid.go:43-48, 126-153`, `realhost.go:243`). This is also K9's `tail`
  item.
- The typed MySQL root password goes to whatever listens on the socket: holds (`cli_rootsql.go:236-259`,
  `conn.go:35-58`).
- Root on the server passes everything; releasing the start lock by hand stays a hand procedure: hold.

Section 11: the table's agent rows are in the code (three lock-file states `lift.go:364-387`,
`quarantine.go:349-357`; P1 without a command for odd names `cli_dropdb.go:521-527`; `mysql.leftover` failing when
its result cannot be kept `leftover.go:76-99`; the three commands refusing without `agent.yaml`
`cli_rootsql.go:202-209`, `cli_admin.go:290-297, 477-484`; T19c for a shared root directory `svcuid.go:87-90`). Its
remaining risk (a first old master whose quarantine stays pending after a second promote is not locked) holds on the
agent: nothing there locks a server that is still pinned master.

### 6. The plan's K9 items, agent side

- `cancelAgentCopy` outside `startMu`: panel code, not read by me. On the agent, `job.cancel` is `strictAllowed`,
  acts on one job id and changes no data itself (`strict.go:32`); the worst is a copy that has to be made again.
- Quoted database names in censored lines: the agent's half is AG-8 (names go in raw). The censoring is the web
  auditor's.
- `MYSQL_*` and `LIBMYSQL_*` in the environment of the agent's client programs: holds; AG-9.
- `tail` on a FIFO named `log_error`: holds and is bounded. It runs as the directory's owner for at most 5 s
  (`svcuid.go:133-137`), only on a quarantined server whose MySQL does not answer (`startlock_guard.go:160-162`), and
  only stretches guard rounds from 5 to about 10 s. In a directory root owns the agent reads the file itself and
  refuses anything that is not a regular file (`startlock_guard.go:402-436`). No fix needed.
- `putBack`/`finalCheck` without `defer`: holds; AG-10.
- `grant-admin` does not recompute before it prints "done": holds (`cli_admin.go:374-379`). Every GRANT returned
  success and each user of the account checks again, so a wrong "done" only sends the operator round once more
  (for example a partial revoke that a new GRANT does not undo). Info.

### 7. Ruling R38

(a) **An oversight, carried forward and documented; not a designed barrier.** The Rencana-4 spec gives the unlock's
purpose as "master kembali persis seperti sebelum kunci" (5.1, line 208-212): the statements were written for a lock
that saved only `super_read_only`. Plan 5b-1 saw the effect and wrote "Di luar lingkup tetapi terlihat … Jangan
diubah di rencana ini" (line 2074-2077). The 5b-1 README re-review (D, m-B) noted the same mechanism. README lines
813-817 describe it. No spec, audit, risk row or README text gives a reason for keeping it, and the code does not
rely on it: the panel's own "Buka kunci master" already removes it (next point).

(b) Callers and what refuses them on a quarantined server:

| Path | Who | Runs when | Refused on a quarantined server by |
|---|---|---|---|
| `unlockPromote` → `unlockMaster` (`promote_lock.go:132-158, 436-498`) | panel | role pin `master`; a lock for that candidate with a recorded address; candidate read read-only now; every listed replica read, none writable with this server's transactions | the role table (`pins.go:134`) and `requireMaster` (`promote_lock.go:163-172`) |
| `guardExpired` → `unlockMaster` (`promote_lock.go:544-598, 624-697`) | the agent itself | role pin `master`; lock with a candidate, past its deadline; candidate read read-only twice, 60 s apart, with the account kept in memory; listed replicas as above | `GuardTick` takes the quarantine branch first (`:567-571`) |
| CLI `unlock-master` (`cli.go:798-839`) | root | role pin `master`, or `unassigned` with a `lock-master` lock; no question asked | the role check (`:806-817`) |
| CLI `set-role master` (`cli.go:382-450`) | root | any role; from `quarantined` or unreadable pins it explains and asks `ya` (unless `--ya`), releases the start lock, then writes the pin | only the question; this is root's exit from quarantine by design |

With an unreadable role file all four refuse or do nothing: `Role()` is empty (`pins.go:402-409`), so `requireMaster`
and `Allow` refuse, `GuardTick` neither unlocks nor touches the start lock (`startlock_guard.go:143-146`), the CLI
refuses, and `set-role` treats the server as possibly quarantined.

(c) **No path would open a server that stays read-only today.** `unlockMaster` already sends
`SET GLOBAL read_only = OFF`: wherever it runs, the server is open at once. Changing what stays saved changes only
the state after the next restart of a server the code has just opened.
- A quarantined old master: none of the callers reaches it (table above).
- A server under the promote lock: the unlock is the very act that opens it, under the same checks as today.
- A server whose role file is unreadable: refused everywhere, as today.
- A server pinned master without a lock: the panel can already send `mysql.setReadOnly{on:false}`, which saves both
  flags OFF (`configure.go:96-117, 609-630`). So the saved `read_only` stops nobody today.
- One real hazard, in the other direction: the statement proposed so far, `RESET PERSIST IF EXISTS read_only`, would
  close a once-promoted master at its next restart (AG-4, fact 3).

(d) **Recommendation: change it, in the next plan, with `SET PERSIST … = OFF` for both flags and a restart test;
not in this merge.**
- Worst outcome if kept: a master that was quarantined once (also the live master after a mistaken
  `set-role quarantined`, AG-2) refuses application writes after a later MySQL restart, while the panel may show it
  healthy; the cure is one statement the operator must know. Possibly also AG-5.
- Worst outcome if changed as recommended: an operator who wrongly reopened an old master no longer finds it half
  closed (for accounts without SUPER) after its next restart. That closing was an accident, it never covered SUPER
  accounts, and `set-role master` warns of two masters and asks first.
- Worst outcome if changed with `RESET PERSIST IF EXISTS read_only` instead: every once-promoted master refuses all
  writes after its first restart following a cancelled planned promote. Do not take that form.
- Before the change lands: keep README and guides 5 to 7 as they are (they are correct), and take AG-4's small step
  if the owner wants one now: `unlock-master` says when a saved `read_only` remains.

### 8. Every place the agent turns read-only off or releases the start lock

| # | Place | Statement | Guards |
|---|---|---|---|
| 1 | `mysql.setReadOnly{on:false}` (`configure.go:96-117, 609-630`) | `SET PERSIST` both OFF | role table: refused on replica and quarantined (`pins.go:902-907`); refused on a master under a promote lock (`:802-810`); on an unassigned server refused while replicas read its binary log; refused with unreadable pins |
| 2 | `mysql.promote` (`promote.go:374-377`) | `SET PERSIST` both OFF | role `replica` with a source pin, the old source must be that pin; planned: old master locked for this candidate, checked again just before; emergency: old master must not answer |
| 3 | `unlockPromote` (`promote_lock.go:490`) | `RESET PERSIST super_read_only`; `SET GLOBAL` both OFF | question 7 |
| 4 | guard, lock expired (`promote_lock.go:686`) | same | question 7 |
| 5 | CLI `unlock-master` (`cli.go:828`) | same | root; question 7 |
| 6 | lifts by agent jobs (`lift.go:296`, from `dump.go:161`, `clone.go:92`, `databases.go:108`, `configure.go:451`, `users.go:317`) | `SET GLOBAL super_read_only = OFF` only; `read_only` stays ON | the lift lock; on a quarantined server every command that lifts is `strictProtected` (root's approval) |
| 7 | lifts by operator commands (`cli_rootsql.go:436`) | same | root; role `quarantined`; both flags on; lift lock free; a typed confirmation |
| 8 | start lock released by the guard (`startlock_guard.go:182, 200`) | removes the `init_file` line and file | role `master`: at once; `replica` or `unassigned`: only while MySQL answers and read-only is proved saved; never on `quarantined`; nothing with unreadable pins |
| 9 | start lock released for a promotion (`startlock_guard.go:506-532`) | same, after PERSISTing read-only | inside `mysql.promote` (row 2) |
| 10 | CLI `set-role master` (`cli.go:426-432`) | same | root; question 7 |

Reach on a server that should stay locked:

- A compromised panel: none on a quarantined server. Rows 1 to 4 are refused by role, rows 6 need root's approval,
  and no `strictAllowed` command lifts anything (checked each one in `strict.go:27-98`). After root's approval and a
  finished rebuild the server is a replica, and the panel can promote it in an emergency if its pinned source does
  not answer from there; that is the standing replica rule, not a way around quarantine. After root's
  `set-role unassigned` the panel can open the server with row 1; the CLI says the agent no longer keeps it
  read-only.
- The `mysql` OS user: cannot reach any row (the pin directory, the lock files and the start lock file are root's
  and outside the data directory). It can still act as itself: add an `init_file` after the agent's in its own
  `~/.my.cnf` (reported as "start lock does not hold", `startlock.go:952-988`), or kill mysqld and start one by
  hand without the option files; the guard then locks it again within a round and reports the start as late or
  bypassed. Inherent, known since 5b-1.
- A DNS controller: no row resolves a name except the reads of the listed replicas in rows 3 and 4 (AG-11) and the
  approval's address check (accepted, SEC-174).

### 9. The rebuild approval and the pins

- Obtain: no. Only `dbpanel agent approve` writes the file (`cli.go:553-558`), in root's directory, after showing
  the source and its address and reading `ya`. The panel knows the six-digit code (it is the agent's answer to
  `mysql.rebuildApproval`), which is not a secret: it only ties root's answer to the current request.
- Replay: within its life the approval lets every protected command through, any number of times (AG-12). Each use
  is logged (`strict.go:172-187`).
- Widen: no. It holds only for the role `quarantined`, the pinned source it names, and while every address that
  source's name resolves to is the approved one (`rebuild_approval.go:141-175`); copies and replication are held to
  the pinned source by `Pins.Allow` (`pins.go:811-835`). `set-source` ends it. A DNS answer that changes between
  the check and the connection is the accepted SEC-174.
- Outlive: 24 hours at most; a life over 24 hours or a date more than 5 minutes ahead is refused. It ends earlier on
  a finished rebuild, any `set-role`, a new quarantine, or `approve batal`. It does not end when a rebuild fails or
  is cancelled (AG-12). A server clock set back lengthens it (root's clock).
- What it covers now: `mysql.dropDatabases`, `dumpFrom`, `cloneFrom`, `configureReplication`, `setReplicationFilter`,
  `startReplica`, `startUntil`, `resetReplica`, `configure` (as replica), `restart`, `createReplUser`,
  `createDumpUser`, `dropUser` (dump accounts), `sweepDumpUsers`, `tlsRequest`, `tlsInstall`. It never covers opening
  writes, a promotion, a release of the role or an install. The approve screen says so (`cli.go:526-534`).
- Change a pin: only along the guarded steps. Unassigned to master or replica after a successful `mysql.configure`
  (`pins.go:690-712`); a first source after a successful copy (`:758-779`); a role from the hint on a server with no
  role file ever seen; replica to unassigned (`:612-627`; master and quarantined refused); master to quarantined
  under a verified new master (the known SA-2 residual: a panel holding the replication password can get a healthy
  master quarantined); replica to master by a verified promotion; quarantined to replica by a settled rebuild. A
  quarantined server's source pin is never moved by the panel (`quarantine.go:181-192`).

### 10. Text the agent sends to the panel

- Bounded: peer error text 300 bytes (`peer.go:112-113`); start lock reason 300 bytes, at most 20 other `init_file`
  values (`startlock.go:53-56, 994-1007`); `init_file` in a start result 4096 bytes; program error output the last
  4096 bytes (`realhost.go:237`); T8a and T8b 800 bytes (`rebuild_load.go:241`); a lag sample 200 bytes; backup
  reports are codes and numbers only; pin errors quote at most 512 bytes of a root-owned file.
- Not bounded by the agent: a health report as a whole and the leftover table list (AG-13), and a job log line
  (built from bounded parts).
- Can it carry a command beside a command box: three sources remain. (1) A peer MySQL's error, up to 300 bytes of
  the peer's choosing, inside the old master's refusals ("master baru … tidak bisa dibaca (…)"); bounded, and it is
  the panel's job to label it. (2) Database names in T8a and T8b, in the sentence that tells the operator to run
  `drop-database` (AG-8). (3) The agent's own fixed advice: the approve command (code digits, source from a strict
  pattern), `set-role` in the role refusals (AG-3), `set-source`, `unlock-master`, `forget-replica`. The values in
  (3) cannot carry shell characters (`snapshot.go:26`, `pins.go:393-398`).
- Log lines: parameters are redacted and cut at 1024 bytes (`registry.go:175-189`); the backup folder is logged
  quoted; no entry name of the folder is ever logged or sent.

### 11. The agent CLI for an operator in a hurry

| Command | Asks? | Prints a next step? | Note |
|---|---|---|---|
| `set-role quarantined` | no | no | AG-2 |
| `set-role master` from quarantined or unreadable pins | `ya` | `unlock-master`, with its two conditions | the condition is in the text; the saved `read_only` is not (AG-4) |
| `set-role replica/unassigned` from quarantined | `ya` | from quarantined to unassigned: yes | says what the panel may do afterwards |
| `set-role` from `master`, `replica`, `unassigned` | no | no | AG-3 for leaving `master` |
| `set-source` | no | no | on a quarantined server it ends an approval, which is the safe side |
| `lock-master` | no | `unlock-master` | makes the master read-only at once; safe direction |
| `unlock-master` | no | no | prints the bypass warning (unchecked candidate and replicas) and goes on; a standing residual (SA-11) |
| `forget-replica`, `forget-replica-port` | no | no | print what the guard no longer waits for |
| `approve <kode>` | `ya`, after source and address | "Kembali ke panel …", and how to cancel | sound |
| `approve batal` | no | no | safe direction |
| `drop-database` | the database's name, typed | P1 prints the `--tanpa-cadangan` command | declined item 3; that path asks for the name again |
| `grant-admin`, `reset-admin` | `ya`, after showing the statements | "klik Segarkan" | sound |

- Messages that lead to harm when followed blindly: the role refusal on a master (AG-3); `setRoleNextMaster`
  followed without its conditions (two masters; the text carries the conditions); P1's command (one database, after
  a typed name).
- `reset-admin`: sound. It runs only on a quarantined server with both flags on, shows its statements, asks `ya`,
  changes MySQL first and the stored account last, stores only after a login as `dbpanel_admin@localhost` worked
  (`cli_admin.go:112-137`), never shows the password, and every unclear ending says exactly what to run. The old
  account stays in MySQL, as the plan text says.

### 12. The test suite as root

Sound, with AG-14.

- The agent's CLI tests and the MySQL driver's tests move every default path into a temporary directory in `init`
  (`system_test.go:19-34`, `testpaths_test.go:14-25`) and each has a test that fails if one of them points under
  `/var/lib` or `/etc`. The system log is replaced by a no-op.
- Every test that runs `set-role master` replaces the layout lookup first (`cli_test.go:237, 522, 762-857,
  1374-1787`; `stubLayoutHost`), so no test releases a real start lock or edits a real MySQL config. Tests in other
  packages go through `RunCLIWith` and its seams.
- The backup tests need a directory outside the hidden trees and make their own under `/var/lib` (or `/srv`, `/opt`,
  `/mnt`, `/run`). They never touch `/var/lib/dbpanel`, `/etc/dbpanel` or MySQL; the `/proc/1` and `/etc/passwd`
  cases only open and stat. A killed run leaves the empty directory and the temporary directories under `/tmp`.
- Not judged by me: MySQL instances a killed run of the driver's tests may leave running (the harness was not in my
  part and I ran none).

## Checked and found sound

- The scan's four rules (no link followed, no entry opened, bounded, cannot block on a FIFO), R13 and R14.
- Generations: an old folder's result is never reported for a new folder (`backup.go:384-386, 435`).
- Strict mode's table: every registered command is classified, an unknown one is protected, and with unreadable pins
  only the allowed ones pass the gate and their handlers still refuse through `Pins.Allow`.
- The quarantine guard: reads both flags every round, puts them back under the lift lock, treats an unopenable lock
  file as "no legitimate lift", and its round sends nothing to the panel directly.
- Pins: read with owner and mode checks, written atomically with a directory sync; unreadable pins refuse every
  checked command and change nothing in the guard.
- The start lock: file before line, line before file on release; kept through every rewrite of the managed block;
  judged from both `--print-defaults` runs.
- `mysql.connect` on a quarantined server stores only a local account with every needed privilege.
- Salvage: bounded to the last leftover result of this quarantine and source, names checked, links refused, sizes
  capped, one at a time.
- The approval's checks and removals; `approve` re-reads the request after the question and rolls back on failure.
- The operator commands' window: signals held, the statement killed and waited for before the put-back, a final
  read of both flags, and messages that claim no more than the server shows.
- The unit: `NoNewPrivileges`, `PrivateTmp`, `ProtectHome`; `KillMode=process` keeps a mysqld the agent started.

## Residual risks (inherent or accepted; worst outcome each)

- A compromised panel can read the data of any server it manages (it creates dump accounts and receives the
  replication password). The agent is not a confidentiality boundary. Worst: a data leak without touching the agent's
  locks.
- An admin account whose password the panel knows and that can log in from another host can turn the flags off
  directly. Worst: an old master writable for up to one guard round at a time, with alerts. `reset-admin` (a local
  account with a random password) is the cure.
- The `mysql` OS user can start MySQL outside the start lock. Worst: an old master writable until the guard's next
  round; reported.
- SEC-174: copies and replication resolve the source's name again. Worst: with a compromised panel and DNS, data
  copied from another address.
- SEC-204, SEC-212, SEC-213 as re-judged. Worst: old databases, tables or accounts stay on a rebuilt replica.
- AG-12: an approval lives 24 hours. Worst: a compromised panel drops what the old master held, as root allowed.
- Root on the server passes everything, including by mistake (AG-2, AG-3 reduce the mistakes, they do not remove
  the trust).

## Not done (what I could not examine, and why)

- Nothing that starts MySQL was run (audit rule). Therefore AG-5 is unconfirmed, MySQL 8.4 is not measured, and no
  restart behaviour was observed.
- The panel's side of the agent's texts (labels, censoring, the master status that reads only `super_read_only`),
  `cancelAgentCopy`, and the panel's handling of `truncated` were not read; they belong to the other two auditors.
- `promote.go`, `repoint.go`, `peer.go`, `health.go`, `status.go`, `users.go`, `install.go`, `tls.go`, `binlog.go`
  and `service.go` were read only where a question led there. The emergency-promote checks are described from the
  lines I read, not audited again.
- AG-11 was not checked against the full 5a audit; it may already be an accepted residual there.
- AG-1 and AG-13 were not reproduced with a test.
- No second pass was made over the earlier audits' closed findings.
