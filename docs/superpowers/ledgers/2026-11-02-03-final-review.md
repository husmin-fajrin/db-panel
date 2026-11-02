# Final whole-branch review: Plan 3 (pemantauan, notifikasi, kontrol)

Range: db3fa80..38e4922 (31 commits, 126 files). Reviewer: Opus 5.5, read-only.

How I reviewed it (in passes):
1. Spec sections 2–5 and 7, the plan's Global Constraints and Review Focus, and the ledger extract.
2. Agent: the monitor loop, health.go, pins (Bootstrap/Allow), the preflight resync changes, releaseRole, CLI, and clone.go.
3. Panel monitor/alerts/notifier: monitor.go, state.go, the alerts.go rules and evaluate, notifier.go, telegram.go, and the panel wiring (panel.go, listener, localagent).
4. Orchestrator: controls.go, resync.go, the create_replica prepare/snapshot/connect/verify path, and the runner cancel/fail/finish paths.
5. Web: router, handlers_controls, handlers_settings, the replica page conditions, the resync modal, _status.html, notices, and README.

Evidence I ran:
- A real-mysqld reproduction of F1 in a temporary worktree, now removed.
- Two scratch unit tests on the panel monitor rig that reproduce F2 and its Start-replika sibling.
- `go test -race -count=1` on internal/panel/monitor, internal/panel/notify, internal/agent/monitor and internal/panel/orchestrator: all green.
- I did not run the E2E.

## Strengths

- **Architecture follows spec 4A cleanly.**
  - The agent checks, sends a report only when something changed, and keeps a ring buffer; the panel derives statuses with pure functions (DeriveReplica/DeriveMaster) and runs the alert rules from one table.
  - Locks are kept well: the Monitor never holds `mu` across a `Hub.Call`. `OnOnline` hands `monitor.configure` to `goBg`, and `Close` cancels it and waits.
  - The Notifier is woken without blocking, and one pass at a time runs under `passMu`.
  - Shutdown order (Orchestrator → Monitor → Store) and goroutine lifetimes (agent `monDone`, local agent detach) are correct.
  - The race detector is clean on all four concurrency-heavy packages.
- **Review Focus items are covered with real tests.**
  - Backlog uses `taken_ago_ms` and the panel clock.
  - A backlog batch is evaluated once, so it sends no Telegram flood.
  - An old agent shows NeedsUpgrade, not a false "Sinkron".
  - A token failure is visible and retried; the token is scrubbed from errors (including from `*url.Error`).
- **Security posture is good.**
  - The role pin table is enforced in the agent, including the startUntil and dropUser refinements and fail-closed on an unexpected params type.
  - Source pins exist.
  - The audit log is append-only by trigger and deliberately has no foreign key.
  - Every new POST route goes through `authedPost` (CSRF). The wipe list is validated against the stored inventory.
  - Raw MySQL errors stay out of Telegram, SSE and the timeline (`eventDetail`).
  - The token is sealed with panel.key and never echoed.
- **Migration 0004 is additive and safe on a Plan 2 database.**
  - New tables, `ADD COLUMN` with defaults, and no CHECK on `replicas.state`, so `resyncing` is accepted.
  - The partial unique index enforces one open alert per (kind, server).
- **Upgrade paths degrade gracefully.**
  - An old panel ignores `monitor.snapshot` events (`HandleEvent` default branch).
  - A new panel marks old agents NeedsUpgrade, and sweep and releaseRole are best effort.
- **Ledger rulings were applied faithfully.** Examples: disk_crit replaces disk_warn, the job_failed close rule, the GTID freshness guard, the startup grace, and the snooze-end resend.

## Issues

Counts below exclude F1 and F2, which were already known. F1 is Critical (a core flow is broken) and F2 is Important; both are confirmed.

### Critical

None new.

### Important

**I1. F2 is wider than recorded: "Start replika" from the panel, and every successful create_replica, raise the same false `replica_stopped` alert and Telegram. A dump resync also raises a false `replica_unlocked`.**

