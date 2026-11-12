# Final whole-branch review — Plan 5a (dua master / kehilangan data)

Range: 0912c5f..343898c (code). Reviewer: final seat, read-only, no test runs, no subagents.

How it was reviewed: I read the spec, the plan's Review Focus and Keputusan perencana, the global
constraints and the ledger extract; then the pre-split diffs in passes myself (agent/proto in full,
monitor/notify/store in full, orchestrator in full, web handlers/templates/app.js in full, README
added lines in part), and searched the test diffs for what is pinned. Risks were followed into source
at HEAD (graphify query first, then targeted reads): `agent/runtime.go`, `agent/monitor/monitor.go`,
`agent/monitor/changes.go`, `drivers/mysql/{promote_lock,quarantine,startlock_guard,pins,replication,configure,binlog}.go`,
`agent/cli.go`, `panel/monitor/{monitor,state,promote}.go`, `panel/orchestrator/{runner,create_replica,change_databases,controls,promote}.go`,
`panel/web/handlers_jobs.go`, `startlock_guard_test.go`. Nothing was executed; where a finding rests on
timing I say so and name the test that would settle it.

### Strengths

- **The start lock is built the way the spec's experiments demand.** File before line, line before
  file, one critical section (`managedConfigMu` + flock) around every writer of the managed config
  (`configure`, guard, quarantine, promote, CLI `set-role master`), the file re-checked against the
  config before it is removed, and `writeConfigBlock` carrying the line byte for byte through every
  other rewrite. I found no writer of the managed config outside `lockManagedConfig` and no lock-order
  inversion (promoteMu → managedConfigMu → config flock; promoteMu → promote-lock flock; never nested
  the other way; `seenMu`/`startLockMu` are leaves).
- **Release needs proof, and the proof is conservative**: role ≠ quarantined *and* MySQL answering with
  both flags ON from this run's own PERSIST, or `persisted_variables` ON/ON; `mysql.promote` always
  PERSISTs before releasing. `set-role replica` with MySQL down keeps the lock (pinned by a test).
- **The promote-lock guard change is faithful to 5.4**, including the availability trade-off: only
  replicas readable at lock time are listed, an unreadable one keeps the lock, a cut-short read refuses
  the lock instead of writing a short list, the unlock runs on its own context, and `forget-replica`
  is a flocked read-modify-write that cannot resurrect an older lock.
- **Fail-closed defaults across versions (5.8)**: an untyped connection list is "tidak bisa dipastikan",
  a quarantine answer without `start_locked` is unguarded, `check_every_sec` 3600 is applied as 300 and
  not refused, unknown events/fields are ignored by the older side. The base panel never sent
  `drop_existing` (checked at 0912c5f), so refusing it breaks no old panel.
- **Job confirmations are bound to what was shown and recomputed, not trusted**: token per job id and
  server, `execute` reloads the job from the store so `confirmDrop`'s write is what runs,
  `preflightFresh`/`addFresh` are deliberately never stored, the retry dialog groups the stored wipe list
  on the master's inventory (so an already-dropped replica-only name can still be acknowledged — no
  dead end), and `leftoverDatabases` only ever returns names the master has, so a wait-approval cannot
  smuggle a replica-only database past `wipe_only`.
- **`dropsData` vs. call sites**: every orchestrator call of `mysql.dropDatabases` (create_replica.go:724,
  change_databases.go:396, :554) and `mysql.cloneFrom` is behind a predicate branch; `mysql.dumpFrom`
  no longer drops on the agent side at all.
- **Snooze**: the unsnoozable set is exactly the spec's twelve kinds, taken from the kind constants;
  the digest is owed in the store and cleared only after Telegram accepted it.
- **Verbatim texts**: all four 5.2 texts, the seven 5.5 texts and the three 5.6 texts match the
  constraints character for character. The 5.10 test list is present (real-mysqld write-storm test,
  5 s at CheckEvery 300, interrupted release, old-agent quarantine upgrade, kept/open/partial lock,
  idle GTID server, P_S off, tokened retry + re-preflight, typed names, digest after restart).
- README is unusually honest about limits (limiter scope, untested MAC, the 5 s residual, flock only
  once both sides are the new binary).

### Issues

#### Critical (Must Fix)

None found. I looked specifically for: a path to `dropDatabases`/`cloneFrom` without a fresh
confirmation, a start-lock release without proof, a line left without its file, a guard path that
opens a locked master past an unread listed replica, and a deadlock between promoteMu, the config
lock and the two flocks.

