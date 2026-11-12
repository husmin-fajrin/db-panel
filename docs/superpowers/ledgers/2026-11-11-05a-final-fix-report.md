# Fix wave report — Plan 5a final review

Branch `claude/install-superpowers-plugin-ev2k9i`, base 765427b. Complete. Commit range a9545a2..e138d86, all pushed.

## Status table

| Finding | Status | Commit | Pinning test(s) |
|---|---|---|---|
| FR-1 = SA-5 | FIXED | a9545a2 | `TestQuarantineStartReportRetriedUntilDelivered` (late, start failed) |
| FR-2 | FIXED | 33ad13f | `TestChangedOnStartLock`, `TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock` |
| FR-3 | FIXED | 2d52c10 | `TestKeptHostsReportedAtOnceAfterAnotherKeepReason` (+ subtest `TestKeptHostsChangeReported`) |
| SP-1 | FIXED | daa12b9 | orchestrator + web `TestApproveRebuildRechecksConnections` |
| SP-2 | FIXED | a70a82a, 427ca73 | `TestStartLateSurvivesRebuildUntilCopied` (monitor), `TestLateStartDistrustsOlderLeftoverCheck` (web) |
| SA-4 | FIXED | 8d9be83, ee3ab70 | `TestSeenPortsCapped`, `TestSeenFileNeverOverReadLimit`, `TestLockTooLargeNotWritten`, `TestLockForPromoteFanOutBounded` (count, budget, ports full), `TestForgetReplicaPortCLI` |
| FR-9 (a) | FIXED | 8d9be83 | `TestForgetReplicaCLI` (host:port part) |
| SA-6 | FIXED | b7dd3db | `TestReadTailOnlyRegularFiles` |
| FR-5 | FIXED | df9ffc1 | `TestAddRetryAtDumpWaitsForDatabaseCreatedMeanwhile` |
| FR-6 | FIXED | df9ffc1 | `TestAddExistingRefusesTruncatedInventory`, `TestEnforceWipeOnlyRefusesTruncatedInventory` |
| Task 13 m3 | FIXED | df9ffc1 | `TestAddExistingRefusesFollowedNameIgnoringCase` |
| FR-14 | FIXED | aff8906 | `TestLockMasterKeepsLockWrittenMeanwhile` |
| FR-12 | FIXED | c51591a | `TestRecordReplicasOutsideRepointMu` |
| FR-8 (a)(b) | FIXED | 66ff084 | `TestResnoozeBeforeDigestKeepsFrom`, `TestClearSnoozeFromOnlyForTheSpanSent` |
| FR-4 | FIXED | 67e172f | `TestBootstrapFailureReportedWhenGuardRecreatesFile` |
| FR-13 (a)(b) | FIXED | 67e172f | `TestQuarantineGuardMakesNoAbortedHandshake`, `TestGuardEnforcesReadOnlyBeforeStartLockHousekeeping` |
| FR-11 | FIXED | 67e172f | `TestGuardFaultWarningsLimited` |
| FR-7 | FIXED | 5832e4f | `TestPromoteViewRefusesCandidateWithStartLock` (extended) |
| SP-3 | FIXED | 9f87ccc | `TestHubLimitsAgentEvents` |
| SP-4 | FIXED | 7df4b48 | `TestRetryRefusedAfterMasterSwapped` |
| SP-5 | FIXED | 67fd2bd | `TestWipeWaitKeepsFullList` (orchestrator), `TestWipeApprovalListsEveryName` (web) |
| SP-9 | FIXED | 7c76beb | `TestLockKeptOversizeStillAlerts`, `TestUnreadableStartEventsStillAlert` |
| SA-10 | FIXED | 7c76beb | `TestReadPeerBoundsAnswer` |
| SP-11 | PARTLY: items 1-3 fixed; item 4 (validate inventory database names at `inventoryCall`, flag non-matching) not done | 69b05d8 | `TestStartAndKeptMessagesBoundAgentText` (extended), `TestConnectionsBoundAgentText` |
| SA-7 | PARTLY: fd-based owner/mode fix done; directory owner check not done (not small, see below) | 8e93bae | `TestStartLockFileFixNeverFollowsLink` |
| `mysql.unlockPromote` (Rec. 4) | FIXED | 81ccdad | `TestUnlockPromoteReadsListedReplicas` |
| FR-10 | FIXED | c2133b3 | `TestLockKeptWithHostsIsCritical` (exact text updated) |
| SA-11 | FIXED | c2133b3 | `TestUnlockMasterSaysWhatItBypasses`, `TestForgetReplicaCLI` (extended) |
| SA-12 | FIXED | c2133b3 | `TestStartLockPathRule` (extended) |
| Ledger Task 11 wording | FIXED | c2133b3 | `TestWipeOnlyRefusalAdviceFitsJobKind` |
| `pins.go` header comment | FIXED | c2133b3 | (comment) |
| FR-16, Task 14 m1/m2, SA-3 README note, README for this wave | FIXED | 2fc63a4 | (docs) |
| FR-15 = SP-7 | FIXED | b9ec203 | `TestCreateReplicaDropsUntilCopied` (table over Method × SnapshotGTID × AdminReplaced × WipeList × WaitReason × RebuildOldMaster), `TestRetryDumpCreateFailedAtCopyNeedsToken` |