Where:
- `internal/panel/orchestrator/controls.go:30-38` (StartReplica writes `paused=false` right after `mysql.startReplica`).
- `internal/panel/monitor/state.go:183-206` (`resolve` takes the new `repPaused`/`repState` on the next tick) and `state.go:214` (`derive` judges the old snapshot).
- `internal/panel/monitor/alerts.go:373` (`active`) and the `replica_stopped` rule (opens at once, no delay).

Failure scenario: the replica is Dihentikan (snapshot SQL=No IO=No, `paused=1`) and the user clicks Start.
1. The orchestrator clears `paused`.
2. Within 5 s the Monitor tick re-resolves and judges the last snapshot (still stopped) with `paused=0`. That gives "Berhenti (di luar panel)", so `replica_stopped` opens and the Notifier sends "⛔ Replika … berhenti" at once.
3. The agent's next check (up to 15 s later) closes it with "pulih".

With tick=5 s and check=15 s, the tick comes first about 5 times in 6.

I reproduced this on the monitor rig: after `SetReplicaPaused(false)` and one tick, the status is "Berhenti (di luar panel)" and `replica_stopped` is open.

Same mechanism elsewhere:
- **create_replica:** the state goes creating→ready in `replicaVerify` (create_replica.go:726) while the last report was taken before START REPLICA.
- **Dump-path resync:** `liftReadOnly` turns `super_read_only` off mid-job. Reproduced: after ready, both `replica_stopped` and `replica_unlocked` open.
- README ("Stop replika / Start replika … Dihentikan (tanpa peringatan) lalu kembali Sinkron") and the E2E do not cover Stop→Start, which is why this was not seen.

Fix: see the F2 section below. The fix must live in the Monitor and cover every store change that alters how a replica is judged, not only job end.

**I2. The manual "unlock" instructions use `SET GLOBAL`, but dbpanel locked the server with `SET PERSIST`, so the lock comes back on the next MySQL restart.**

Where:
- `web/templates/_status.html:52` ("Yang bisa dilakukan sekarang", the emergency manual promotion).
- `web/templates/replica_detail.html:192` and `internal/panel/web/handlers_mysql.go:92` (Lepas dan hentikan).
- `README.md:494`.

The lock comes from `configure.go:572` (`SET PERSIST read_only/super_read_only = ON`) and the `read_only=ON`/`super_read_only=ON` lines in the dbpanel my.cnf block (`configure.go:287`). Persisted values win at startup.

Failure scenario: the master dies.
1. A hospital IT staffer follows the panel's emergency box and runs `… SET GLOBAL super_read_only=OFF; SET GLOBAL read_only=OFF;` on the best replica.
2. They point the application at it, and it works.
3. On the next reboot or MySQL restart of the new master, it comes back read-only and the application cannot write. That is a second outage, at a time nobody connects it to the promotion.

The detached-replica instruction has the same trap.

Fix: use `SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF;` in all four places, and say in the text that it survives a restart. Add a template test that checks the PERSIST wording.

**I3. Spec 5.5 is missing: when the master's source address is changed on the master page, the panel must show `dbpanel agent set-source <host:port>` for each replica.**

Where: `internal/panel/web/handlers_mysql.go:85` (the notice "sumber" only says the address is saved). There is no `set-source` anywhere in web/ or the orchestrator. The plan's Task 9/12/13 text dropped this spec sentence (this is a plan gap).

Failure scenario:
1. The master's IP changes and the user updates the address on the master page.
2. Existing replicas keep following the old address, and their agents hold a source pin to it.
3. The next Resync ulang, or the next Buat replika onto a server with a stale pin, fails at "Salin data"/"Sambungkan replikasi" with a pin refusal. That opens job_failed and sends a Telegram.
4. Only then does the user learn the CLI command from the agent's refusal text. Note that a resync has already reset replication in "Siapkan replika" before it fails.