#### Important (Should Fix)

**FR-1 — The `late` / `bypassed` start report is sent once and dropped when the panel is not connected.**
`internal/agent/drivers/mysql/quarantine.go:315-317` (clears `qStartPending` before reporting),
`startlock_guard.go:385` (`d.emit`), `promote_lock.go:681-692` (`emit` only logs "event not sent"),
`internal/agent/runtime.go:94-105` (`Emit` returns `ErrOffline` while `a.cur == nil`; no queue for
driver events — only health reports have the ring buffer).
- What's wrong: `monitor.quarantineStart` is the only carrier of "this old master was writable for a
  while" (nothing in `HealthSnapshot` repeats it). It is emitted in the first guard round after MySQL
  answers; if the WebSocket is not up at that instant the event is gone and the next ticks return early
  (`guard.up && sameRun`).
- Scenario: the old master is pinned quarantined; its agent is stopped (or crashed); someone starts
  MySQL and the lock does not hold (line removed while the agent was dead, `--init-file` on the
  service command line, a quarantine written by a pre-5a agent). The agent starts: monitor and
  connection start concurrently (`cli.go:167-171`), the first guard round runs at t≈0 and reaches
  `reportQuarantineStart` after one `--print-defaults` (~0.1 s) — racing the mTLS dial. Or simply: the
  panel is unreachable at that moment. The agent puts read-only back, logs a warning locally, and the
  panel never opens `master_start_late`.
- Why it matters: this is the alert that tells the operator to run "Periksa data tertinggal" before
  rebuilding. Without it, a leftover check done *before* that start still reads "0 transaksi", the
  rebuild form shows no warning, and the writes taken while it was writable are wiped by the rebuild.
  README (l. 1283-1286) promises "agent melaporkan 'sempat bisa ditulis' begitu tersambung" — the code
  does not do that. Same pattern, lower stakes: `quarantineStartFailed` (`bootFailAt` advanced before
  the emit) and an `ok` that should close `bypassed`/`blocked`.
- Fix: make `emit` return the error and keep the unreported start result (result, bypassed, init_file)
  until an `Emit` succeeds, retried every guard round; or, sturdier, carry the last start result and
  its time in `StartLockState` so the panel derives the alert from health reports (which are buffered
  and repeated). Do the same for `bootFailAt`.
- Test: `TestQuarantineStartReportRetriedUntilDelivered` — guard rig with an Emitter that fails N
  times: a late start yields exactly one delivered `late` event after the emitter recovers, none lost,
  none duplicated; panel-side `TestStartLateFromSnapshotAfterReconnect` if the snapshot route is taken.

**FR-2 — A change of the start lock's state is not a "change" for the agent's reporter.**
`internal/agent/monitor/changes.go:25-43` (no `StartLock` term), `drivers/mysql/health.go:51-54`
("kunci start belum diperiksa agent"), panel rule `monitor/alerts.go` (`master_start_unguarded` opens
on `!Locked`).
- What's wrong: `master_start_unguarded` follows the last health report, but a report is sent only on
  `changed()` or every `SummaryEvery` (default 120 s, configurable to 3600 s). `StartLock` going
  Locked→not Locked (an operator adds an `init_file` in a later-read file, the file's mode changes and
  cannot be repaired, a MAC profile appears) or back is found by the guard within a minute but reaches
  the panel only with the next summary.
- Second effect (timing-dependent, not run): at every agent start on a quarantined server the first
  snapshot(s) can be taken before the guard's first check (`Snapshot` then reports "belum diperiksa").
  If the answer to the panel's `monitor.configure` is such a snapshot, the panel opens a Critical,
  unsnoozable "master lama belum terjamin terkunci…" that stays until the next summary, then
  "pulih". Agent restarts happen at every upgrade (which 5a requires on all servers).
- Why it matters: up to an hour of a wrong answer, in both directions, on the alert that says whether
  it is safe to start the old master's MySQL; and false Criticals teach people to ignore the kind.
- Fix: add `StartLock` (nil-ness, `Present`, `Locked`, `Reason`) to `changed()`; and have `Snapshot`
  compute `startLockStatus` itself when the state is unset on a quarantined server (it only reads files
  and runs `--print-defaults`), instead of reporting "belum diperiksa".
- Test: `TestChangedOnStartLock` (table: nil↔set, Locked flip, Reason change → true);
  `TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock` (driver with an intact lock, no guard
  tick yet → `StartLock.Locked` true).