## Per finding

### FR-1 = SA-5 — start reports survive a panel that is not connected

Design choice: keep the unreported result until a send succeeds (not the snapshot route). The driver keeps start
reports (`monitor.quarantineStart`, `monitor.quarantineStartFailed`) in an in-memory FIFO (`pendingStart`, at most 16;
past that the oldest plain `ok` goes first, else the oldest) and `GuardTick` sends what waits at the start of every
round (5 s), stopping at the first refusal. Why this one: it is local to the driver (no proto field, no panel change,
no compatibility question either way), and it survives the first guard round racing the dial — the report computed
at t≈0 is sent again 5 s later, and every 5 s after, until the session is up. What it does not survive: an agent
restart before delivery (the result is memory-only; a restarted agent re-reads the flags it PERSISTed and sees `ok`).
The snapshot route would have needed a new `StartLockState` field, panel derivation and ordering against the event.
`emit` now returns its error; other events keep their old best-effort behaviour.

RED (old code, new test):
```
go test -count=1 -run TestQuarantineStartReportRetriedUntilDelivered ./internal/agent/drivers/mysql/
--- FAIL: .../late: start events once connected = [], want one late
--- FAIL: .../start_failed: events once connected = [], want one monitor.quarantineStartFailed
```
GREEN: same command plus `TestQuarantineStartEvents|TestBootstrapFailureEventFromErrorLog` → `ok`.

### FR-2 — start lock changes reported at once; no false "belum diperiksa" at agent start