Fix:
- After a source-address save, show a notice that lists, per registered replica, `sudo dbpanel agent set-source <new host:port>`. The master page could also show this permanently while any replica exists.
- Optionally, have `replicaCheckTarget` call a cheap agent read of the pinned source (or reuse preflight), so a mismatch fails in "Periksa server tujuan" before anything is reset.

### Minor

**M1. "Pulih" is sent for alerts that did not recover.**

Where: `alerts.go:373` closes every open alert of a replica that becomes Belum siap (resync started) or is no longer a replica (Lepas, keep mode). `notify/message.go:173` then sends "✅ … pulih".

Effect: pressing Resync ulang on a broken replica sends "replika berhenti … pulih" while it is being rebuilt. With F2 unfixed, the full sequence for one resync is "pulih" → "berhenti" → "pulih". Spec-compliant ("status lain") but misleading.

Fix: record a close reason and word the recovery message accordingly ("ditutup: Resync ulang dimulai" / "replika dilepas dari panel"), or skip the recovery message for inactive closes.

**M2. Server-level alerts are suppressed while a replica is Belum siap (creating, resyncing, failed).**

Where: `alerts.go:373` (`!active` closes and never opens disk_warn, disk_crit, connections_high and agent_offline).

Effect: a dump resync that fills the replica's disk raises no disk alert. Any open disk_crit even sends "pulih" when the resync starts.

Fix: apply the disk, connection and agent_offline rules regardless of replica state. Only the replication-status rules need `active`.

**M3. The resync wipe list is the replica's whole inventory.**

Where: `handlers_mysql.go:635-637`.