**FR-3 — The hosts of a kept promote lock can be withheld for up to an hour (ledger Task 5, "ops-relevant").**
`internal/agent/drivers/mysql/promote_lock.go:627-634` (`keepLock`: one `keptAt` for every reason).
- What's wrong: `keptEvery` throttles by time only. If the guard first kept the lock for another reason
  (candidate unreachable at the deadline) and the candidate then answers while a listed replica does
  not, the `kept` event that carries `Hosts` is suppressed until an hour after the first one. The
  same holds when the unreadable set changes.
- Scenario: a planned promote fails in a network blip; at the deadline the candidate is unreachable
  (generic `master_lock_expired` notice, whose advice is "Buka kunci master" — which the agent
  refuses while the lock file stands, `pins.go:698-706`); a minute later the candidate reads read-only twice but replica
  R is still cut off. The master now stays read-only *because of R*, and the Critical
  `master_lock_kept` with the `forget-replica` / `unlock-master` instructions (spec 5.4) does not
  appear for up to 60 minutes. The hospital's application cannot write the whole time.
- Fix: throttle per reason class — report at once when the set of hosts differs from the last one
  reported (including empty→non-empty), keep the hourly cadence only for an unchanged report.
- Test: `TestKeptHostsReportedAtOnceAfterAnotherKeepReason` (candidate unreachable → kept; candidate
  read-only + listed replica down within the hour → a second event with Hosts);
  `TestKeptHostsChangeReported`.

#### Minor (Nice to Have)