`monitor.changed` now has a `startLockChanged` term (nil-ness, `Present`, `Locked`, `Reason`). `Driver.Snapshot`, when
the guard has not checked the lock yet on a quarantined server, runs `startLockStatus` itself for the report (files +
`--print-defaults`, read-only; not stored, so the guard's own state stays the one it acts on). "kunci start belum
diperiksa agent" remains only when there is no MySQL host at all.

RED: `go test -run TestChangedOnStartLock ./internal/agent/monitor/` → `goes / locked flips / unlocked back / reason
changes / present flips: changed = false, want true`. `go test -run TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock
./internal/agent/drivers/mysql/` → `StartLock of the first snapshot = &{Present:false Locked:false Reason:kunci start
belum diperiksa agent ...}, want locked`.
GREEN: both `ok` (driver run with `-run 'TestFirstSnapshot...|Snapshot|StartLock'` → ok 150 s).

### FR-3 — kept-lock hosts not withheld by another keep reason's throttle

`keepLock` remembers the sorted host set it last reported; it reports at once when the set differs (including
empty→non-empty), and keeps the hourly cadence only for an unchanged set. `keptAt`/`keptHosts` are recorded only when
the event was delivered, so a kept report the panel did not get is tried again at the next recheck (one minute).

RED: `go test -run TestKeptHostsReportedAtOnceAfterAnotherKeepReason ./internal/agent/drivers/mysql/` → `events =
[monitor.lockExpired], want [monitor.lockExpired monitor.lockExpired]`. GREEN: with
`TestGuardKeeps|TestLockKept|TestGuardQuarantinesWhenCandidateIsMaster|TestGuardChecksListedReplicasBeforeUnlock` → ok.

### SP-1 — Setujui of an old master's rebuild re-reads its connections

`Runner.Approve` now calls `retryConnectionsClear` before `startMu` exactly like `Retry`, and `confirmDrop` applies the
`accept_connections` rule to approvals too (same rule as Retry: rebuild job, not acknowledged, not clear →
`ErrConnectionsRemain`; an unreadable list is not clear; the acknowledgement stored at job start never counts). Web: the
approve handler forwards `accept_connections` for a rebuild; the job page reads the old master's connections when a
rebuild job waits for approval (`jobPage.ApproveConns`) and renders the existing `accept-connections` partial in the
approve dialog (danger approvals) or beside the approve form (plain approvals), tied to `form="form-setujui"` (new
optional `.Form` of the partial; `retryView.Form`, `rebuildInfo.Form`). Design note: same rule for every rebuild
approval, whether or not the copy is made yet (the task asked for the Retry rule; the report's narrower "copy not made"
condition was not added).

RED: orchestrator `TestApproveRebuildRechecksConnections` → `Approve without acknowledging the connections = <nil>,
want ErrConnectionsRemain`; web test → `ack = {Confirmed:true ... AcceptConnections:false}` (and the checkbox missing).
GREEN: `go test ./internal/panel/orchestrator/` ok, `go test ./internal/panel/web/` ok.

### SP-2 — `master_start_late` survives a rebuild until the copy; forms distrust an older check

Monitor (`closeStartAlerts`): `master_start_late` is no longer closed by "not quarantined" while the server is a replica
whose state is not judged (creating/failed: copy not made); it closes once the rebuilt replica is ready/attention (data
replaced) or by a leftover check after it, as before. A cancelled rebuild (back to quarantined) leaves it open. Web:
`startLateOpen` (open `master_start_late`; store error counts as open) makes the rebuild form and the delete dialog show
a late-start warning instead of trusting the stored leftover result (the alert closes only once a check after it is
stored, so an open one means no check after the late start).

RED: monitor → `master_start_late closed when a rebuild made the server a replica in state creating`; web → body lacks
the warning. GREEN: monitor and web packages ok.

### SA-4 — recorded ports capped, lock fan-out bounded, records never larger than read back

- Ports: `maxSeenPorts = 8`. A new port past it is not recorded; the file gets a `penuh <RFC3339>` line (an older
  agent's parser skips it) and `RecordReplicaPortsSeen` returns `errSeenPortsFull` after the write (logged by the guard
  loop's per-minute limiter; not rewritten every round). `lockForPromote` refuses while the mark is present (fail
  closed: a replica at an unrecorded port would never be read), with a message naming the file and the remedy.
- `dbpanel agent forget-replica-port {<port>|semua}` (new sibling): one port, or every port plus the mark.
- Fan-out: at most `maxLockReads = 64` host:port reads (over → refused at once, naming both files and both forget
  commands); reads run under `lockReadBudget = 60 s` (panel call timeout is 2 min); over → refused, no lock written.
- `writeJSON` refuses any record (lock, promoted, promoting) larger than `maxRecordSize` (what `readJSON` reads back).
- replicas-seen: `writeSeen` drops the oldest hosts until the file fits `maxSeenSize`, writes, and returns
  `errSeenTrimmed` (logged; counted as written).
- Visibility: agent log (rate-limited) + the planned promotion's lock step fails in the panel with the message. No new
  health field (no protocol change).

RED: the new tests reference API that did not exist (`go vet` with the old `promote_files.go`, `promote_lock.go`,
`cli.go`: `undefined: errSeenPortsFull / errSeenTrimmed / readSeenPortsFull`, `undefined: cliSeenPortsPath`); no
behavioural RED was captured separately. GREEN: `-run 'TestSeenPortsCapped|TestSeenFileNeverOverReadLimit|TestLockTooLargeNotWritten|TestLockForPromoteFanOutBounded|TestSeenPortsFileFormatAndPrune|TestLockForPromoteRecordsCandidate|TestReadableAtLockParallel|TestDriverRecordsReplicasSeen|TestReplicasSeenPrune'` → ok (123 s); `go test -run TestForgetReplica ./internal/agent/` → ok.

### FR-9 (a) — `forget-replica` accepts `host:port`

The port is stripped (`net.SplitHostPort`); the recorded host is forgotten. Pinned in `TestForgetReplicaCLI`.

### SA-6 — error log opened safely

`readTail` opens with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, `fstat`s the descriptor and reads only a regular file
(bounded by `bootLogTail`, 64 KiB, as before). Anything else (FIFO, symlink, device) is "not readable": the guard logs it
at most once a minute (`bootLogWarnAt`) and reports nothing.
RED: `TestReadTailOnlyRegularFiles` → `ReadTail(fifo) blocked` (5 s watchdog). GREEN: with
`TestBootstrapFailureEventFromErrorLog` → ok.

### FR-5 — dump-step re-read whenever the run did not read the replica itself

`addDump` calls `dropAddExisting` when `(len(AddDrop) > 0 || !addFresh) && !reattach`; with nothing approved and a
database now present, the job waits (`WaitAddExisting`, typed names, token) instead of loading over it. Log line for an
empty `AddDrop` suppressed.
RED: `job ... status succeeded ..., want waiting`. GREEN: orchestrator package ok.

### FR-6 — truncated inventory is signalled; both callers fail closed

`inventoryCallCut`/`refreshInventory` return whether the list was cut at `maxInventoryDBs`; `RefreshInventory` (web) is
unchanged. `addExisting` and `enforceWipeOnly` refuse a cut list (`errInventoryCut`).
RED: add job `status succeeded ..., want failed`; resync failed with the old, unrelated wipe-only message.
GREEN: orchestrator package ok.

### Ledger Task 13 m3 — no drop of a name equal (ignoring case) to a followed database

`addExisting` fails the job when a database to drop equals, ignoring case, one in `st.Databases` (what the replica
already follows). RED: `status waiting ..., want failed`. GREEN: ok.

### FR-15 = SP-7 — every create_replica drops data until its copy is made

`dropsData(KindCreateReplica)`: `copyLeft := SnapshotGTID == "" && !AdminReplaced` replaces `cloneLeft` (method no
longer matters). So Retry and Approve of such a job need a token, are bound to the 24 h limit, and
`preflightBeforeDrop` re-runs the preflight before the copy. Cost: one password on the retry/approval of an early
failure of a plain create (as the review accepted).
RED: `TestRetryDumpCreateFailedAtCopyNeedsToken` → `Retry without confirmation = <nil>, want ErrJobNeedsConfirm`;
table test → `dropsData = false, want true` for plain/dump/foreign-source/reconnect/restart rows without a copy.
GREEN: `go test ./internal/panel/orchestrator/` ok. Not added: a test at exactly 24 h (wall-clock based; skipped).

### FR-14 — `lock-master` check and write in one critical section

New `Pins.WriteLockIfNone` (flock, read, write only when none). `lock-master` keeps its fast pre-check, PERSISTs
read-only, then writes through `WriteLockIfNone`; a lock that appeared meanwhile (a planned promotion's) is kept and the
CLI exits 1 naming it (MySQL stays read-only, which that lock wants anyway).
RED: `lock-master racing a promote lock = 0 ...; lock after the race = {Candidate:cli ...}`. GREEN: with
`TestCLILockUnlockMaster` → ok.

### FR-12 — `mysql.recordReplicas` outside `repointMu`

`repointReplica` no longer calls it; it returns `record` (receiver connected). The promote job's repoint step calls it
once after the loop (deferred), `RepointNow` after releasing `repointMu`, `onOnline` after its deferred unlock.
RED: `mysql.recordReplicas ran 2 times with repointMu held`. GREEN: orchestrator package ok.

### FR-8 — snooze digest bookkeeping

(a) `SetSnooze` keeps `snooze_from` when it is set (a digest is owed), so a new snooze before the digest went out
extends the owed span (the digest then also lists alerts of the unsnoozed gap — over-reporting, the safe side).
(b) `ClearSnoozeFrom(SnoozeSpan)` clears only `WHERE snooze_from = ? AND snooze_until = ?` (the span the digest
covered); 0 rows on an existing server is not an error; a missing server is still `ErrNotFound`.
RED: (a) `from 2026-11-11 11:00 ... want from 09:00`; (b) compile failure of the new signature in the test.
GREEN: store and notify packages ok.

### FR-4 — a start that failed on a missing lock file is reported

When `keepStartLock` has to write the file again, it records `bootSince` = the managed config's mtime (from before
the write); `watchBootFailure` scans the log from `min(file mtime, bootSince)`, so the failure logged before the file
was recreated is found. `bootSince` is cleared once MySQL answers. Duplicates are still prevented by `bootFailAt`.
The README claim is now true (no README correction needed for FR-4).
RED: `events = [], want one monitor.quarantineStartFailed`. GREEN: guard/start-lock tests ok (186 s run).

### FR-13 — guard housekeeping on a quarantined server

(b) `GuardTick` on a quarantined server runs `guardQuarantined` (read-only first) and then `guardStartLock`, whatever
the first returned. (a) `guardStartLock` no longer probes `localGreeting`; it uses `d.qUp`, which `guardQuarantined`
just set from its own `h.Open` (an authenticated session, no aborted handshake). `localGreeting` is still used by
`mysql.quarantine` (unchanged).
RED: (a) `Aborted_connects grew by 3 over three guard rounds, want 0`; (b) `super_read_only when the start lock was first
written: state 2, want on (1)`. GREEN: ok.

### FR-11 — the three fault-bound guard warnings are rate-limited

New `Driver.warnLimited` (once a minute per message, wall clock). Used for "promote lock expired but another replica is
a writable master…; quarantining", "promote lock present but super_read_only was off; locking again", and the SHOW
REPLICAS warning (which logged on every flip of its text; `portsErr` removed).
RED (temporary revert of the ensureLocked line): `warning logged 3 times in three rounds, want 1`. GREEN: with the
guard/seen tests → ok.

### FR-7 — the start-lock candidate refusal says what to do

Refusal kept. Truthful remedy from the code (`readOnlyProvedNow`): the agent releases a replica's start lock by itself
once `performance_schema.persisted_variables` shows both flags PERSISTed ON (or its own PERSIST in this run, which a
restarted agent lost); with `performance_schema` off it never can. The text now says so and offers "turn it on and
restart MySQL, or choose another candidate". RED: not run separately (the new assertions test words absent from the
old constant). GREEN: `TestPromoteViewRefusesCandidateWithStartLock` ok.