Effect: it includes databases the replica does not follow (for example a local database that Plan 2's create flow deliberately kept). The dump path drops every listed database (`create_replica.go`, `replicaSnapshot`: "Every confirmed leftover goes"), although a subset dump only needs the followed ones. The modal does list them, so the user is told. Spec 5.4 says "dari inventaris replika", so this is partly a spec question.

Suggestion: list followed or master-named databases as "dibuang" and others as "dibiarkan", unless CLONE is chosen. CLONE replaces everything, so the list must stay complete for CLONE.

**M4. The Notifier keeps sending when Telegram is unreachable.**

Where: `notify/notifier.go:213` (Pass) and `:303` (deliver).

Effect: each pass sends every pending alert one by one with a 10 s timeout. On a panel without internet (spec 9.15) and a firewall that drops packets, a pass takes N×10 s and starts again at once.

Fix: after the first transport-level failure in a pass, mark the remaining alerts failed with the same reason without trying them.

**M5. `make test` does not run `-race`.**

Where: `Makefile:16`. The plan's Global Constraint requires `-race` for internal/panel/monitor, internal/panel/notify and internal/agent/monitor.

Status: I ran it and it is green, but nothing enforces it. Fix: add a `go test -race` line for those packages.

**M6. Resync ulang is not refused for an agent that needs upgrading.**

Where: `resync.go:44`.

Effect: an old agent ignores `Resync/WipeList/ReclaimBytes` and `mysql.sweepDumpUsers`, so the job takes Plan 2 preflight paths.

Fix: refuse in StartResync, or in the web handler using `MonitorView`, with "Mutakhirkan agent di server ini dulu".

**M7. README says an upgraded agent writes both pins from MySQL's state; it writes only the role pin.**

Where: `README.md:302-303`. `Pins.Bootstrap` (`pins.go:360-392`) writes only RoleFile.

Effect: replicas made before Plan 3 have no source pin, so SEC-28 item 3 is inactive for them until they are configured again.

Fix: correct the README. Bootstrapping the source pin from `Source_Host:Port` is possible, but the panel's `topo.SourceHost` spelling may differ, so documenting is the safer choice.

**M8. "Start replika" does not wait 10 s and then show the new status (spec 5.4).**

The handler redirects at once with "Status terbaru tampil setelah laporan berikutnya". This is acceptable with SSE, and the I1/F2 fix (a forced snapshot after the change) delivers the intent.

**M9. A failed CLONE gives no hint about Paksa dump.**

A CLONE failure message ("CLONE gagal: …") does not tell the user that "Paksa dump" is the way around it. Worth a sentence in `cloneFailure` (or in the resync failure step text) after F1 is fixed, for future CLONE failures (network, donor version).

## Root-cause diagnosis and proposed fix for F1 and F2

### F1: Resync via CLONE fails at "Salin data" with 1290 (--super-read-only)

**Root cause (verified against real mysqld 8.0.46).**

The donor had the clone plugin and a BACKUP_ADMIN account. I ran `CLONE INSTANCE` on recipients with `SET PERSIST super_read_only=ON`:

| Recipient | CLONE result |
|---|---|
| bare (no user schemas) | starts fine (3707 restart-without-supervisor, the normal sign that the data phase completed) |
| gtid-only (created and dropped a schema) | starts fine (3707) |
| has a user schema | **1290 --super-read-only** |
| has a user schema, `super_read_only=OFF` (read_only still ON) | starts fine (3707) |

Explanation: a remote CLONE first drops the recipient's user-created schemas and tablespaces. That drop is refused while `super_read_only=ON`.

- **create_replica** works only because preflight chooses CLONE solely for a target with no user databases (`preflight.go`: `targetClear` with `Resync=false`).
- **resync** chooses CLONE for a replica that holds the followed databases (`targetClear` via WipeList). By then `replicaPrepare` has applied `mysql.setReadOnly{on:true}` (`create_replica.go:514`, SET PERSIST), and the replica was already locked anyway. So `CloneFrom` (`clone.go:53-102`) runs `CLONE INSTANCE` on a locked, non-empty recipient.

The implementer's guess (gtid_executed or persisted state) is ruled out by the gtid-only case.

Side effect today:
- Prepare already ran `mysql.resetReplica`, so a failed CLONE resync leaves the replica with no replication at all (state failed).
- Every retry fails the same way until the user ticks "Paksa dump".
- Every all-databases replica is affected, and that includes every replica created with CLONE.

**Proposed fix (agent side; the panel cannot do it because the lock is also in my.cnf):**

1. In `CloneFrom`, after `installClonePlugin` and before `CLONE INSTANCE`, call `restore, err := liftReadOnly(ctx, j.Logf, db)`, as `dump.go:121` and `databases.go:101` do.
   - It uses `SET GLOBAL`, so the persisted ON is untouched.
   - Lifting only `super_read_only` also works for SUPER/CONNECTION_ADMIN accounts (row 4 above). Lifting both matches the dump path and is safe for non-SUPER admin accounts.
2. Run `restore()` only when CLONE fails before its data phase (`cloneErr != nil && !isCloneRestart(cloneErr)`, and the cancel/`waitClone` error paths). After a completed data phase the old server is gone, and the restart re-applies `super_read_only=ON` from mysqld-auto.cnf and the dbpanel my.cnf block.
3. Defence in depth, panel side: in `replicaSnapshot`, after a successful CLONE with `AdminReplaced=false`, call `mysql.setReadOnly{on:true}` (idempotent) before "Sambungkan replikasi". `verifyCopyAfterReconnect` already does this for the AdminReplaced path.
4. Tests:
   - A testmysql test in the mysql driver package: `CloneFrom` into a recipient with a user schema and `super_read_only=ON` succeeds, and `super_read_only` is ON afterwards.
   - A failure test: an unreachable donor leaves `super_read_only` restored to ON.
   - Flip `e2eCloneResyncFixed`.

### F2: a successful resync raises a false `replica_stopped` alert and Telegram before "pulih"

**Root cause (verified on the monitor rig).** The Monitor judges a replica by combining two sources that are not synchronised:
- the store row (`replicas.state`, `replicas.paused`), re-read by `resolve` on every tick (`monitor.go` tick → `state.go:183`) and every report;
- the agent's last snapshot, which arrives only on the agent's 15 s check cadence.

The orchestrator changes MySQL and then writes the store within milliseconds:
- `replicaVerify` sets ready at `create_replica.go:726`;
- `StartReplica` sets `paused=0` at `controls.go:37`.

The tick (every 5 s) usually re-resolves before the agent's next report, so `derive` runs `DeriveReplica` on a snapshot taken before the change:
- **During a resync**, that snapshot shows no replication (reset in Prepare), replication configured but not started, or `super_read_only=OFF` (the `liftReadOnly` load). That gives "Berhenti (di luar panel)", and with the lock off also LockLost.
- `evaluate` then sees `active=true` and opens `replica_stopped` (no delay) and possibly `replica_unlocked`. `OnAlertsChanged` wakes the Notifier, which sends at once.
- The next agent report closes the alerts and the Notifier sends "pulih".

The same mechanism hits Start replika (I1) and create_replica success.

**Proposed fix (Monitor level, so every source of the race is covered):**

1. In `serverState`, add `awaitFresh time.Time`. In `resolve`, when a replica's `repState` or `repPaused` differs from what `s` held before (and `s.resolvedOK` was already true), set `s.awaitFresh = now` and queue a background fresh read with `goBg(func(ctx) { hub.Call(ctx, id, "monitor.snapshot", …); m.report(id, he) })`. This read does not go through `CheckNow`'s rate limit.
2. In `derive` for a replica: while `!s.awaitFresh.IsZero()` and the stored snapshot was taken before it (`s.receivedAt.Before(s.awaitFresh)`), keep the previous status. That is Belum siap during or just after a job, Dihentikan right after Start, and so on. Do not re-derive and do not move the alert clocks.
   - Clear `awaitFresh` when a report with `at >= awaitFresh` is ingested.
   - Also clear it after a bound (for example `awaitFresh + 2×CheckEvery + configureTimeout`), after which the normal rules (including staleness and Agent offline) apply.
   - A report that is in flight but older (taken before the change, received after) is correctly not trusted, because the `at >= awaitFresh` test uses `taken_ago`.
3. Optional but faster: let the orchestrator call `Alerts.ReplicaChanged(serverID)` (added to the `orchestrator.Alerts` interface, implemented by Monitor) right after `SetReplicaState(ready)` and `SetReplicaPaused(...)`. The 5 s tick detection is then not needed for speed.
4. Ordering hardening in `StopReplica`: write `paused=true` before `mysql.stopReplica` and revert it if the command fails. Otherwise a report landing between the command and the store write raises a false "Berhenti (di luar panel)". The window is tiny, but the fix is free.
5. Tests:
   - Monitor tests: resync ready with a mid-job snapshot opens no alert and no `replica_unlocked`; Start from Dihentikan opens no alert; the fresh snapshot moves the replica to Sinkron; the bound expiry falls back to the normal rules.
   - Flip `e2eResyncQuietFixed`.
   - Add an E2E check that Stop→Start through the panel sends no "berhenti" message.

## Triage of the ledger's deferred minors

**Fix before merge.** These are cheap and user-facing, or they are already part of the fix wave:

1. **T7: English transport error text in `telegram_error`, in the SendTest flash, and on the Peringatan page.**
   - This is the main feedback for Review Focus 5 ("panel tanpa internet"), and the Global Constraint says user-facing errors are in Indonesian.
   - Example of what the user sees today: `Post "https://api.telegram.org/bot***/sendMessage": dial tcp …: i/o timeout`.
   - Fix: wrap as "Telegram tidak terjangkau: …" (or "Telegram menolak: …" for API refusals) and keep the raw text after it.
2. **T11: stale "serves no application yet" comment on the resync path.**
   - Where: `replicaPrepare` restarts MySQL unasked on a resync, and on a resync the replica may serve reads.
   - Fix: correct the comment, and say in the resync modal that MySQL on the replica may restart.
   - It is a one-liner and is in the code touched by F1.
3. **T11: Stop/Start `paused` flag race.** Fold into the F2 fix (step 4 above).
4. **T13: "Belum diterima dari master: 0 transaksi" shown when the gap was never computed.**
   - A non-DBA reads "0 transaksi" as proof of sync. Show "belum dihitung" instead.
   - Cheap, and in the area the F2 fix touches.
5. **T14: clearing the base URL does not persist.**
   - The empty field is pre-filled with the public URL, so a user cannot remove links.
   - Fix: make the field placeholder-only, or reword the help text. A one-line template change.
6. **T10: proto comment "ignored without Resync" is inaccurate.** A trivial fix, kept accurate for the F1 work in the same area.

**Stay deferred** (no user-visible harm at expected scale, or a tested invariant already holds). By task:

| Task | Deferred minors that can stay |
|---|---|
| T1 | all |
| T2 | all; driver stderr noise and Statfs outside the budget are acceptable |
| T3 | all |
| T4 | all |
| T5 | both lists, including English context errors from CheckNow (rare: only on agent timeout) |
| T6 | all; replica_stopped has no hysteresis, but flapping needs human START actions |
| T7 | the rest; Config has no LogValue, but nothing logs Config today (cheap to add later) |
| T8 | all |
| T9 | all; pin-file TOCTOU needs root on the host |
| T10 | the rest |
| T11 | the rest |
| T12 | all |
| T13 | the rest; "2–3 EventSource connections per page" is already resolved: app.js now shares one EventSource per page (`aliranSSE`) |
| T14 | the rest |
| T15 | all |

## Declined to judge

- **Telegram burst when many agents go offline at once** (for example a network blip on the panel host): one alert per server is the spec's model, and grouping is not specified.
- **Telegram 429 / `retry_after` handling:** the spec fixes a 60 s backoff.
- **The per-host HTTP/1.1 connection limit for SSE with many tabs:** a pre-existing Plan 2 pattern, and the page now uses one EventSource.
- **Monitor tick cost under the lock at 50 servers** (about 200 SQLite queries per 5 s): judged acceptable, and already a deferred minor (T5).
- **Panel running as root, MySQL CA verification, per-replica replication accounts, promote:** deferred to Plan 4 by the spec.
- **Audit IP from RemoteAddr only:** the plan deliberately ignores X-Forwarded-For.
- **No pruning of the alerts table:** the spec only says the history view shows 90 days, and growth is negligible.
- **Old panel with a new agent never releasing pins** (cancel, delete): the README orders "update the panel first", and the pin is fixable with the CLI.
- **MySQL 8.4 tagged GTIDs:** dbpanel targets and tests 8.0 and does not create tags (T4 deferred).
- **Writes to the replica while its lock is lifted during the F1-fixed CLONE:** the data is replaced by the clone anyway. This is a design choice for the fix wave (lift only `super_read_only` if they want a smaller window).
- **Whether a resync should drop databases the replica does not follow (M3):** reported as Minor and flagged as a spec question rather than an implementation defect.
- **Weekly summary DST and time-zone behaviour:** covered by the existing deferred minor (T8).

## Verdict

Not ready to merge. These must be fixed first:
- **F1** (Critical): CLONE resync is broken for every all-databases replica, and a failed attempt leaves the replica with no replication. The fix is described above and was verified against mysqld.
- **F2, generalised as I1:** it also hits Start replika and create_replica, so a routine Stop→Start sends a false "berhenti" Telegram most of the time.
- **I2:** the unlock instructions are not durable across a restart, and this matters most in the emergency-promotion box.
- **I3:** the spec 5.5 set-source guidance is missing.
- The six cheap triage items above.

With those fixed and `e2eCloneResyncFixed`/`e2eResyncQuietFixed` flipped to true and passing, the branch is ready to merge. The rest of the work is solid.
