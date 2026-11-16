# Design review: Plan 5b-2a, the old master under one rule

- Reviewed: the design `docs/superpowers/specs/2026-11-14-rencana-5b-2a-master-lama-satu-aturan-design.md`
  and the plan `docs/superpowers/plans/2026-11-14-05b2a-master-lama-satu-aturan.md`
  (plan commit `affba55`), against the code at `8ce7fc5`, the fact sheets
  the author worked from, the builder check, and the controller's
  pre-flight table with rulings P1–P4.
- Method: reading only. No test, no `make`, no mysqld was run. Every
  statement about MySQL or the kernel is marked "known" (documented
  behaviour I rely on) or "to measure" in section 2.
- This is Task 1 of the plan. Nothing but this file was written.

## 1. Verdict

30 findings: 1 Critical, 17 Important, 12 Minor. Four simplifications are
proposed in section 6.

The design's direction is sound: one meaning per state, a lock both
processes take, the list of databases read from the new master, commands in
place of copied SQL. What the review found is mostly in the joints:

- the lift lock's life cycle in the one path that never restores (CLONE),
  its file permissions, and which guard paths may write the flags;
- goal 2 holds only while the agent's MySQL account still works;
- the three commands lose two protections the README steps had (the check
  on the new master, the warning about sessions that block `DROP`), and two
  of the new advice texts name a command that cannot do what the text says;
- on the panel, the role is changed in more places than the plan lists, and
  one new callback inverts an existing lock order.

**Task 3 should not be dispatched as written.** R-1, R-2, R-3 and R-21
change the text of Task 3 itself (and R-4 the values Task 3 consumes). All
four are plan edits the controller can make; none needs an owner decision.
The findings that change section 5 or 7 of the design (R-5, R-8, R-10,
R-11, R-12, R-16 and the simplifications F-1, F-3, F-4) go to the owner
before the task they touch.

## 2. Behaviour the design rests on: known, or to measure

| # | Behaviour | Status | Used by |
|---|---|---|---|
| K1 | `read_only=ON` refuses writes from accounts without `CONNECTION_ADMIN` (or `SUPER`); `super_read_only=ON` refuses them for those accounts too; replication applier threads are exempt from both | known | 5.3 |
| K2 | `SET GLOBAL super_read_only=OFF` leaves `read_only` as it is; `SET … super_read_only=ON` turns `read_only` on; `SET … read_only=OFF` turns `super_read_only` off | known (cheap to confirm in M1) | 5.2, 5.3, 5.4 |
| K3 | `SET PERSIST` sets the running value and writes `mysqld-auto.cnf`; needs `SYSTEM_VARIABLES_ADMIN`; allowed under `super_read_only` | known | 5.2 |
| K4 | Turning `read_only` on waits while another session holds `LOCK TABLES … WRITE`, runs a statement or commits; other sessions' lock requests wait behind it | known from the manual; **to measure** (R-4 e): how long the guard's statement waits and what it returns at its 30 s limit | 5.2 "one round" |
| K5 | `flock(2)` is per open file description: two `open()`s of one file conflict even inside one process; the lock goes when the last descriptor of that description closes, also at process death; any open mode is enough to take it; a child keeps the description across `fork` and across `exec` unless `O_CLOEXEC` | known | 5.2, Task 3 |
| K6 | A statement run with `sql_log_bin=0` is not written to the binary log and takes no GTID; `gtid_executed` does not change. The variable is per session, so it holds only on the connection it was set on | known | 5.4, Task 8, 9 |
| K7 | `DROP DATABASE` and `GRANT` are refused under `super_read_only` (1290), for `SUPER` holders too | known (fact F15) | 5.4 |
| K8 | `DROP DATABASE` waits for metadata locks held by other sessions' open transactions; `lock_wait_timeout` defaults to one year | known | R-8 |
| K9 | Global privilege changes do not reach sessions already connected | known | R-21 |
| K10 | `information_schema.schemata` shows an account only the schemas it holds a privilege on, unless it has `SHOW DATABASES` or a global privilege | known; M2 measures it for the dump account | 5.3 |
| K11 | `mysqldump` writes routine, trigger and event bodies as stored, with real newlines, inside `DELIMITER ;;` blocks | known (builder check C1) | R-11 |
| M-a | Load of a full dump, `DROP DATABASE`, `CREATE USER`/`GRANT`, `INSTALL PLUGIN`, `CLONE` under `read_only=ON, super_read_only=OFF` | Task 2 M1 | 5.3 |
| M-b | The dump account sees every schema; shape of `mysql -N -B` output | Task 2 M2 | 5.3 |
| M-c | `mysqlbinlog` as the service uid | Task 2 M3 | 5.5 |
| M-d | `SHOW GRANTS` under `sql_quote_show_create=OFF`, `ANSI_QUOTES` | Task 2 M4 | 5.7 |
| X1–X7 | Seven behaviours decisions hang on that Task 2 does not measure | **missing**, see R-4 | 5.2, 5.3, 5.4 |

## 3. Findings

Each finding: id, severity, where, scenario, proposed change.

### 3.1 Must change before any code (before Task 3 is dispatched)

#### R-1 (Critical) The CLONE path never ends its lift, so the lock and the count stay taken until the agent restarts

Where: design 5.2 ("pembuka terakhir memasangnya lagi"), plan Task 3
steps 3–4; `internal/agent/drivers/mysql/clone.go:87-96`.

The code today:

```go
restore, err := liftReadOnly(ctx, j.Logf, db, mark)
...
dataReplaced := false
defer func() {
	if !dataReplaced {
		restore()
	}
}()
```

After the data phase (`clone.go:141`, `dataReplaced = true`) `restore` is
never called: the restart of MySQL puts the flags back. Task 3 turns
`restore` into the gate's `end` and passes the gate to "the five users";
it says nothing about this path.

Scenario, built as written:

1. Any CLONE that reaches its data phase (a rebuild of an old master, or an
   ordinary Resync of a replica: `liftReadOnly` is shared by all roles)
   calls `begin(both=…)`. Count 1, `flock` held on the gate's descriptor.