### SP-3 — agent events rate-limited beside the health limit

`Hub.OnEvent` limits `monitor.quarantineStart`, `monitor.quarantineStartFailed`, `monitor.lockExpired`,
`monitor.quarantined` per server: burst 10, one token per 5 s. Without a token the event still passes when no event
of its kind passed over the limit in the last 10 minutes; a kind is the type plus the result/action, taken only from
known words (an agent cannot mint kinds), so the first `late`/`bypassed`/`blocked`/`kept` after a flood of another kind
is never dropped. Drops go to the existing `Dropped` counter and its once-a-minute log. Job events and samples are not
touched.
RED: build failure (`undefined: eventBurst`). GREEN: `go test ./internal/panel/hub/` ok.

### SP-4 — a job from before a promotion is superseded

`supersededBy` (create/resync/add/remove kinds) refuses when the topology's master is not the job's `MasterServerID`
("master topologi ini sudah berganti sejak pekerjaan ini dibuat"); a job without it (0) counts as changed (fail closed).
RED: `Retry after the master changed was accepted`. GREEN: orchestrator package ok.

### SP-5 — the full drop list of a WaitWipe approval

New additive job-state field `WaitList` (`wait_list`), set with every `WaitWipe` wait; the approve dialog lists every
name from it (scrollable list). An older wait stored without it whose step message was cut (`…(dipotong)`) is refused
on Approve (`errWipeListCut`: cancel and start again).
RED: orchestrator build failure (`st.WaitList undefined`); web → body lacks the names. GREEN: both packages ok.