**FR-4 — A start that failed because the lock file was *missing* is never reported (ledger Task 3 m3; plan Review Focus 2; README l. 1275-1281).**
`startlock_guard.go:123-127` runs `keepStartLock` (which rewrites the file, new mtime) before
`watchBootFailure`, which only takes log lines newer than the file's mtime (`:278`, `:303`).
Scenario exactly as Review Focus 2 words it (file deleted while the agent is dead, MySQL refuses to
start, agent comes back): the agent heals the file silently and no `master_start_blocked` is raised;
MySQL stays down (Ubuntu's unit does not restart on exit 1) and nobody is told it can be started now.
Data is safe. Fix: when `keepStartLock` had to recreate the file, scan the log from the last check
time (or agent start) instead of the new mtime; or correct README/Review Focus wording. Test:
`TestBootstrapFailureReportedWhenGuardRecreatesFile`.

**FR-5 — add_databases re-reads the replica at the dump step only when `AddDrop` is non-empty (ledger Task 13 m1 SAFETY).**
`internal/panel/orchestrator/change_databases.go:311`. A job whose step 0 found nothing existing is not
a "dropping job", so its Retry from step 1 or 2 is tokenless, has no 24 h limit and loads the dump over
whatever exists by then (same-named tables replaced; R-058's path) without the typed-name wait.
Needs a database of that name created on the replica between the first run and the retry, so
unlikely; but the fix is one condition: when `!jr.addFresh && !reattach`, call `addExisting` and wait
on a non-empty list whatever `AddDrop` holds. Test: `TestAddRetryAtDumpWaitsForDatabaseCreatedMeanwhile`.

**FR-6 — Inventory truncation is still silent (Ruling 18, not fixed at HEAD).**
`orchestrator/runner.go:238-240`. Fail-open only for `addExisting` on a replica with more than 5000
databases; `enforceWipeOnly` fails safe. Far outside the target deployments, cheap to close: return a
truncation flag/error from `RefreshInventory` and have both callers refuse. Test:
`TestAddExistingRefusesTruncatedInventory`.

**FR-7 — The candidate check refuses any replica whose start-lock line is present, which contradicts planner decisions 5/6.**
`orchestrator/promote_view.go:269-270`. Decision 5 accepts "with performance_schema off and a restarted
agent the lock stays until promote releases it"; the agent does release it at `mysql.promote`
(`releaseForPromote`), but the panel never lets that replica be chosen, and the text
("agent-nya belum bisa melepasnya") gives no remedy. Only reachable with `performance_schema=OFF`
plus an agent restart while a replica still carries the line; the cost, if hit, is a refused
emergency candidate. Fix: let the candidate through and rely on the agent's refusal, or refuse only
when the agent reported a failed release, and say what to do. Test: extend
`TestPromoteViewRefusesCandidateWithStartLock`.

**FR-8 — Snooze digest bookkeeping edges.**
(a) `store/servers.go:230-233`: a new snooze started after the previous ended but before its digest
went out resets `snooze_from`; the first snooze's digest (the alerts that opened and closed silently)
is lost (ledger Task 10). Keep `snooze_from` when it is non-NULL. (b) `notify/notifier.go:520` +
`servers.go:291`: `ClearSnoozeFrom` is unconditional; a snooze started between the Telegram send and
the clear loses its start. Clear with `WHERE snooze_from = ? AND snooze_until = ?`. Tests:
`TestResnoozeBeforeDigestKeepsFrom`, `TestClearSnoozeFromOnlyForTheSpanSent`.

**FR-9 — `forget-replica` ergonomics in the one moment it is needed.**
`agent/cli.go:436-460`. (a) The alert names `host:port`; the command takes `host` and answers
"`10.0.0.5:3306 tidak ada di catatan`" for a pasted entry. Accept `host:port` (strip the port).
(b) Forgetting a replica that is still connected removes it from `replicas-seen` until the agent's
hourly refresh (the agent's in-memory "last written" set still equals what it observes), and the old
master refuses a quarantine under a host it never recorded. The CLI can refuse or warn when the host
has a Binlog Dump thread now. Tests in `cli_test.go`.

**FR-10 — The `master_lock_kept` alert gives `unlock-master` without the caution the README gives.**
`panel/monitor/alerts.go:470-473`. README (l. 650-652) says "pastikan dulu replika itu tidak sedang
menjadi master di sisi jaringan lain"; the alert, which is what a person reads mid-failover, does
not. Planner text; add the clause.

**FR-11 — Driver warnings in the guard loop are not rate-limited (Task 14 ⚠️1; global constraint "1 per menit").**
My verdict after enumerating every `d.warn` reachable from `GuardTick`/`RecordReplicas`: nothing
repeats every 5 s in a steady state; three repeat while a fault persists or flaps:
`promote_lock.go` "promote lock expired but another replica is a writable master…; quarantining"
(every round while `selfQuarantine` fails — that branch does not set `nextRead`), `ensureLocked`
"super_read_only was off; locking again" (every round while something keeps turning it off), and the
SHOW REPLICAS warning (`promote_files.go:557-558`, logs again each time the error flips; ledger Task 4
m1). Everything else is transition-bound or behind `startLockNextTry`/`keptEvery`/`warnLimiter`.
Fix if cheap: give the driver the same limiter. README already states the narrower truth.

**FR-12 — `mysql.recordReplicas` (10 s bound) runs inside `repointMu` (ledger Task 8).**
`orchestrator/promote.go:1235`. A hung new-master agent adds 10 s per repointed replica while
quarantine sends wait behind the same mutex. Bounded and small for 1–3 replicas; call it once after
the repoints, outside the mutex.

**FR-13 — Guard housekeeping details on a quarantined server.**
(a) `startlock_guard.go:125` probes `localGreeting` every 5 s: one aborted handshake per round on the
old master (`Aborted_connects` grows, notes at verbosity 3) and a short "unauthenticated user"
session that `mysql.connections` now counts — a rare false "1 koneksi" on the cutover screen/rebuild
form. `guardQuarantined` already learns up/down from `h.Open`; use that. (b) `GuardTick` runs the
start-lock housekeeping before `guardQuarantined`; read-only enforcement closes a live write window
and should come first (a stuck `--print-defaults` delays it by one tick budget).

**FR-14 — `lock-master` read-then-write is not one critical section (ledger Task 5, "safety-relevant").**
`agent/cli.go:373-389`. It can replace a lock `lockForPromote` wrote in between with a `cli` lock.
The result fails closed (master stays read-only until `unlock-master`, which the same person runs),
so not a merge blocker; the flock now exists, so `WriteLockIfNone` under it is a few lines.

**FR-15 — `dropsData` for create_replica is an enumeration that will rot.**
`orchestrator/runner.go:577-590`. Ruling 16 added `cloneLeft`; the ledger still records a tokenless
Retry of a plain create that failed at step ≤ 2 choosing CLONE days later on the original form's tick.
Simpler and stricter: every create_replica whose copy is not made yet drops data. Cost: one password
on retries of an early failure.

**FR-16 — README precision (ledger Task 14 m1, m2).** `--print-defaults` runs once a minute, not in
every 5 s round (l. 1252-1256); the lock opens one recheck plus the grace after `forget-replica`, and
also when a writable listed replica lacks this server's transactions (l. 648-649, 689-691). Fix with FR-1/FR-4's
README corrections.

**FR-17 — Tests that would have caught the Important items are absent**: nothing asserts that a
start-lock change produces a report, that a start report survives an offline emitter, or that a
second keep reason reports its hosts.

### Ledger triage

Rulings — I agree with all of 1-20. Notes: **1/4** (e2e changed): right, the old assertion is the hole
5.2 closes. **7/11** (flocks): right, and the lock ordering is clean. **12** (replication threads
excluded by exact instrument name): right; server-chosen names cannot hide a client session, and the
alternative trains people to tick the data-loss box. **16** (clone-based create needs a token): right,
and I would widen it (FR-15). **18**: stands but is **not done at HEAD** (FR-6). **9** (no squash):
fine, squash at merge. **19/20**: fine.

Deferred lines marked safety-relevant / SAFETY / FINAL REVIEW / final wave:
- Task 4 m2 (candidate host not recorded if its thread drops) — **already fixed at HEAD** (Rulings 10/11:
  `observeReplicas(…, addr)` records the matched address on both the normal and the processlist-error
  path; pinned by `TestLockForPromoteRecordsCandidate`, `TestObserveReplicasRecordsSeenWhenProcesslistFails`).
- Task 5 kept-event Hosts share `keptAt` — **must fix before merge** (FR-3).
- Task 5 `lock-master` check/write race — **fix if cheap**; fails closed (FR-14).
- Task 5 >64 listed replicas never fit the tick — **leave**; fails closed, unreachable at this scale, documented residual.
- Task 5 forget-replica vs guard (concern 2) — **fixed at HEAD** (flock; guard never writes the lock back).
- Task 8 recordReplicas under `repointMu` — **fix if cheap** (FR-12).
- Task 10 `SetSnooze` resets `snooze_from` — **fix if cheap** (FR-8).
- Task 12 residual (tokenless early Retry may pick CLONE) — **fix if cheap** via FR-15; otherwise leave, as recorded.
- Ruling 18 inventory truncation — **fix if cheap** (FR-6); not a blocker at 5000 databases.
- Task 13 m1 SAFETY (re-read at dump step) — **fix if cheap** (FR-5).
- Task 13 m3 SAFETY (AddExisting equals a followed database without case) — **fix if cheap**; I could
  only reach a wrong drop with `lower_case_table_names` differing between master and replica, which
  the preflight is given (`PeerLowerCaseTableNames`); a three-line guard is still worth having.
- Task 14 ⚠️1 (guard warnings every 5 s?) — **verdict: no steady-state repeat; three fault-bound
  repeats; fix if cheap** (FR-11).
- Task 3 m3 (missing-file boot failure unreported) — **fix if cheap, or correct README and Review Focus 2** (FR-4).
- Task 3 I1 ⚠️(c) (promoteMu around `startLockProved`/`qUp`) — re-checked: every reader and writer
  (`guardStartLock`, `guardQuarantined`, `selfQuarantine`, `quarantine`, `requarantine`,
  `releaseForPromote`) runs under promoteMu. **OK.**
- Task 7 "tell planner/user" (kept+Hosts leaves `master_lock_expired` open; host change sends no new
  message; a quarantined server that never reported opens no unguarded alert) — **leave**; the first
  two are cosmetic, the third is covered by `agent_offline` and by the quarantine being pending.

Other deferred lines:
- Task 3 m-R2, m2, m6, m11, m12, m13, m-a — **leave**: manual-edit or root-only preconditions, bounded
  delays, or already documented residuals. m12 loses at most one start report around a rebuild load;
  FR-1's fix (keeping the unreported result) covers it for free.
- Task 5 lock without `Readable` reads no other replica — **leave**; unreachable (cred is memory-only, an old agent's lock hits the keep path).
- Task 5 exact-string dedup in `readableAtLock`, `flockLock` on `context.Background()`, forget-replica exit 1 — **leave**.
- Task 6: `AgentIndistinct` doc, unused export, `replica_monitor` not on the list (safe direction), X mid-login unobserved — **leave**; `incompleteLostUnread` wording — owner's eye.
- Task 9: stale count label accepted by design, `LocalAdminTotal` unused, double log — **leave**.
- Task 10: digest retried each tick without backoff, one dialog per alerts row — **leave**.
- Task 11: refusal text says "Resync ulang" on create/rebuild reruns — **fix if cheap** (wording); truncation → FR-6; the rest leave.
- Task 12: 409 vs 400, stale job still offers "Ulangi", DropsData error → 500, `prepareRetry` not gated,
  duplicated status switch, WaitReconnect needs a token, `cloneLeft` on an empty GTID set, panel restart
  during CLONE redoes the copy — **leave** (all fail closed); no-test-at-exactly-24 h — add with FR-15 if touched.
- Task 13 m4 wording — owner's eye; the lead-in "Database yang pernah dilepas tanpa hapus data:" narrows
  a note that also applies to a database created directly on the replica. m2, m5-m7 — cosmetic.
- Task 14 m1/m2 README — FR-16; m3 helper format match — leave; m4 empty commit — Ruling 9.
- Plainly cosmetic, grouped, **leave**: Task 3 m7-m10, Task 4 m3/m4, Task 5 test-shape notes, Task 7
  comment/dead-code notes, Task 8 doc comment, wip/empty commits.

### Declined to judge

- `mysql.unlockPromote` (panel-initiated rollback of a planned promote) opens the master after one
  read of the candidate and never reads `PromoteLock.Readable` — unchanged by 5a; spec 5.4 names the
  guard only. An honest panel sends it only for its own job, but a stale job's cancel after an
  emergency promote whose quarantine was refused would reach it. Recommended below.
- Panel restart resumes a data-dropping job without a token and without the 24 h limit (it does
  re-run the preflight) — spec 5.6 names "Ulangi" and "Setujui" only.
- `dbpanel agent set-role quarantined` writes neither read-only nor the start lock itself; it relies
  on a running agent (README says so) — spec 5.2 assigns the lock to quarantine and the guard.
- `set-role master` needs the agent's secrets key and MySQL layout to load just to remove a file and
  a line; a corrupted `mysql.cred`/`secrets.key` blocks the emergency escape hatch — spec says only
  "bila pelepasan gagal, peran tidak diubah".
- An agent restart in the middle of a rebuild load reports a `late` start (read-only was lifted by
  the panel's own load) — pre-existing guard behaviour, now with an alert attached.
- Reusing a deleted old master as a new master while its line is still present (≤ 5 s, or the P_S-off
  corner) can restart it read-only — spec defers reuse to runbook 5b.
- `readableAtLock` under certificate verification reads replicas by their processlist host; a
  certificate that does not name that host makes the replica "unreadable at lock time" and so
  unguarded — pre-existing semantics of the read it replaced.
- The WaitWipe approval text says "disalin ulang dari master" for every listed name; correct today
  because `leftoverDatabases` lists only names the master has — no finding, noted so the coupling is known.
- SELinux/RPM layouts where the lock file lands in `/etc` — accepted in spec 7 and decisions 1/2.
- Global Telegram kind switches can still turn off the new Critical kinds — decision 21, left to 5e.
- OWASP-lens items (token handling, agent text in HTML/Telegram) — left to the parallel security seats;
  I saw `agentText` bounding and template escaping in passing and nothing to report.

### Recommendations

1. Fix FR-1, FR-2, FR-3 before merge; each is a small, local change with a unit test, and FR-1/FR-2
   together make the start-lock alerts trustworthy in both directions.
2. In the same wave, the cheap ledger items: FR-5, FR-6, FR-8, FR-9(a), FR-10, FR-16.
3. Consider carrying safety facts in health reports rather than one-shot events as a rule: reports
   are buffered, ordered and repeated; events are not. `late`, `bypassed`, `blocked` and the kept
   hosts all fit in `StartLockState` / a lock state field.
4. Have `mysql.unlockPromote` use `readListed` too and refuse (or self-quarantine) when a listed
   replica is a writable master with this server's transactions — the helper exists.
5. Simplify `dropsData` for create_replica (FR-15) and add a table test that walks every setter of
   `Method`, `SnapshotGTID`, `AdminReplaced`, `WipeList`, `WaitReason`.
6. Squash the wip/empty commits at merge.

### Assessment

**Ready to merge?** With fixes

**Reasoning:** The data-dropping and two-master paths the spec names are closed and I found no
Critical defect, but three alert-delivery gaps (a lost "sempat bisa ditulis" report, start-lock state
that reaches the panel late or falsely, kept-lock hosts withheld for an hour) undercut exactly the
signals an operator relies on mid-failover and are each cheap to fix and pin.