2. The job ends. `end` was not called. Count stays 1, the lock stays held.
3. On a quarantined server whose rebuild then fails before the settle
   (certificate step, `AdminReplaced` wait, T7, lost connection):
   `tryGuard` answers "not free, both" on every round, so the guard never
   relocks again; every CLI command answers `ErrLiftBusy` ("sedang dibuka
   pekerjaan lain") for good. Goal 2 is void on that server and nothing
   says so.
4. On any server, any later lift (`dumpFrom`, `DropDatabases`,
   `createAccount`, `installClonePlugin`) is "a later holder": it "only
   raises the count" and does not turn the flags off. The server's flags
   are ON again since the restart, so the load fails with ERROR 1290. For a
   dump this is after `STOP REPLICA` and, in GTID mode ON, after
   `resetBinaryLogs`.
5. It stays so until someone restarts the agent.

None of Task 3's tests runs a CLONE through the gate.

Proposed change to Task 3:

- `begin` returns two functions, or `end` takes a mode:
  `end()` (restore what was on, then release) and `abandon()` (release the
  holder without touching the flags: MySQL restarted, its values are the
  persisted ones). `cloneFrom` calls `abandon()` after `restartAfterClone`
  returned and, for a rebuild, after its `persistReadOnly(vctx, after)`;
  on every error return after `dataReplaced` too (one `defer`).
- When the last holder leaves through `abandon`, the gate forgets the
  recorded original values.
- Tests: `TestCloneReleasesLift` (real mysqld where the harness can
  CLONE, else a fake host that fails after the data phase): after the job,
  `tryGuard` is free and a following `begin` turns `super_read_only` off;
  `TestAbandonAfterRestartKeepsFlags`.

#### R-2 (Important, security) The lock file has no stated mode or owner check; with the directory's usual modes any local user can blind the guard and block every rebuild

Where: design 5.2 ("berkas kunci milik root di direktori data agent"),
plan Global Constraints (`/var/lib/dbpanel/lift.lock`, no mode), Task 3.
OWASP A01 Broken Access Control / A05 Security Misconfiguration
(CWE-732, CWE-412).

Facts: `/var/lib/dbpanel` is root's with mode 0711 on a panel host
(`scripts/install.sh:35`) and is created 0755 by the agent itself
(`start_result.go:193`, `seenDirMode = 0o755`). Files the agent keeps
there are 0644 (`pinFileMode = 0o644`, `pins.go:68`). `flock` needs only
an open descriptor, read-only is enough (K5).

Scenario, if the lock file follows the directory's convention:

1. Any local account (the `mysql` service account, the unprivileged
   `dbpanel` user of a panel on the same host, a web user) runs
   `flock /var/lib/dbpanel/lift.lock sleep infinity`.
2. The guard's `tryGuard` is never free. By 5.2 it then only puts
   `read_only` back; `super_read_only` off is left alone and not reported.
3. Every agent lift fails after 10 s with `ErrLiftBusy`; every CLI command
   refuses. No rebuild of that server can run, and the text blames "another
   job".

The design's own threat model includes the service account (5.5).

Proposed change (Global Constraints and Task 3):

- The lock lives in a directory only root can enter:
  `/var/lib/dbpanel/agent/` mode 0700, owner root; file `lift.lock`
  mode 0600. (Name and place are the implementer's by design section 6;
  the modes are not.)
- Every opener (gate, guard, CLI) opens with `O_NOFOLLOW|O_CLOEXEC`,
  creates with `O_CREAT|O_EXCL` when missing, and then checks on the
  descriptor: regular file, owner uid 0, no group or other bits, link
  count 1. A file that fails the check is replaced (rename of a fresh
  file) by the agent and refused by the CLI with a text that says why.
- Test `TestLiftLockRefusesForeignFile` (wrong mode, symlink, hard link).
- `not-copied`, if it stays (F-3), goes to the same directory, 0600.

#### R-3 (Important) Which guard paths may write the flags is described three different ways; two of them lock the server in the middle of a legitimate lift

Where: design 5.2; plan Task 4 step 3; the controller's ruling P1;
`quarantine.go:267-273` (deferral), `:302-304` (early return), `:311`
(`persistReadOnly` "unconditionally").

The three descriptions:

- Design 5.2: every round the guard tries the lock; free → put both back;
  not free → look at `read_only` only.
- Task 4 step 3: the *steady* branch does that; "the existing transition
  path is unchanged"; and "while the load mark is active the guard still
  runs the not-free branch".
- Ruling P1: the gate, not the mark, is the truth; `deferringToRebuild`
  "keeps only its logging and the re-check after the load".

The transition path today reads the flags and then runs
`persistReadOnly` whatever it read, and reports a start as `late` when a
flag was off. It runs on: the first round after agent start, MySQL back
after it did not answer (a refused login counts, `quarantine.go:253-257`),
`Uptime` falling short (which, if MySQL counts `Uptime` from the wall
clock, also happens when that clock steps back by more than 2 s: to
confirm), `quarantinePending`, and the round after any load mark cleared. Today the deferral returns before all
of it while a rebuild load runs. Nothing protects the lifts that carry no
mark: `DropDatabases`, `createAccount`, `installClonePlugin`, and the new
CLI.

Scenarios:

- (a) `reset-admin` on a slow server. Guard round 1 falls between its
  `ALTER USER` and the saved credential: login refused, `qUp = false`.
  Round 2, five seconds later, logs in with the new credential while the
  command still holds `super_read_only` off: transition → `SET PERSIST
  super_read_only = ON` in the middle of the command, its next statement
  fails 1290, and a `late` start is recorded → a Critical alert for a
  legitimate command (anti-goal 2) and, after a load, T7.
- (b) The agent is restarted (upgrade) while `drop-database` holds the
  lock: same result.
- (c) Under P1 read literally (no early return for a marked load), a clock
  step or a re-check left by an earlier job during a dump load: `SET
  PERSIST super_read_only = ON` mid-load, the load fails 1290 after
  `resetBinaryLogs` (anti-goal 1).
- (d) CLONE restart: MySQL answers again while the lift (both) is held. If
  the transition path runs, it reports a start that the load made (the
  code's own comment says it is none), and with a start lock that did not
  hold it records "writable since load": every CLONE rebuild of that
  server then ends in T7.
- (e) The unconditional persist races a lift even when both flags read ON:
  guard reads `1 1`, a job's `begin` turns `super_read_only` off, the guard
  then persists it ON.

Proposed change: one invariant in the design (5.2) and one algorithm in
Task 4, replacing step 3.

Invariant: **the guard writes `super_read_only`, reports a start, or
reports a relock only while it holds the lift lock itself and no load mark
is active. `begin` and the CLI need that same lock to lift.**

Task 4 step 3:

1. Open; bookkeeping of `qUp`, `Uptime`, `qStartPending`, `takeRecheck` as
   today. Read `super_read_only`, `read_only`, `init_file` every round.
2. `needWrite` = a flag is off, or this round is a transition (as today).
   When `needWrite` is false, return: the lock file is not touched in the
   normal round.
3. `release, free, both := tryGuard()`; `loading := deferringToRebuild()`
   (logging as today).
4. `!free || loading`: a legitimate lift is running, or a load is between
   its mark and its lift. If `!both` and `read_only` is off: `SET GLOBAL
   read_only = ON`, report (both=false), note. Then `guard.up = false` and
   return; `qStartPending` is kept, so the start is judged in the first
   round after the lift ended.
5. Free and not loading: `persistReadOnly` under the lock; then, as today,
   the start report when `qStartPending` (with `put` as read in step 1),
   else the relock report when a flag was off; note when a flag was off.

Consequences to write down: the guard holds the lock only while it writes
(the CLI no longer collides with a guard round; R-22 covers the rest); the
start report of a restart during a lift comes after the lift; ruling P1 is
replaced by the invariant (the mark still matters between `mark.set()` and
`begin`, and for logging).

Tests to add to Task 4: `TestGuardTransitionDuringCLILift` (a foreign
descriptor holds the lock, flags `1 0`, first round after driver start: no
write, no report; after release: one `ok` start, no `late`);
`TestGuardTransitionDuringUnmarkedLift` (`DropDatabases` running);
`TestCloneRestartIsNoStart` with the gate (the existing assertion, kept).

#### R-4 (Important) Task 2 does not measure seven behaviours that decisions hang on

Where: design section 3 (M1–M4), plan Task 2 and its decision rule.

| # | Missing measurement | Decision that hangs on it |
|---|---|---|
| X1 | M1's load must be the agent's own: a dump made with `dumpArgs` (`--set-gtid-purged=ON`, `--all-databases`), loaded after `RESET BINARY LOGS AND GTIDS` / `RESET MASTER`, with the `SET @@GLOBAL.GTID_PURGED` header, the `mysql` schema and the `FLUSH PRIVILEGES` trailer, all under `read_only=ON` | `both=false` for `dumpFrom` (5.3) |
| X2 | The same load by an account with `SUPER` but no `CONNECTION_ADMIN`, and by one with neither: at which statement it fails (before or after the reset) | whether `dumpFrom` needs its own check before the reset (see below) |
| X3 | Which series: 8.0 and 8.4 both. Task 2 names no version | every `both` value |
| X4 | K4 as the guard meets it: `SET PERSIST read_only = ON` while another session holds `LOCK TABLES t WRITE`, and while one runs a long `UPDATE` | "not writable for more than one round" (goal 2), R-5 |
| X5 | Shape of `mysqldump --databases <n>` output for the backup check: the last line, the `CREATE DATABASE` line for names with backtick, quote, space, non-ASCII; whether views, generated or partitioned tables add or drop lines starting `CREATE TABLE`; a routine whose body has a line starting `CREATE TABLE` | 5.4 step 3 (R-11) |
| X6 | Whether the restart after CLONE comes up with the persisted read-only values (what CLONE does to `mysqld-auto.cnf`) | R-3 (d), R-1 |
| X7 | After a full dump replaced the accounts (`AdminReplaced`): does the lift's restore still succeed on the pooled connection, and after that connection idled past `wait_timeout` | R-21, R-6 |

Proposed change to Task 2: add X1–X7 as steps; add to the decision rule:
"a caller that could not be measured gets `both = true`" (today the rule
has no answer for an unmeasured CLONE). If X2 shows a failure after the
reset, add to Task 3: `dumpFrom` with `both=false` refuses, before
`STOP REPLICA`, a load account that holds neither `CONNECTION_ADMIN` nor
`SUPER` (the panel's replica preflight already fails such an account,
`preflight.go:276-280`, but the agent must not depend on the panel having
asked).

### 3.2 Must change before the named task

#### R-5 (Important, design 5.2 and goal 2; Task 4, 5, 12) A guard that cannot log in, or whose statement fails or waits, tells nobody: the old master stays writable with no alert

Where: `quarantine.go:253-257`:

```go
db, err := h.Open(ctx)
if err != nil {
	d.guard.up, d.qUp = false, false
	return nil
}
```

and `persistReadOnly`'s error, which the caller only logs, rate-limited
(fact sheets, guard loop). On the panel no status rule applies to a
quarantined server and its status comes from the store alone (fact
sheets, monitor), so `mysql_ok=false` in the reports raises nothing.

Scenario (the actor the audit names for SEC-205: an account with
`SYSTEM_VARIABLES_ADMIN`, here also `CREATE USER`):

1. `SET GLOBAL super_read_only=OFF; ALTER USER 'dbpanel_admin'@'localhost'
   ACCOUNT LOCK;` in under five seconds (or `REVOKE
   SYSTEM_VARIABLES_ADMIN`, or holding `LOCK TABLES … WRITE`, K4).
2. Every guard round: login refused (or the `SET` fails, or waits 30 s).
   No relock, no record, no event.
3. The panel shows "Master lama; dikarantina (hanya-baca)". Two writable
   masters, silently: the state the design set out to end. T1 would even
   be false if sent ("agent sudah menguncinya lagi").

Proposed change (owner: one new text, no new alert kind):

- Agent: when, on a quarantined server, MySQL answers but the guard could
  not read both flags or could not put them back for three rounds in a row
  (a refused login per `LoginRefused`, a failed or timed-out statement),
  and no load mark is active, record and report a `late` result with
  `While = "unverified"`; repeat at most once per 10 minutes while it
  lasts; note "writable since load".
- Panel text T1b for that value:
  `Agent tidak bisa memeriksa atau memasang lagi kunci baca-saja master lama ini (login atau hak akun MySQL agent ditolak, atau MySQL tidak menjawab perintahnya); master lama mungkin bisa ditulis. Jalankan di server itu: sudo dbpanel agent reset-admin. Sesudah itu jalankan Periksa data tertinggal`
- Goal 2 reworded: "… cannot stay writable for more than one guard round
  without being locked again **or reported as not lockable**".
- Tests: `TestGuardReportsWhenItCannotLogIn`,
  `TestGuardReportsWhenRelockFails`, panel `TestUnverifiedOpensLateAlert`.

#### R-6 (Important, security; Task 4 step 4, design 5.2 "Settle") A load that fails clears "writable since load" while the earlier load's record still lets the settle pass

Where: `rebuild_load.go:113-124` (`done` is the only writer of
`loaded`/`copied`, and only on success), plan Task 4 step 4
("`mark.set()` menghapus `writableSince`"). OWASP A04 Insecure Design.

Scenario (the SEC-204 actor set: a `SUPER` session on the old master and a
compromised panel):

1. A full dump succeeds: load record L1.
2. The `SUPER` session opens `super_read_only`, changes what it wants,
   the guard sees it: relocked, alert, `writableSince = true`.
3. The panel sends a second `mysql.dumpFrom` that fails at once (wrong
   password for the dump account). `mark.set()` ran first and cleared
   `writableSince`; nothing replaced L1.
4. `mysql.configureReplication`: `loadedFrom` finds L1,
   `writableAfterLoad()` is false, the quarantine ends.

The same failed second load also leaves L1 standing over data it only
partly replaced (true today as well).

Proposed change to Task 4:

- `mark.set()` drops the load record together with the flag
  (`loaded = ""`, `copied = loadCopy{}`): once a new load begins, the old
  record no longer describes the server.
- `settleRebuilt` also reads the flags itself, under `promoteMu`, right
  before it settles; anything but `1 1` is T7. This closes the window in
  which the guard has not run yet and the case where it cannot log in
  (R-5).
- "A lock attempt that failed" counts only when a flag was read off or
  could not be read; a failed `SET PERSIST` after reading `1 1` is retried
  next round and does not cost the operator a second copy.
- Tests: `TestFailedLoadDropsEarlierLoadRecord`,
  `TestSettleRefusedWhenFlagsOffNow`.

#### R-7 (Important; Task 5) `recordStart` treats a relock as a start: it ends the bypass alert, and the id the plan asks for cannot exist

Where: plan Task 5 step 3 ("`reportRelocked` memakai `recordStart` …
`StartID` baru (awalan `relock-` + waktu)"); `start_result.go:79-103`,
`:108`, `:261-270`.

```go
over(proto.QuarantineStartFailed)
if bypassed || result == proto.QuarantineStartBypassed {
	kinds[proto.QuarantineStartBypassed] = startKindRecord{Decided: t}
} else {
	over(proto.QuarantineStartBypassed)
}
```

1. `judgeKinds` with result `late` and `bypassed=false` marks the stored
   `bypassed` entry Over. The panel then closes `master_start_bypassed`
   ("kunci start tidak dipakai MySQL") although MySQL did not start again
   and the bypass stands. The next restart comes up writable with the
   warning gone.
2. A record has no id field: the snapshot derives each id from `Decided`
   (`startID(rec.Decided)`). A `relock-…` id can be put in the event only,
   and the panel ignores events that carry an id in favour of the snapshot.
3. The 60-second limit drops incidents, not only messages: relock at t0
   (recorded), Periksa data tertinggal at t0+20 s closes the alert, relock
   at t0+40 s is "within the minute" and is not recorded. The writes of the
   second incident are never announced (goal 2: "setiap kejadian").

Proposed change to Task 5:

- `startKindRecord` gets `While string \`json:"while,omitempty"\``;
  `StartResult()` copies it to `StartKindState.While`.
- New `func (d *Driver) recordRelock(t time.Time, while string) error`:
  sets only `Kinds[late] = {Decided: t, While: while}` and the worst
  result; it never calls `judgeKinds`. The id is `startID(t)`, in the
  event too. No prefix.
- Every incident is recorded (a new `Decided`); only the queued event is
  limited to one per 60 s. The panel's own limit (`allowStartOpen`) bounds
  the alerts.
- Tests: replace `TestRelockRepeatedReportsOncePerIncident` by
  `TestRelockRecordsEveryIncidentQueuesOncePerMinute`; add
  `TestRelockLeavesBypassedAndFailedEntriesAlone`; panel
  `TestRelockAfterLeftoverCheckOpensAgain`.

#### R-8 (Important; Task 8, 9; design 5.4) A command that waits or is interrupted with the lock open ends in a Critical alert and a forced second copy

Where: plan Task 8 step 8 ("selalu … dengan konteks yang tidak ikut
batal"), `TestDropDatabaseKilledMidway` (expects the guard to "relock and
report"). The README step this replaces warned about it: "Hentikan dulu
aplikasi yang masih memakai database itu: `DROP DATABASE` menunggu sampai
sambungan yang memakainya selesai."

Scenario (the flow T8 itself prescribes):

1. The settle refused: "database tidak disalin". The operator runs
   `drop-database arsip`.
2. A reporting tool still holds an open transaction that read a table of
   `arsip` (reads are allowed on a quarantined server). `DROP DATABASE`
   waits for the metadata lock (K8), with `super_read_only` off and the
   lock held. The guard keeps its hands off; nothing is reported; any
   panel job that needs a lift fails after 10 s.
3. The operator sees a terminal that does nothing and presses Ctrl-C, or
   the SSH session drops (SIGHUP). The Go process dies without running its
   deferred statements.
4. The guard finds the lock free and the flag off: relock, Critical alert
   T1 "jalankan Periksa data tertinggal", and "writable since load".
5. "Ulangi dari tahap ini" now answers T7; the copy of several hours has
   to be repeated. Anti-goals 2 and 4 for a command the panel told the
   operator to run.

Proposed change to Task 8 (and by reference Task 9):

- Before the statement: `SET SESSION lock_wait_timeout = 60` on that
  connection. On timeout the command puts the flag back and prints:
  `Database <nama> sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi. Cadangan tadi tetap ada di <berkas>.`
- From before `super_read_only=OFF` until after the final check, SIGINT,
  SIGTERM and SIGHUP are caught: the command puts the flag back (own
  connection, own timeout), prints one line, then exits non-zero.
- An upper bound for the whole lifted window (10 minutes) after which the
  command gives up the same way.
- `TestDropDatabaseKilledMidway` stays (SIGKILL cannot be caught) and its
  cost is recorded as residual risk; add
  `TestDropDatabaseInterruptedRelocksItself` (SIGINT: `1 1`, no guard
  report) and `TestDropDatabaseBlockedByOpenTransaction`.

#### R-9 (Important; Task 8, 9) Nothing is checked again after the operator answers; the command then acts on what it read minutes earlier and always sets the flag ON

Where: plan Task 8 steps 1–3 (checks) and 7–9 (confirmation, then
`AcquireLiftLock`, `OFF`, `DROP`, "selalu … `ON`"). `approve` re-validates
after `ya` (`cli.go`: "The request may have changed while root read the
screen"); the new commands do not.

Scenario, two administrators on one server:

1. A runs `drop-database app`, the backup is written, the prompt waits.
2. B gives up on the rebuild and runs `set-role master` on the same
   server; applications write to `app` again.
3. A comes back and types the name. The command takes the lock, runs
   `SET GLOBAL super_read_only=OFF` (already off), `DROP DATABASE app`
   with `sql_log_bin=0`, then `SET GLOBAL super_read_only=ON`.
4. A live master lost a database to a backup that is minutes stale, the
   drop is in no binary log, and the master is now read-only.

A smaller variant: a rebuild's load starts during the prompt; the command
then only answers `ErrLiftBusy`, after the backup was already made.

Proposed change to Task 8 and 9: after the confirmation and after the lock
is held, on the session's connection: the role pin is read again and must
be `quarantined`; the flags must be `1 1`; the database must exist with
the same number of base tables. Otherwise:
`Keadaan server ini berubah selama Anda membaca; tidak ada yang diubah. Jalankan perintah ini lagi.`
The final statement puts back what was read, not a constant. Test
`TestDropDatabaseRecheckAfterConfirmation` (role changed; flags changed;
table added).

#### R-10 (Important; design 5.4 and T8; Task 8, 9) The "not copied" list says more than the agent knows, the advice to drop is unconditional, and the commonest way into this situation gets no pointer at all

Where: design 5.4 step 4 and T8; plan Task 8 (sentence A, `not-copied`),
Task 9 (`errNotReplaced`); `rebuild_load.go:172-187`;
`preflight.go:542-544`; `pins.go:454-479`.

Four things:

1. **What the list holds.** `notCopied` names a database that was there
   before the load, *or*, for a dump of named databases, one that was not
   among them:

   ```go
   if has(c.before, name) || (len(c.named) > 0 && !has(c.named, name)) {
   ```

   For a named dump that is every other database on the server, whether or
   not the new master has it. Sentence A then tells root "cadangan di atas
   satu-satunya salinannya" with `ya` as the only barrier. A compromised
   panel chooses the names of a dump, so it can put any database on the
   list.
2. **T8 drops the fork the old text had.** Today the refusal says: start
   the rebuild again; only if the panel then refuses because the database
   is not on the new master, go to the README. The README step 2 has the
   operator check on the new master and says "Bila tampil nama yang persis
   sama, berhenti: database yang ada di master baru dihapus oleh bangun
   ulang baru, bukan dengan cara ini." T8 says, for every name: run
   `drop-database`, then "Ulangi dari tahap ini". For a name that is on the
   new master (a stale local copy the dump loaded into), following T8
   drops the freshly loaded tables too, the settle then passes, and the
   replica lacks a database its master has: replication stops at the first
   event for it, or nobody notices until a promote.
3. **The file outlives the quarantine.** It is removed at a successful
   settle and at the next load. `Pins.SetRole` clears the approval files
   only. After `set-role master` and a later promote, the next quarantine
   of this server reads a list from the previous one.
4. **The main path has no pointer.** A rebuild of an old master that holds
   a database the new master lacks fails first at "Periksa server tujuan":

   ```go
   "server ini masih berisi database lain (%s); versi ini hanya mendukung server replika khusus"
   ```

   That text is not among the four the design rewrites, it names neither
   the README nor a command, and in that path no `not-copied` file exists,
   so the command asks for the typed name with sentence B. With the README
   blocks gone the operator sees a refusal and no next step on the page
   (anti-goal 4).

Proposed change (owner; see F-3 for the smaller option):

- Preferred: no `not-copied` file. The command always prints sentence B
  and always asks for the typed name; before the prompt it prints how to
  look on the new master:
  `Periksa dulu di master baru (hanya membaca): mysql -e 'SHOW DATABASES LIKE "<nama>"'. Jawabannya harus kosong.`
- If the file stays: one line per name with a class, `absent` (full dump
  and the name is on neither list read from the new master) or `unknown`
  (everything else); the source and the time on the first line; sentence A
  only for `absent` under the current source pin; removed by `SetRole` and
  when the agent starts on a server that is not quarantined.
- `errNotReplaced` (replaces T8), with the advice before the list so that
  the 1024-byte cap never cuts it. For a full dump the agent knows both
  groups from the source list:
  `server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Yang tidak ada di master baru (%s): jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel. Yang ada di master baru (%s): mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang.`
  For a named dump: one list, with "bila database itu tidak ada di master
  baru: … drop-database …; bila ada: mulai lagi Bangun ulang".
- The "dedicated" preflight check, when the role pin is quarantined,
  ends with:
  `Untuk tiap database itu jalankan di server ini: sudo dbpanel agent drop-database <nama> (perintah itu membuat cadangan dulu), lalu tekan "Ulangi dari tahap ini".`
- Tests: `TestNotReplacedTextSplitsByNewMaster`,
  `TestDedicatedCheckPointsToCommandOnQuarantined`, panel
  `TestQuotesKeepsNewAdvice` extended to both.

#### R-11 (Important; design 5.4 step 3; Task 8 step 6) The backup check refuses healthy databases, a database that cannot be dumped has no way out, and the "only copy" is not on disk when the drop runs

1. **False refusal.** "Jumlah baris berawalan `CREATE TABLE` sama dengan
   jumlah tabel dasar". Routine, trigger and event bodies are dumped as
   stored (K11). A procedure that creates a work table, written with
   `CREATE TABLE` at the start of a line, makes the count one too high.
   The command then refuses that database on every run.
2. **No way out.** A table that `mysqldump` cannot read (an old master
   that went down hard is where this happens), or case 1: "Gagal satu
   saja: berhenti". The rebuild cannot pass "Periksa server tujuan" while
   the database exists (R-10 point 4), for any copy method. The README's
   SQL is gone. What remains is Hapus server.
3. **Durability.** The file is written and closed; `DROP DATABASE` is
   durable at once. A power cut between the two can lose the backup the
   command called the only copy. Nothing in Task 8 syncs the file or its
   directory.
4. **Path re-use.** If the check and the SHA-256 open the file again by
   path and `--ke` names a folder another account can write, that account
   can swap the file in between.

Proposed change to Task 8:

- Count `CREATE TABLE` lines only outside `DELIMITER ;;` … `DELIMITER ;`
  blocks, and require "at least" the number of base tables; the trailer
  line and exit status 0 stay the proof of completeness. Measure in Task 2
  (X5).
- Keep the descriptor from `O_CREAT|O_EXCL` to the end: check and hash
  through it, `fsync` it and its directory before the prompt.
- `--ke`: when the folder is not root's or is writable by group or others,
  print a warning line before the prompt (do not refuse: a mounted disk is
  the reason the option exists).
- Owner decision: `--tanpa-cadangan`, offered only in the text of a failed
  backup, with its own prompt:
  `Cadangan database <nama> tidak bisa dibuat (<sebab>). Dengan --tanpa-cadangan database itu dihapus TANPA cadangan dan tidak bisa dikembalikan. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: `
  Without it, the failed-backup text must name the remaining way (Hapus
  server, then install again).
- Tests: `TestDropDatabaseRoutineWithCreateTableLine`,
  `TestDropDatabaseSyncsBackupBeforeDrop` (fake file layer).

#### R-12 (Important; design 5.4, Task 9) The new `mysql.connect` refusal sends the operator to a command that works on a different account

Where: plan Task 9, text for `mysql.connect`; `connect.go:148-157`:

```go
if len(res.MissingGrants) > 0 && d.pins != nil {
	if role, err := d.pins.roleErr(); err != nil || role == roleQuarantined {
		return nil, &commands.ParamError{Msg: ...}
	}
}
if err := d.Creds.Save(p); err != nil {
```

On a quarantined server the weak account is refused *before* it is
stored. `grant-admin` works on "akun tersimpan agent". The new text says:
"akun %s belum punya semua hak … Jalankan di server ini: sudo dbpanel
agent grant-admin, lalu hubungkan lagi."

Scenario: the operator connects account `simrs_admin`; refused; runs
`grant-admin`; it inspects the account stored earlier and answers "Akun
dbpanel_admin@localhost sudah punya semua hak yang dibutuhkan."; "hubungkan
lagi" with `simrs_admin` is refused again. A loop, and the README
paragraph that covered it ("Hubungkan MySQL di server yang dikarantina")
is being removed.

Proposed change (Task 9 text; `TestConnectWeakAccountOnQuarantinedServer`):

`server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan, jadi tidak disimpan. Jalankan di server ini: sudo dbpanel agent reset-admin (membuat akun dbpanel_admin dan menyimpannya sebagai akun agent), lalu klik Segarkan di halaman server ini. Hubungkan MySQL tidak perlu diulang.`

Add `TestConnectRefusalAdviceWorks`: refused connect, then the command the
text names, then `mysql.inventory` reports no missing grants.

#### R-13 (Important; Task 9) `grant-admin` computes the master's grant set; the rebuild needs the replica's, and the check that says so still prints `GRANT` statements

Where: plan Task 9 (`MissingAdminGrants`: "`missingGrants` terhadap
`requiredGrants`", i.e. the set of `mysql.connect`); `connect.go:96-105`
(`RequiredReplicaGrants`: "plus the data privileges a dump load needs");
`preflight.go:276-280`:

```go
c := l.add("grants", proto.CheckFail, "Hak akses akun admin", fmt.Sprintf(
	"akun %s belum punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai root lalu periksa ulang", f.Account))
c.FixSQL = f.MissingGrants
```

Scenario: the stored account holds the master set but not the data
privileges. The rebuild fails at "Periksa server tujuan" with the text
above and a list of `GRANT` statements, which the quarantined server
refuses (K7): SEC-229 in a third place the design does not list. The
operator finds `grant-admin`; it answers "sudah punya semua hak". No way
forward but `reset-admin`, which nothing mentions.

Proposed change to Task 9:

- `MissingAdminGrants` uses the replica set (`replicaGrants` for the
  server's version) plus `SHOW DATABASES`: a replica is the only thing the
  panel can make of a quarantined server.
- The preflight "grants" check, when the role pin is quarantined, carries
  no `FixSQL` and reads:
  `akun %s belum punya semua hak yang dibutuhkan. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- `grant-admin` names the account as MySQL knows it: it logs in once with
  the stored account and reads `CURRENT_USER()` (the stored host is the
  host connected to, not the account's host part). When that login fails:
  `akun MySQL yang disimpan agent tidak bisa masuk; jalankan sudo dbpanel agent reset-admin`.
- Tests: `TestGrantAdminCoversReplicaPreflight` (after the command, the
  replica preflight's "grants" check is OK).

#### R-14 (Important, security; Task 7, design 5.5) "Could not be run as that uid" must mean "could not be started"; otherwise the service account chooses when root runs the tool

Where: plan Task 7 (`execAsService`: "bila kredensial tidak ada atau
proses tidak bisa dijalankan sebagai uid itu, jalankan lewat `h.Exec`").
OWASP A04 Insecure Design (CWE-636 not failing securely, CWE-59).

Scenario if a non-zero exit of the tool counts: the service account
replaces a binary log by a symlink to a device or to a file only root
reads. `mysqlbinlog` as the service uid fails ("Permission denied"). The
agent runs it again as root on the same path: the exact case SEC-206 is
meant to close, now reachable at will. For Amankan, a second run after
partial output would also write the output twice.

Proposed change to Task 7: the fallback happens only when no credential
could be determined or the process could not be *started* under it (the
error is not an exit status: fork, `setuid`/`setgroups` or `exec` failed).
A tool that started and failed is a failed Periksa/Amankan with its own
error text; it is never repeated as root. A resolved uid of 0 counts as
"not determined" (same behaviour, but with the warning line). Tests:
`TestServiceToolFailureIsNotRetriedAsRoot`,
`TestServiceCredentialRootCountsAsMissing`.

#### R-15 (Important; Task 11) The role is not changed on every path that fails a rebuild, and where it is changed it is a second write

Where: plan Task 11 step 3 (`fail`, `Resume`), step 4 (`Retry`,
`Approve`); `runner.go:549-575` (`failUnrunnable`), `:1055-1080`
(`Resume` writes the job first, the row afterwards).

After Task 11, `AwaitingRebuild` is "role is quarantined" and nothing
else. Any moment in which a failed rebuild has role `replica` is therefore
a moment in which the old master is an ordinary failed replica: Resync
ulang and Lepas replika are accepted, the box is gone.

1. `failUnrunnable` (a reopened job whose steps or parameters this panel
   cannot rebuild): Retry has already set the role to `replica`; this path
   fails the job without touching row or role. The state lasts until the
   next panel restart.
2. A store error in `endRebuildAttempt` after the row was marked failed:
   the same state; `Resume` does not repair it later because the job is no
   longer running.
3. `Resume` sets the job to failed before the row and the role. A Retry
   that arrives in between reopens the job; `Resume` then marks the row
   failed and the role quarantined under a running job.
4. Lepas replika in that state makes the server `unassigned` in the panel
   while its agent stays quarantined and the topology keeps naming it.

Proposed change to Task 11:

- Two store functions, each one transaction:
  `FailRebuildAttempt(serverID int64, lastErr string) error` (row failed,
  and role quarantined when the row's topology still names the server as
  its old master) and
  `ReopenRebuildAttempt(jobID string, serverID int64, now time.Time) error`
  (job reopened and role replica). `fail`, `Resume`, `failUnrunnable` and
  the re-fail in `Retry` use the first; `Retry` the second. The flag
  `QuarantineEnded` is read from the job by the caller as now.
- `Resume` handles a rebuild job under `startMu`, role and row before the
  job's status.
- A test that fails a rebuild through every path that writes `JobFailed`
  for a `create_replica` job and asserts role, row and marker
  (`TestEveryFailurePathReturnsToQuarantine`: step error, restart, unrunnable
  job, launch refused).
- Drop the Approve half of `TestRetryFromQuarantinedFlipsRole`: a waiting
  job always has a `creating` row (`run` sets it, `runner.go:1534`), so
  "waiting, role quarantined, row failed" cannot be reached.
- F-1 removes this whole class.

#### R-16 (Important; design 7.9, Task 11, 12) "Ulangi dari tahap ini" after Periksa wipes what Periksa just found, without the warnings the rebuild form shows

Where: design 5.1 "Sudah digantikan" and decision 7.9;
`web/handlers_jobs.go:549-562` (`retryView`: wipe groups and connections
only). The rebuild form (`rebuildTarget`) shows the leftover result, the
"belum diperiksa" warning and the start-late warning.

Until now a leftover job superseded the rebuild job, so the only way on
was the form. With 7.9:

1. The rebuild fails before the copy. The alert says "jalankan Periksa
   data tertinggal".
2. Periksa finds 214 transactions that never reached the new master.
3. The job page still offers "Ulangi dari tahap ini". Its dialog lists the
   databases to wipe and the connections. It says nothing about the 214
   transactions or that they are not saved.
4. The copy replaces the data. The transactions are gone.

Proposed change to Task 12: for a pending rebuild, the retry dialog and
the approve dialog show the same warning lines as the form, from the same
function (leftover found and not saved since; not checked; written while
running or at start since the last check). Test
`TestRetryDialogShowsLeftoverWarnings`. The e2e in Task 15 adds: Periksa
finds transactions → the dialog names them.

#### R-17 (Important; Task 12) The new callback from the monitor into the orchestrator inverts an existing lock order

Where: plan Task 12 (`OldMasterReportsReplica` "dipanggil sesudah 3
laporan", `SettleFromAgentPin`); `controls.go:128-143` and `:158-178`
(the orchestrator calls `jobEnded`/`replicaChanged` while it holds
`startMu`); `monitor.go:211-225`, `alerts.go:787`, `:824` (those take the
monitor's `mu`); `monitor.go:481-495` (`report` holds `mu` across
`ingest`).

Existing order: `startMu` → monitor `mu`. If the count is kept in `ingest`
and the callback is called there, and `SettleFromAgentPin` takes `startMu`
(it must, against Retry), the order is `mu` → `startMu`. A Lepas replika
or a job ending at that moment deadlocks the panel: no more reports
judged, no alerts, no job starts, and nothing says so.

Proposed change to Task 12: the monitor returns "tell the orchestrator"
from `report` and calls the callback after `mu` is released, as
`emitAlerts` is; the interface text says so; `SettleFromAgentPin` takes
`startMu` and re-reads everything under it. Also: the monitor's
configuration struct is `Deps` (`monitor.go:30`), not `Options`. Test
`TestPinCallbackRunsWithoutMonitorLock` (the callback calls
`ReplicaChanged`). F-4 removes the callback.

#### R-18 (Important, security; Task 14, design 5.6) `fromAgent` cannot tell a panel-made error from an agent's by its code: the agent chooses the code

Where: plan Task 14 ("salah untuk … `proto.ErrOffline`, batas waktu hub —
hub mengekspor nilai pembanding `hub.ErrTimeout`");
`hub.go:481-488` (`return resp.Error`: the agent's code and message as
sent), `proto/messages.go` (`Is` compares codes only). OWASP A04 Insecure
Design (CWE-451 misrepresentation, CWE-290).

Scenario (the SEC-167 actor, a compromised agent or database server): it
answers a command with `{"code":"timeout","message":"Panel: jalankan di
master baru sudo …"}`. `errors.Is(err, hub.ErrTimeout)` is true, so the
text counts as the panel's and is shown without "Pesan dari agent …
(bukan dari panel)". Goal 5 is bypassed by one field.

Proposed change to Task 14: the hub marks origin by type, not by code.
`readResponse` wraps every failure an agent reported in
`*hub.AgentError{Server int64; Err *proto.Error}`; `timeoutError` and
`offlineError` stay plain. `fromAgent` is `errors.As(err, *hub.AgentError)`.
An agent answer whose code is one the panel reserves (`offline`,
`timeout`) is still wrapped, so it is labelled. Tests:
`TestAgentErrorWithPanelCodeIsLabelled`,
`TestHubTimeoutIsNotLabelled`.

### 3.3 Minor: change before the named task

#### R-19 (Minor; design 5.1 "Panel mengikuti pin agent", Task 12) The heal reads more into the report than it says

- A pin `replica` with the topology's source is also what
  `sudo dbpanel agent set-role replica`, typed by an administrator on a
  quarantined server, produces. The panel would then store
  `QuarantineEnded` ("the agent ended the quarantine after a copy") for a
  job that may have failed before any copy.
- Proposed: heal only when the job failed at or after its connect step
  (`connectIndex`); count live reports only, not a backlog flushed after a
  reconnect (as `unpinnedCount` does). In every other case, with or
  without a row, show one sentence in the box (T3 widened):
  `Agent server ini melaporkan peran %s, bukan karantina. Mulai lagi Bangun ulang sebagai replika, atau hapus server ini.`
- OWASP A08 (state changed on an unverified report). F-4 drops the heal.

#### R-20 (Minor; Task 13, 12) After an emergency second promote, three places still speak as if the first old master could be rebuilt

- Its replica page: `RebuildURL` is "" (no topology names it), so
  `CanResync` and `CanDetach` stay true (`handlers_mysql.go:923-940`);
  the orchestrator refuses both with "pakai Bangun ulang sebagai replika",
  which T6 says is not available. Rule the page by role: a quarantined
  server's replica page offers neither, and links to the server page.
- An open `master_start_late` keeps saying "jalankan Periksa data
  tertinggal", which is refused for that server. Close it, or word it,
  like the `CopyStarted` case: "tidak bisa diperiksa lagi dari panel;
  hapus server ini".
- `TestOldMasterAfterSecondPromote` expects Retry refused with "master
  topologi ini sudah berganti". `supersededBy` tests the role first
  (`runner.go:971-973`, before `:985`), and after the second promote the
  four conditions no longer hold, so the answer is "peran server ini sudah
  berubah". Change the expectation or the order.
- The promote screen lists a quarantined server that has a failed row as
  candidate "Replika gagal" (`promote_view.go:278-279`). Say what it is:
  "Master lama yang dikarantina".

#### R-21 (Minor; Task 3) Gate details

- A later holder re-issues `SET GLOBAL super_read_only = OFF` instead of
  only counting: something else (a `mysql.quarantine` sent again, a
  person) may have turned it on since the first.
- Count the holders that asked for `both` separately; when the last of
  them leaves and others remain, `read_only` goes back on and `tryGuard`
  answers `both=false` again. As written, one short `both` holder leaves
  `read_only` off and unguarded for the rest of a long load.
- The restore runs on a connection the holder took at `begin` and keeps
  (with a large session `wait_timeout`), not on whatever the pool gives
  after hours of idling: after a full dump replaced the accounts a new
  login can be refused (K9, X7), and with T7 a failed restore now costs a
  second copy.
- When the last `end` cannot put the flags back, the lock is released
  anyway and the guard's relock is the backstop; say so, and log it as the
  agent's own failure.
- Open with `O_CLOEXEC` (K5): `mysqldump` and `mysql` run for hours as
  children.

#### R-22 (Minor; Task 8) Order of the checks, and one text for two different causes

- Step 2 checks `1 1` before anything looks at the lock. While a rebuild's
  load runs the flags are `1 0`, so the operator reads "tunggu beberapa
  detik (agent memasangnya lagi) lalu ulangi", which is wrong for hours;
  `TestCLIRefusesWhileLoadHoldsLock` expects `ErrLiftBusy`. Probe the lock
  first (take and release), then check the flags; take it for good at
  step 8.
- Retry the non-blocking attempt for up to 3 s before saying busy.
- `ErrLiftBusy` reaches both sides with the same example. In a panel job
  that lost to a CLI command it says the rebuild is blocked by "bangun
  ulang yang sedang menyalin data". Two texts: for the CLI as written; for
  an agent job:
  `kunci baca-saja server ini sedang dibuka sebuah perintah dbpanel agent yang dijalankan di server itu; tunggu sampai perintah itu selesai, lalu tekan "Ulangi dari tahap ini"`.

#### R-23 (Minor, security; Task 8) The root password on its way to `mysqldump`

- `DefaultsFile` is a file in the temp directory "dihapus `Close`". An
  operator who answers the prompt with Ctrl-C (the natural way to say no)
  leaves the MySQL root password in clear text there. OWASP A02
  (CWE-459, CWE-312). Proposed: write no file when the socket login needs
  no password; otherwise hand `mysqldump` the options through an inherited
  pipe (`--defaults-extra-file=/dev/fd/3`), or at least remove the file in
  the signal handler of R-8.
- One reader for the password line and the confirmation line (a second
  `bufio.Reader` on the same input loses what the first buffered; the
  tests feed both through one stream).
- Residual, to record (OWASP A07, CWE-522): the password is sent in clear
  to whatever listens on the socket path, and the service account controls
  that directory while mysqld is down.

#### R-24 (Minor, security; Task 8, 9) Names from the server go to root's terminal and to syslog

Database and account names are printed in the summary and written to the
system log. A name with control characters or direction marks can redraw
the screen above the prompt or split the log line (OWASP A03 CWE-150,
A09 CWE-117). Proposed: one helper for everything printed that came from
MySQL (the rule `AgentText` applies: control and format characters out),
and `strconv.Quote` in the syslog line, as `approvalSyslog` does for the
user name.

#### R-25 (Minor; Task 9) Interfaces that cannot keep `sql_log_bin=0`

`ResetAdmin(ctx, root *sql.DB, …)` and `MissingAdminGrants(ctx, root
*sql.DB, …)` take a pool. `sql_log_bin` is per session (K6): a statement
that lands on another pooled connection is written to the binary log with
a new GTID on the old master. Take `*sql.Conn` (or a small interface with
`ExecContext`/`QueryContext`) in both, and in `rootSession`. Also decide
here: an option `--pengguna <nama>` for servers whose administrator
account is not `root@localhost` (the prompt then asks for that account's
password).

#### R-26 (Minor; Task 6, design 5.3) A failed second listing costs a whole copy and is explained wrongly

A dump of hours succeeds; the one-second listing after it fails once; the
job still succeeds; the operator learns at "Sambungkan replikasi" that
"agent tidak punya catatan salinan … (misalnya karena agent dimulai
ulang)". Proposed: try the second listing three times; if it still fails,
fail the copy step with
`salinan selesai dimuat, tetapi daftar database master baru tidak bisa dibaca sesudahnya, jadi salinan ini tidak dihitung; tekan "Ulangi dari tahap ini"`.
Residual for design section 8: the union of the two lists also holds
names created on the new master after `mysqldump` listed its databases; a
database of the same name created on the old master during the load then
counts as copied. And the list is as complete as the dump account's view
(M2).

#### R-27 (Minor; Task 4, 9, 12) Texts

- T7 should say, as `errNoLoadRecord` does, that the button is not
  enough: `… karantina tidak diakhiri. Mulai lagi Bangun ulang sebagai replika dari panel; tombol "Ulangi dari tahap ini" tidak cukup, karena salinan data harus diulang.`
  It is also returned when the server was writable *during* the load;
  "sesudah salinan data selesai" → "selama atau sesudah salinan data".
- `startLateCopiedText` ends "Selesaikan Bangun ulang sebagai replika";
  after an incident during or after the copy the agent will refuse to
  finish it (T7). Proposed ending: "… Bila agent lalu menolak mengakhiri
  karantina, mulai lagi Bangun ulang sebagai replika; atau hapus server
  ini".
- An alert opened in state `CopyStarted` is closed by T2 on the next
  monitor round. That is one Critical notice and one closing notice per
  incident; write it down as intended.

#### R-28 (Minor; design 5.9, Task 14, 15) New panel, agent not yet updated

An older agent's refusals quote two README section titles. Task 15 may
rename the sections and Task 14 stops keeping the two titles through the
censor, so the operator reads `lihat README, bagian "[…]"`; and the
rewritten section tells them to run a subcommand the old binary does not
have. Proposed: keep both titles in the README and both phrases in
`panelPhrases` for one release; the rewritten sections start with
"Perintah ini ada sejak versi …; bila agent menjawab perintah tidak
dikenal, perbarui agent dulu (Pasang ulang agent di halaman server)". Add
the case to 5.9.

#### R-29 (Minor; plan bookkeeping)

- Task 12's file list lacks `internal/panel/web/handlers_alerts.go`
  (`alertKindLabels`, line 45) and `internal/panel/notify/message.go`
  (`kindNames`, line 83), where the label it changes lives.
- Task 12 names `monitor.Options`; the type is `monitor.Deps`.
- Task 5's `While` needs a field in the stored record (R-7); Task 5's
  file list is right, its interface text is not.
- The implementer note "Task 3–10 hanya agent dan proto" does not hold for
  Task 9 under ruling P3 (panel tests may change there). Harmless; say it.

#### R-30 (Minor; Task 11) After a panel restart the agent may still be copying while the panel offers Hapus and a new rebuild

`Resume` fails the panel's job; the agent's snapshot job runs on. Before,
the failed row blocked the delete and Lepas was refused. Now Hapus is
offered at once. Proposed: before the delete of a quarantined server with
a row, and before a new rebuild, send `job.cancel` for the stored agent
job (best effort, as `cancelJob` does), and add the case to
`TestDeleteQuarantinedWithFailedRow`.

### 3.4 Record as residual risk (design section 8)

- A command killed with SIGKILL, or a server that loses power, between
  `OFF` and `ON`: the guard relocks, a Critical alert opens, and after a
  load the copy must be repeated (R-8).
- Root holding the lift lock for a long time (a stuck command) keeps
  `super_read_only` off and unreported; `read_only` stays guarded.
- An account that can switch `read_only` off can make every rebuild end in
  T7. It could already write to the server.
- The union of the two source lists, and the dump account's view (R-26).
- The root password prompt and the socket path (R-23).
- The start report of a restart that happened during a lift is delayed
  until the lift ends (R-3).
- A relock recorded during a rebuild and not yet seen by the panel is
  removed with the start-result file at the settle (builder check Q2;
  still true).

## 4. The state table, walked

Columns: panel role / replica row / old-master marker / agent pin; what
the pages offer; what the orchestrator accepts; how the monitor judges;
ways out. "→ R-n" marks a finding.

| State | Role / row / marker / pin | Offered | Accepted | Monitor | Ways out | Problems |
|---|---|---|---|---|---|---|
| S1 Quarantined | Q / – / yes / Q | Hapus, Periksa, Amankan, Bangun ulang | same | old master | rebuild, delete | none found |
| S1' Quarantined, agent says replica | Q / – / yes / replica | T3 | new rebuild runs (agent needs no approval); Periksa refused by the agent | old master | rebuild, delete | only pin `replica` is worded → R-19 |
| S2 Rebuild running or waiting | replica / creating / yes / Q | job page | Cancel → S1; Approve; nothing else (busy) | replica "not judged"; start results judged (new) | finish, fail → S3, cancel → S1 | role differs from pin only here → F-1 |
| S2 → fail before copy | → S3, copy not started | | | | | via `failUnrunnable` or a store error the role stays replica → R-15 |
| S2 → fail at copy | → S3, uncertain or started | | | | | a lock lost to a CLI command leaves "uncertain" though nothing changed (acceptable) |
| S2 → settle refused by agent | → S3, copy started | Retry, Bangun ulang, Hapus | same | old master | CLI command then Retry; new rebuild | advice texts → R-10, R-12, R-13; T7 → R-27 |
| S2 → settle answer lost | panel S3, pin replica | Periksa (agent refuses), Retry, Bangun ulang, Hapus | Retry heals (existing test) | old master until healed | Retry; heal after 3 reports | R-17, R-19 |
| S2 → panel restart | → S3 by `Resume`; waiting stays S2 | | | | | order in `Resume` → R-15; agent job still running → R-30 |
| S3 Failed before settle | Q / failed / yes / Q | Hapus; Periksa and Amankan unless copy started; Bangun ulang; Retry on the job | same; Resync, Lepas, Buat replika refused | old master | Retry → S2; new rebuild → S2; delete; Periksa then Retry | Retry dialog silent on leftover → R-16; planned promote refused (T4) with a way out (delete; for a dead server "Hapus tanpa karantina") |
| S3 + Retry, launch refused | back to S3 | | | | | two writes → R-15 |
| S4 Failed after settle | replica / failed / – / replica | Resync, Lepas, Retry | same | failed replica | ordinary | none found |
| S6 After emergency second promote | Q / – or failed / **gone** / Q | T6, Hapus | delete; everything else refused | old master without topology | delete, then `set-role` on the server (the delete dialog says so) | replica page, alert text, test expectation → R-20 |
| S7 Legacy row | replica / failed / yes / Q | – | normalised at start | – | → S3 or S4 | none found; idempotent as specified |

Races looked at: Retry against a new rebuild, a delete, a leftover job
(all under `startMu`, busy checks hold); Retry against the heal (safe in
either order once the heal takes `startMu`, R-17); `fail` against a Retry
(the role must change before the job reads as failed: R-15); `Resume`
against a Retry (R-15); two CLI commands (the lock; the later one
refuses); a CLI command against a role change (R-9); guard against CLI and
against the agent's own lifts (R-1, R-3).

## 5. Areas checked with nothing to report

- `flock` as the shared lock between gate, guard and CLI: correct for
  separate descriptors in one process and across processes, released at
  process death (K5), given R-1, R-2, R-21.
- Design 5.3's rule for a full dump ("present here, on neither list from
  the new master → not copied"), names compared without case, system
  schemas left out on both sides: correct. The Task 6 test
  `TestRebuildFullDumpRefusesDatabaseCreatedDuringLoad` fails without the
  change and passes with it.
- 5.9, new agent with old panel: an old panel reads the `late` entry and
  ignores the extra field. True once R-7 keeps the relock from touching
  the other entries.
- The four conditions for "not superseded" and leaving leftover and
  salvage jobs out of "newer job": no way found to run old parameters over
  a changed state (but see R-16 for what the operator is shown).
- Delete of a quarantined server with a failed row; the new master's
  delete afterwards; TLS jobs leaving a quarantined server out
  (`quarantinedForTLS` already goes by role).
- T4 and T5: no state found in which a planned promote stays refused with
  no way to clear it.
- Legacy normalisation: safe to repeat; a missing or unreadable job falls
  to "quarantined", the safe side.
- Task 10 (source stamp on `leftover-gtid`, session settings for the
  listing proof, channel source against the pin): consistent with the
  code; an unstamped file only costs one more Periksa.
- `sql_log_bin=0` in the three commands: no GTID is taken (K6), given
  R-25.
- Rulings P2 and P4: fine. P3: the new texts pass today's anchors (the
  first account anchor still matches). P1: replaced by R-3.
- The kept phrases after Task 14: none lets agent text pass as the
  panel's once the label is always applied (given R-18).

## 6. Simpler ways

#### F-1 Keep the role `quarantined` until the settle (owner; before Task 11)

Today's table has one row where the panel's role and the agent's pin
differ: "Bangun ulang berjalan" (`replica` against `quarantined`). If the
role stayed `quarantined` from the promote to the settle, the rule would
be: role quarantined ⇔ the quarantine has not ended.

- Dropped: the role change in `fail`, `Resume`, `Retry`, `Approve` and
  their undo; the relaxed role test in `supersededBy` (it becomes "role
  must be quarantined while the flag is unset"); the monitor's special arm
  for "role replica, marker still there"; R-15 entirely.
- Added: `AddReplica` keeps a quarantined role; the settle sets the role
  to `replica` in the same transaction as the flag and the marker; the
  box shows "Bangun ulang sedang berjalan" with a link to the job.
- Lost: a running rebuild no longer shows as a replica card "sedang
  dibuat" in the replica lists; it shows in the old-master box and on its
  job page.
- Legacy normalisation stays.

#### F-2 Run the tools as the owner of the directory they read (before Task 7)

Take the credential from `lstat` of the binary-log directory (and of the
error log's directory) instead of `--print-defaults` → `--user` → user
database. The process then can read nothing its files' owner could not.

- Dropped: `serviceCredential`'s option run and lookup, the "other user"
  case, most ways for "uid cannot be determined".
- Kept: owner uid 0 → as today, as root, with the warning line.
- Lost: supplementary groups of the service account (not needed to read
  files it owns).

#### F-3 No `not-copied` file (owner; before Task 8)

Always sentence B and the typed name; print how to look on the new master
(R-10).

- Dropped: the file, its three functions, writing it at the refusal,
  removing it at the settle and at the next load, sentence A, its
  staleness (R-10 point 3) and the panel's influence on it (point 1).
- Lost: `ya` instead of typing the name in one case (a database created on
  the old master during a full load). The main path (the preflight
  refusal) never had the file.

#### F-4 No heal from reports (owner; before Task 12)

Retry already heals a lost settle answer (the existing test
`TestRetryConnectHealsQuarantineEnded`). Show the sentence of R-19 in the
box for a quarantined server
whose agent reports another role, with or without a row, and add for the
row case: `… atau tekan "Ulangi dari tahap ini" pada pekerjaan bangun ulangnya.`

- Dropped: the report count, the callback, `SettleFromAgentPin`, R-17,
  R-19, and the one place where the panel changes a role because of a
  report.
- Lost: the repair without a click. Until the click the monitor judges the
  server as an old master whose agent no longer reports start results.

## 7. All findings

| Id | Severity | Design section / task | One line | Fix by |
|---|---|---|---|---|
| R-1 | Critical | 5.2 / Task 3 | CLONE never ends its lift: lock and count stay taken, guard blind, later lifts do nothing | before Task 3 |
| R-2 | Important | 5.2 / constants, Task 3 | Lock file without mode or owner check: any local user blinds the guard and blocks rebuilds | before Task 3 |
| R-3 | Important | 5.2 / Task 4, ruling P1 | Guard's transition path writes the flags during legitimate lifts; three descriptions disagree | before Task 3 (text), built in Task 4 |
| R-4 | Important | 3 (M1–M4) / Task 2 | Seven behaviours decisions hang on are not measured; no default for an unmeasured caller | before Task 3 |
| R-5 | Important | 5.2, goal 2 / Task 4, 5, 12 | A guard that cannot log in or cannot set the flags reports nothing | before Task 4 (owner: text T1b) |
| R-6 | Important | 5.2 / Task 4 | A failed load clears "writable since load" while the old load record still settles | before Task 4 |
| R-7 | Important | 5.2 / Task 5 | Relock recorded as a start ends the bypass alert; the id cannot exist; the limit drops incidents | before Task 5 |
| R-8 | Important | 5.4 / Task 8, 9 | Waiting or interrupted command: Critical alert and a forced second copy | before Task 8 |
| R-9 | Important | 5.4 / Task 8, 9 | No re-check after the confirmation; flag set ON whatever it was | before Task 8 |
| R-10 | Important | 5.4, T8 / Task 8, 9 | "Not copied" list and T8 say more than is known; main path has no pointer | before Task 8 (owner) |
| R-11 | Important | 5.4 step 3 / Task 8 | Backup check refuses healthy databases; no way out; backup not synced before the drop | before Task 8 (owner: `--tanpa-cadangan`) |
| R-12 | Important | 5.4 / Task 9 | `mysql.connect` refusal names `grant-admin`, which works on another account | before Task 9 (owner: text) |
| R-13 | Important | 5.4 / Task 9 | `grant-admin` uses the master's grant set; preflight still prints `GRANT` on a quarantined server | before Task 9 |
| R-14 | Important | 5.5 / Task 7 | Fallback to root must be "could not start", never "tool failed" | before Task 7 |
| R-15 | Important | 5.1 / Task 11 | Role not changed on every failing path, and not atomically | before Task 11 |
| R-16 | Important | 7.9 / Task 11, 12 | Retry after Periksa wipes found leftover data without the form's warnings | before Task 12 |
| R-17 | Important | 5.1 / Task 12 | Monitor callback inverts `startMu` → monitor lock order: deadlock | before Task 12 |
| R-18 | Important | 5.6 / Task 14 | `fromAgent` by error code: the agent picks the code and loses the label | before Task 14 |
| R-19 | Minor | 5.1 / Task 12 | Heal also fires for a hand-typed `set-role replica`; backlog reports | before Task 12 |
| R-20 | Minor | 5.1 / Task 13, 12 | After emergency second promote: replica page, alert text, test expectation, candidate label | before Task 13 |
| R-21 | Minor | 5.2 / Task 3 | Gate: re-assert OFF, `both` accounting, own connection for restore, `O_CLOEXEC` | before Task 3 |
| R-22 | Minor | 5.4 / Task 8 | Flag check before lock probe gives the wrong text; one busy text for two causes | before Task 8 |
| R-23 | Minor | 5.4 / Task 8 | Root password left in a temp file on Ctrl-C; one reader for two lines | before Task 8 |
| R-24 | Minor | 5.4 / Task 8, 9 | Names from MySQL printed raw to terminal and syslog | before Task 8 |
| R-25 | Minor | 5.4 / Task 9 | `*sql.DB` cannot keep `sql_log_bin=0`; other administrator account | before Task 9 |
| R-26 | Minor | 5.3 / Task 6 | Failed second listing costs a copy and is explained wrongly; union residual | before Task 6 |
| R-27 | Minor | 5.8 / Task 4, 12 | T7 and the copied-state alert text promise what the agent will refuse | before Task 4, 12 |
| R-28 | Minor | 5.9 / Task 14, 15 | Old agent's texts lose their README titles and their kept phrases | before Task 14 |
| R-29 | Minor | plan | Missing files in Task 12, `Options` → `Deps`, notes | with the plan edit |
| R-30 | Minor | 5.1 / Task 11 | Hapus and new rebuild offered while the agent's copy job still runs | before Task 11 |