### SP-9 — an unreadable/oversized lock or start event still alerts

Panel: an oversized or unreadable `monitor.lockExpired` opens the generic "kept" `master_lock_expired` notice; an
oversized/unreadable `monitor.quarantineStart` opens `master_start_late` (new text; closes by "Periksa data
tertinggal", its remedy) and `monitor.quarantineStartFailed` opens `master_start_blocked`, each only when none is open
(an open alert keeps its message; nothing is closed). An unknown result is still ignored (5.8: a newer agent's value).
Agent: the kept event's `Reason` no longer repeats the hosts (they are in `Hosts`; the log line carries them).
RED: `no open master_lock_expired alert`, `no open master_start_late alert`. GREEN: monitor package ok.

### SA-10 — a peer's answer and error text are bounded

`ReadPeer` dials through `cfg.DialFunc` wrapping the connection in `limitedConn` (256 KiB per read, TLS included) and
cuts `PeerState.Err` to 300 bytes. RED: `Err is 2097181 bytes, want at most 512`. GREEN: with the peer/lock tests → ok.

### SP-11 — agent strings bounded (partly)

Fixed: (1) `le.Candidate` and `qe.NewSource` go through `agentText` (120) in `lockExpiredMessage`/`quarantinedMessage`;
(2) `ConnectionsResult.Unreadable`/`.Incomplete` are bounded (300, control/format characters removed) in
`connectionsOf`, which also bounds what the rebuild's confirmation text copies; (3) `StartLock.Reason` (300) and
`OperatorInitFiles` (each 300, at most 10 + "… dan N lagi") on the server page. New exported `monitor.AgentText`.
Not done: (4) validating database names at `inventoryCall` with the agent's pattern and flagging non-matching ones — it
touches every inventory consumer and the display of names; the report itself rates it display-only (the agent validates
every name it acts on). RED: monitor `lock expired message not bounded: 5261 bytes`; web body contains U+202E and the
long run. GREEN: monitor and web ok.

### SA-7 — start lock file fix on the descriptor (partly)

`writeStartLockFile` opens an existing file with `O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, reads it bounded, and fixes owner and
mode with `f.Chown`/`f.Chmod` on that descriptor (`fixStartLockFile`); never through the path. Not done: the check that
the directory (and its parents) is root-owned and not group/other-writable. Reason (stopped, not small): the `testmysql`
harness puts `my.cnf` in an instance directory owned by the mysqld user (`nobody`), which mysqld must write (socket,
pid, error log); the check would make every start-lock test report "not locked", so the harness layout would have to
change first. RED: build failure of the new test hook (the old code had no point to interpose; by reading, its
`Lstat`→`Chmod(path)` followed a link swapped in between). GREEN: `TestStartLockFileFixNeverFollowsLink` and the
start-lock tests (128 s) ok.

### `mysql.unlockPromote` reads the listed replicas (final review Rec. 4)

Before opening, it reads `PromoteLock.Readable` with the candidate's account (`readListed`): an unreadable entry refuses
and keeps the lock; a writable entry holding this server's transactions makes the server quarantine itself under it
(`selfQuarantine`, as the guard does) and the command is refused.
RED: `unlock with a listed replica unreadable = {OK:true}`. GREEN: with `TestUnlockPromote|TestUnlockUsesOwnContext|TestLockRequiresConnectedCandidate` → ok.

### Tier 3 texts

- FR-10: the `master_lock_kept` alert now carries the README caution before `unlock-master`. RED: exact-text test
  failed against the old code after it was updated; GREEN: monitor ok.
- SA-11: `unlock-master` of a lock with a candidate prints (stderr) that it opens without reading the candidate and the
  listed replicas; `forget-replica` prints that the guard no longer waits for that host; the unreadable-lock message no
  longer presents `unlock-master` as the repair. (No `--yes` gate was added; the task asked for the texts.)
- SA-12: `StartLockPath` refuses a directory containing `#`, `'`, `"`, `\` or control characters.
- Ledger Task 11: the `enforceWipeOnly` refusal advises "ulangi pekerjaan ini dari halamannya dan akui database itu di
  dialog Ulangi" on create/rebuild jobs; resync keeps "mulai Resync ulang lagi dari halaman replika".
- `pins.go` header: now says what holds and names SA-1/SA-2 as open paths.
- README: `--print-defaults` once a minute; kept-lock reporting; unlock timing after `forget-replica` (next recheck ≤1
  min, then the 60 s grace); the lock also opens when a writable listed replica lacks this server's transactions;
  retire replicas with `forget-replica` and why (SA-3); port cap, fan-out bounds, `forget-replica-port`, host:port;
  start report delivery; late-start alert vs. rebuild/delete; unreadable events; confirmation scope (FR-15, SP-1, SP-4,
  SP-5); event rate limit.
RED for SA-12, Task 11 and SA-11's forget-replica line: not run separately (new assertions on text that did not exist).

## Existing tests whose expectations changed

- Test helpers only: `eventLog` (agent driver tests) gained an offline switch; `fakePeer` gained per-host answers.
- FR-15 (`internal/panel/orchestrator/runner_test.go` `TestDropsData`): rows "create, foreign source wait",
  "create, reconnect wait", "create, plain", "create, dump onto empty target" changed from `false` to `true` (the copy
  is not made in those states, which is now the rule); added rows "reconnect wait after the copy" (`false`) and
  "dump copied" (`false`). The old comment about Ruling 16 was replaced.
- FR-15: Retry/Approve calls of plain create jobs in tests about other behaviour now pass `JobAck{Confirmed: true}`
  instead of `JobAck{}` (no assertion changed): `create_replica_test.go` `TestFailsWhenAgentDropsDuringSnapshot`,
  `TestJobFailedReachesAlerts`; `snapshot_test.go` `TestForeignSourceNamedBeforeReset`, `TestOfflineMasterLogWording`;
  `superseded_test.go` `TestRetryReopensBeforeNewJobs`, `TestCancelBetweenReopenAndLaunchIsCanceled`;
  `upgrade_test.go` `TestOldCreateReplicaJobResumesAndRetries`.
- FR-12 (`record_replicas_test.go` `TestRecordReplicasCalledAfterVerify/promote repoints to the new master`): the new
  master gets 1 `mysql.recordReplicas` (once after the repoints) instead of 2 (one per repointed replica) — the finding
  asked for exactly this.
- FR-8 (`store/topologies_test.go`): `TestSetSnoozeKeepsFromWhenExtended` now clears the owed digest
  (`ClearSnoozeFrom`) before asserting that a later snooze starts anew (without the clear, FR-8 a keeps the owed
  start); `TestEndedSnoozesAndClear` calls the new `ClearSnoozeFrom(SnoozeSpan)` signature (same assertions).
- SP-9 (`monitor/alerts_test.go` `TestMasterLockExpiredFromEvent`): an unreadable `monitor.lockExpired` used to be
  asserted to open nothing; it now opens the generic kept notice (the finding's fix). Assertion changed accordingly.
- SP-1 (`internal/panel/e2e_helpers_test.go`): the e2e rebuild's approvals tick `accept_connections` when the approve
  dialog asks for it (`approveJobAck`), since Setujui now re-reads the connections.
- FR-10 (`monitor/alerts_test.go` `TestLockKeptWithHostsIsCritical`): the exact expected kept-lock message gained the
  caution clause.
- FR-15: `TestFailsWhenAgentDropsDuringSnapshot` expected replica commands after the retry now start with
  `mysql.preflight` before `mysql.dumpFrom` (the fresh preflight SP-7 asked for).

## Protocol / compatibility notes

- No proto change. No SQLite migration. New job-state field `wait_list` is additive JSON (`omitempty`); an older panel
  ignores it; a newer panel reading an older wait without it refuses only a cut list (SP-5).
- Agent ↔ panel both ways: events and their shapes are unchanged. A new agent sends start reports again until delivered
  (an old panel takes them as before; a repeat of a known result is idempotent there). The kept event's `Reason` no
  longer lists hosts (an old panel never showed the reason). A new panel rate-limits the four lock/start events; an old
  agent sends them far below the limit. A new panel treats an unreadable start/lock event as the worst case (safe
  direction); an unknown result is still ignored (a newer agent's value).
- The replica-ports file gains a `penuh <RFC3339>` line; an older agent's parser skips it (and would drop it on its next
  write — only one binary runs per host).
- `forget-replica-port` is a new local CLI command; `forget-replica` accepts `host:port`.

## Texts for the owner's eye

New or changed Indonesian texts (file:line at the final commit):

- `internal/panel/web/handlers_oldmaster.go:711` "MySQL %s sempat bisa ditulis saat menyala setelah Periksa data tertinggal terakhir; tulisan itu belum diperiksa" — joined with `:763` ": transaksi yang tidak sampai ke master baru akan hilang dari server ini. Jalankan Periksa data tertinggal (dan Amankan bila ada) di halaman server ini dulu." (rebuild form) and `:469` ": setelah server ini dihapus dari panel, tulisan itu tidak bisa diperiksa atau diamankan lagi lewat panel." (delete dialog) (SP-2)
- `internal/panel/monitor/promote.go:649` "laporan menyala master lama dari agent tidak terbaca; anggap master lama sempat bisa ditulis dan jalankan Periksa data tertinggal" (SP-9; the 5.2 verbatim texts are unchanged)
- `internal/panel/monitor/alerts.go:476` kept-lock alert: "Untuk membuka kunci sekarang (pastikan dulu replika itu tidak sedang menjadi master di sisi jaringan lain: unlock-master membuka master tanpa memeriksanya): sudo dbpanel agent unlock-master" (FR-10)
- `internal/panel/orchestrator/promote_view.go:91` "Kunci start dari karantina lama masih terpasang di server ini; agent-nya belum bisa melepasnya. Agent melepasnya sendiri dalam semenit setelah performance_schema menunjukkan read_only dan super_read_only tersimpan ON (SET PERSIST); bila performance_schema dimatikan di server ini, nyalakan lalu restart MySQL-nya, atau pilih kandidat lain" (FR-7; was a planner text, extended)
- `internal/panel/orchestrator/create_replica.go:1100` "ulangi pekerjaan ini dari halamannya dan akui database itu di dialog Ulangi" (Task 11)
- `internal/panel/orchestrator/runner.go:242` "daftar database %s lebih dari %d; panel tidak bisa memeriksa semuanya, pekerjaan ditolak agar tidak ada database yang dibuang atau ditimpa tanpa dicek" (FR-6; %s = "master"/"replika")
- `internal/panel/orchestrator/change_databases.go:500` "database %s sama dengan database %s yang sudah diikuti replika ini bila huruf besar-kecil tidak dibedakan; pekerjaan ditolak agar database itu tidak ikut dibuang" (Task 13 m3)
- `internal/panel/orchestrator/runner.go:968` "master topologi ini sudah berganti sejak pekerjaan ini dibuat" (SP-4)
- `internal/panel/orchestrator/runner.go:808` "daftar database yang akan dihapus terlalu panjang dan dipotong di halaman ini; batalkan pekerjaan ini lalu mulai lagi agar daftar lengkapnya ditampilkan" (SP-5)
- `web/templates/job_detail.html:51` "Semua {{len .}} database yang dihapus:" (SP-5)
- `internal/agent/drivers/mysql/promote_lock.go:220` "catatan port replika %s penuh (%d port; port lain tidak dicatat sejak %s), jadi tidak semua replika bisa dibaca; master tidak dikunci. Periksa berkas itu, lalu jalankan di server ini: sudo dbpanel agent forget-replica-port semua (port replika yang tersambung dicatat lagi dalam beberapa detik)" (SA-4)
- `internal/agent/drivers/mysql/promote_lock.go:227` "terlalu banyak replika tercatat untuk dibaca sebelum master dikunci (%d pembacaan host:port, paling banyak %d); master tidak dikunci. Hapus replika yang sudah tidak dipakai dengan: sudo dbpanel agent forget-replica <host> (catatan %s) dan port yang tidak dipakai dengan: sudo dbpanel agent forget-replica-port <port> (catatan %s)" (SA-4)
- `internal/agent/drivers/mysql/promote_lock.go:243` "replika lain belum selesai dibaca dalam %s; master tidak dikunci. Replika yang tercatat ada di %s dan %s; hapus yang sudah tidak dipakai dengan: sudo dbpanel agent forget-replica <host>" (SA-4)
- `internal/agent/drivers/mysql/promote_lock.go:484` "replika %s sudah menjadi master yang bisa ditulis dengan transaksi server ini; server ini dikarantina, kunci tidak dibuka"; `:487` "replika yang terbaca saat kunci dipasang tidak bisa dibaca sekarang (%s); kunci tidak dibuka"; `:482` "…; karantina gagal: %w" (unlockPromote)
- kept event reason (agent, not shown by the panel) `promote_lock.go` "replika yang terbaca saat kunci dipasang tidak bisa dibaca sekarang" (hosts removed)
- `internal/agent/drivers/mysql/promote_files.go:86` "port replika tidak dicatat: sudah ada %d port tercatat"; `:87` "host replika tertua dibuang dari catatan agar berkasnya tetap terbaca"; `:350` "%s tidak ditulis: %d byte, lebih dari %d yang bisa dibaca kembali"; `:260` "%s tidak valid; master tetap dikunci. Periksa berkas itu: sudo dbpanel agent unlock-master menghapusnya dan membuka MySQL tanpa memeriksa kandidat atau replika mana pun, jadi pastikan dulu tidak ada replika yang sudah menjadi master" (SA-11)
- `internal/agent/drivers/mysql/startlock_guard.go:412` "%s bukan berkas biasa" (SA-6)
- `internal/agent/drivers/mysql/peer.go:124` "jawaban server lebih dari %d KiB; berhenti dibaca" (SA-10)
- `internal/agent/drivers/mysql/startlock.go:170` "direktori konfigurasi MySQL %q memuat tanda yang punya arti di berkas opsi MySQL (#, kutip, garis miring terbalik atau karakter kendali); kunci start tidak ditempatkan di sana" (SA-12)
- `internal/agent/cli.go:47-52` usage lines for `forget-replica` (host:port) and `forget-replica-port`; `:405` lock-master race message; `:441` unlock-master "Perhatian: kunci promosi untuk kandidat %s (tenggat %s) dibuka tanpa memeriksa kandidat itu[ atau replika %s]; bila salah satunya sudah menjadi master, kini ada dua master yang bisa ditulis."; `:491` forget-replica "Penjaga kunci promosi tidak lagi menunggu %s terbaca: bila replika itu sudah menjadi master, master ini bisa terbuka dalam beberapa menit dan ada dua master yang bisa ditulis."; `:510`, `:522`, `:525`, `:528`, `:530` forget-replica-port messages.
- README.md: the sections listed under "Tier 3 texts".

## Gate results

1. `make test` on 2fc63a4: exit 2 after 35m58s. Two failures, both fixed in e138d86:
   - `TestLockForPromoteFanOutBounded/count` (new test): the candidate had not applied the replicated `dbpanel_repl`
     account yet (`Access denied`); the test now waits for a synced replica (`sourceWithReplica`). Test-only.
   - `TestE2EPromotePlanned`: the rebuild's **Setujui** now re-reads the old master's connections (SP-1); the e2e keeps
     an application connected and approved without the acknowledgement, so it got 400 ("masih ada koneksi ke master
     lama…"). This is the intended new behaviour; the e2e helper `approveJob` became `approveJobAck`, which ticks
     `accept_connections` in the approve dialog when the rebuild was started with the acknowledgement and fails the test
     if an approval asks for it unexpectedly (existing test expectation changed: the e2e approval of a rebuild now sends
     the acknowledgement).
2. `make test-e2e` on e138d86: exit 0, 8m44s (`ok internal/panel 522.673s`).
3. `go test -timeout 60m -race -short ./internal/panel/... ./internal/agent/...` on e138d86: exit 0, 38m18s, no race
   reported.
4. `make test` on e138d86 (final commit): exit 0, 36m27s (vet, all packages, install_test.sh with shellcheck).

Final state: e138d86, pushed, tree clean.

## Outside the wave

## Residuals of this wave's own fixes

- FR-1: pending start reports live in memory; an agent restart before delivery loses them (a restarted agent reads the
  flags it PERSISTed and reports `ok`). The snapshot route would cover that; not taken (larger, protocol change).
- SA-4: when replicas-seen would pass 1 MiB the oldest hosts are dropped (logged); such a host is then neither read at
  lock time nor accepted as a quarantine source. Only reachable by an attacker registering thousands of source
  addresses; the lock-time fan-out cap (64) refuses the lock long before.
- SA-4 visibility is the agent log plus the refused planned-promotion step in the panel; there is no health field the
  panel alerts on before a promotion is attempted.
- SP-3: dropped events are counted in the same `Dropped` counter as health reports (the server page's "laporan dibuang").
- FR-8 (a): a digest that covers two snoozes also lists alerts opened in the unsnoozed gap between them.
- FR-15 also makes an old-format create job whose state lacks `SnapshotGTID` count as dropping (token on retry): fail
  safe, one extra password.

## Outside the wave (noticed, not changed)

- `leftoverDatabases` (create/rebuild preflight) uses `RefreshInventory` without the FR-6 cut signal; a cut target list
  only shrinks the drop list (safe direction), but the approval then shows fewer names than the target holds.
- `TestWSLogsReplacedConnection` flake (known) not touched.
