### Finding Verdicts

Reviewed in one pass over `fixwave-agent-code.diff` and `fixwave-agent-tests.diff` (765427b..e138d86), with targeted reads at HEAD of `quarantine.go:251-330`, `startlock_guard.go:425-476`, `startlock.go:480-510,659-700`, `health.go:76-105`, `monitor/monitor.go:473-518`, `runtime.go:94-105`, `session.go:18,35-45`, `cli.go:360-392`, `promote.go:653-670`, `proto/mysql.go:590-612`. No tests were run: nothing I read raised a doubt that the reported runs do not already settle.

**[FR-1 — late/bypassed start report lost when the panel is not connected]** — ADDRESSED. Start reports now go through `emitStart` (`startlock_guard.go` diff +1780-1801), which queues them in `pendingStart` (at most 16; `driver.go` +304). `flushStartEvents` sends the queue oldest first at the start of every `GuardTick` (`promote_lock.go:557`) and stops at the first refusal. Both carriers named in the finding are covered: `reportQuarantineStart` (`startlock_guard.go:476`) and `watchBootFailure` (`:334`). `bootFailAt` advancing before the send no longer loses the report, because the report itself is now queued. `ok` results take the same path, so the "ok closes bypassed/blocked" case is covered too. Every caller runs under `promoteMu` (all are reached from `GuardTick` only). Pinning test: `TestQuarantineStartReportRetriedUntilDelivered` (late, start failed). It ticks while offline, then online, and asserts exactly one delivery and no duplicate on later rounds. The old code fails it (RED shown). Residual, as the report states: the queue lives in memory, so an agent restart before delivery loses it. The finding offered this route as acceptable.

**[FR-2 — start lock change not a "change"; false "belum diperiksa" at agent start]** — ADDRESSED. `changed()` gained `startLockChanged`, which compares nil-ness, `Present`, `Locked` and `Reason` (`monitor/changes.go` +1915, +1921-1931). When the guard has not checked the lock yet on a quarantined server, `Snapshot` now computes `startLockStatus` itself (`health.go:370-381`). The result is not stored. "belum diperiksa" remains only when there is no Host. `startLockStatus` takes no lock and runs `readEffectiveOptions` on its own timeout (`startlock.go:486`). `mysqlHealth` bounds only a child context (`health.go:93`), so a slow MySQL does not starve the check. Pinning tests: `TestChangedOnStartLock` (8-row table) and `TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock`. Both would fail on the old code.

**[FR-3 — kept-lock hosts withheld up to an hour by another keep reason]** — ADDRESSED. `keepLock` (`promote_lock.go:709`, diff +1389-1405) now throttles only when the host set equals the last reported set (`keptHosts`, sorted) and the report is less than `keptEvery` old. `keptAt`/`keptHosts` are recorded only after `emit` succeeds. Every caller sets `nextRead` to `lockRecheck` (`promote_lock.go:622-672`), so a failed send is retried once a minute, not every 5 s. Pinning test: `TestKeptHostsReportedAtOnceAfterAnotherKeepReason`. It goes candidate unreachable → kept with no hosts, then listed replica down → second event with Hosts. Its subtest covers an unchanged set staying silent and a grown set reporting at once.

**[FR-4 — failed start on a missing lock file never reported]** — ADDRESSED. When `keepStartLock` has to rewrite the file, it records `bootSince` = the earliest managed-config mtime before the rewrite (`startlock_guard.go` diff +1697-1701). `watchBootFailure` then scans the log from `min(file mtime, bootSince)` (+1754-1758). `bootSince` is cleared once MySQL answers (+1671). Pinning test: `TestBootstrapFailureReportedWhenGuardRecreatesFile`. It sets the config mtime to 03:00, writes a log line at 03:18, removes the file and runs one tick, then asserts the file is back and one `quarantineStartFailed` was sent. The old code scans from the new mtime and finds nothing. (See New Breakage 3 for a side effect of the wider window.)

**[FR-9 (a) — forget-replica rejects host:port]** — ADDRESSED. `net.SplitHostPort` strips the port (`cli.go` diff +182-184). A bare IPv6 address fails the split and is kept as is. Pinned in `TestForgetReplicaCLI` by forgetting `10.0.0.4:3306` and asserting the host is no longer seen.

**[FR-11 — guard fault warnings not rate-limited]** — ADDRESSED. `warnLimited` (`promote_lock.go` diff +1453-1473) logs a message at most once per minute. It is applied to all three warnings the finding named: the selfQuarantine branch (+1349), `ensureLocked` (+1326) and the SHOW REPLICAS ports warning (`promote_files.go` +948, `portsErr` removed). The pinning test, `TestGuardFaultWarningsLimited`, covers only the `ensureLocked` path. The other two changes are unpinned (Minor test gap, see New Breakage 5).

**[FR-13 (a) — greeting probe every 5 s on a quarantined server]** — ADDRESSED. `guardStartLock` uses `d.qUp` (`startlock_guard.go:128-133`). `guardQuarantined` sets `qUp` on every path from its own authenticated `h.Open` (`quarantine.go:252-263`), and it now runs immediately before. Pinning test: `TestQuarantineGuardMakesNoAbortedHandshake` (`Aborted_connects` unchanged over three rounds).

**[FR-13 (b) — start-lock housekeeping before read-only enforcement]** — ADDRESSED. On a quarantined server the order is now `guardQuarantined`, then `guardStartLock`, whatever the first returns (`promote_lock.go:561-564`). Moving `reportQuarantineStart` before `keepStartLock` does not change its bypass verdict: with the file missing, `os.Stat` fails in both orders. Pinning test: `TestGuardEnforcesReadOnlyBeforeStartLockHousekeeping` (`super_read_only` already ON at the first config write). Caveat: the new `flushStartEvents` was placed ahead of both (New Breakage 1).

**[FR-14 — lock-master check and write not one critical section]** — ADDRESSED. `Pins.WriteLockIfNone` reads and writes under the lock flock (`promote_files.go` diff +592-609). `lockForPromote` writes through `Pins.WriteLock`, which takes the same flock. The CLI keeps the lock it finds, exits 1 and names it (`cli.go` +119-128). Pinning test: `TestLockMasterKeepsLockWrittenMeanwhile`. The racing write is injected through `cliMySQLHost`, which `cliExec` calls after the pre-check (`cli.go:383-392`, `:350`), so the test really exercises the window. RED is shown.

**[Rec. 4 / declined-to-judge — mysql.unlockPromote never reads PromoteLock.Readable]** — ADDRESSED. After the candidate check, and before `unlockMaster`, `unlockPromote` reads `serverUUID` and `readListed(ctx, c, l.Readable, uuid)` (`promote_lock.go` diff +1220-1240). A writable listed replica holding this server's transactions makes the server run `selfQuarantine` and the command is refused. Any unreadable entry refuses and keeps the lock. A failed UUID read refuses. Ctx expiry leaves entries unread, which counts as unreadable, so it refuses. Nothing opens the master without the reads. A lock written by an older agent has no `Readable`, so nothing is read; that is the same semantics as the guard. Pinning test: `TestUnlockPromoteReadsListedReplicas` (unreadable → refused and still locked; promoted → quarantined, both flags ON). RED is shown.

**[SA-4 — replica-supplied, uncapped port set; 10-minute fan-out, late lock, unreadable lock, recording stops]** — ADDRESSED.
- Ports are capped at `maxSeenPorts`=8, with a `penuh` mark, and the lock is refused while the mark stands (`promote_files.go` +555-558, +751-789; `promote_lock.go` +1023-1031).
- The fan-out is capped at `maxLockReads`=64 (+1032-1039) and runs under `lockReadBudget`=60 s, refused when exceeded (+1040-1053). The refusal checks ctx first, then the budget, so a truncated list is never written.
- `writeJSON` refuses records larger than `maxRecordSize` (+652-655).
- `writeSeen` trims the oldest hosts to stay under `maxSeenSize` (+686-723).
- `forget-replica-port` is added (`cli.go` +212-244).

Each of the four harm paths is closed, apart from the availability point in New Breakage 2. The behavioural pins are real even though the RED was a build failure:
- `TestLockForPromoteFanOutBounded/count`: on the old code it would read 65 dead hosts and then lock, which the test asserts against.
- `/budget`: the stub blocks until ctx ends, so the old code would hang on the 10-minute request ctx rather than pass.
- `/ports full`: the old code would lock.
- `TestLockTooLargeNotWritten`: the old code writes the file.
- `TestSeenPortsCapped` and `TestSeenFileNeverOverReadLimit`: the old code would record 1,000 ports and write past 1 MiB.

Not done from the suggestion: a health field announcing that the ports file is full (the report says so).

**[SA-5 — one-shot start events lost (same defect as FR-1)]** — ADDRESSED; see FR-1. The `bootFailAt`-before-send path is also covered by the "start failed" subtest.

**[SA-6 — error log opened with plain os.Open]** — ADDRESSED. `readTail` opens with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, `fstat`s the descriptor and reads only regular files (`startlock_guard.go` diff +1833-1846). An unreadable log is warned about at most once per `startLockRetry` (+1747-1750). A FIFO no longer blocks the guard. A symlink to a device is refused (ELOOP). A device node planted directly needs CAP_MKNOD, and a hard link to `/dev` crosses filesystems. Pinning test: `TestReadTailOnlyRegularFiles` covers the FIFO with a 5 s watchdog (RED: blocked) and the symlink.

**[SA-7 — start lock file: Chmod/Chown follow symlinks; directory owner unchecked]** — PARTLY.
- Done and correct: an existing file is opened with `O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, and owner and mode are fixed with `f.Chown`/`f.Chmod` on that descriptor (`startlock.go` diff +1565-1611). A symlink at the path makes the open fail with ELOOP and falls through to `writeFileAtomic` (rename replaces the link) plus `Lchown`. `TestStartLockFileFixNeverFollowsLink` swaps in a link after the open and asserts the victim keeps 0600. It pins the behaviour: on the old path-based `Chmod` the victim would get 0644.
- Open: the check that the directory and its parents are root-owned and not group/other-writable.
- The stated reason holds only as a test-harness cost, not as a design barrier. `testmysql` keeps `my.cnf` in a directory owned by the mysqld user, but the check could take an injectable expected owner, as other hooks in this package do. So the work is not small, but nothing makes it unreasonable.
- What is left is a real but low exposure. It is reachable only on non-stock layouts where the option-file directory is writable by a non-root user (the `mysql` OS user), which spec §7 already treats as outside the lock's defence. There:
  - That user can delete or replace the lock file between guard rounds while the agent reports `StartLocked=true`.
  - The `f.Chown(0,0)` in `fixStartLockFile`, which still runs on a regular file whose content differs, can chown a hard-linked inode to root. `protected_hardlinks` (default on Debian/Ubuntu/RHEL) limits this to files the attacker already owns or can write, and the old code had the same `Chown(path)`.
- Cheapest remaining value: report `Locked=false` with a reason when the directory fails the owner/mode check, so the panel's "terkunci" is truthful on such layouts.

**[SA-10 — nothing bounds what a peer sends back]** — ADDRESSED. `ReadPeer` dials through `cfg.DialFunc`, which wraps the connection in `limitedConn` (256 KiB over the connection's life, TLS included). `PeerState.Err` is cut to 300 bytes on a rune boundary (`peer.go` diff +408-482). The kept event's Reason no longer repeats the host list (+1356-1358). The test, `TestReadPeerBoundsAnswer`, pins only the error-text bound: `cutText` alone would make it pass without `limitedConn` (Minor test gap, see New Breakage 5).

**[SA-11 — local unlock paths do not say what they bypass]** — ADDRESSED.
- `unlock-master` of a lock with a candidate prints the candidate, the deadline, the listed replicas and the two-master warning before acting (`cli.go` +150-159).
- `forget-replica` prints that the guard no longer waits for that host (+201-203).
- The unparseable-lock message no longer offers `unlock-master` as the repair (`promote_files.go` +628-630).
- Pinned by `TestUnlockMasterSaysWhatItBypasses` and `TestForgetReplicaCLI` (extended).
- The suggested `--yes` gate was not added. The defect ("say nothing") is gone; whether a confirmation is still wanted is the owner's call.

**[SA-12 — lock path written into an option file unescaped]** — ADDRESSED. `StartLockPath` refuses a directory containing `#`, `'`, `"`, `\` or control characters (`startlock.go` diff +1518-1524). Pinned in `TestStartLockPathRule` (five rows).

**[pins.go header comment — must match the code]** — ADDRESSED. `pins.go:507-524` (diff +513-524) now names the agent's own guarded writers (quarantine, promotion, rebuild settling, release) and root's CLI. It no longer claims that a compromised panel "can never … drop or replace its data". It names the SA-2 path (a quarantine under a host the panel attached, then a drop or replace from it) and the SA-1 path (releaseRole, then configure/setReadOnly). This matches what the code does.

### New Breakage in the Fix Diff

1. **Minor — `flushStartEvents` runs before read-only enforcement on a quarantined server** (`promote_lock.go:557`, ahead of `guardQuarantined` at `:562`).
   - The cause: `Agent.Emit` → `session.send` blocks for up to `writeTimeout` (10 s, `session.go:18,40`) when a session exists but its socket does not drain. That block happens under `promoteMu`, before the round re-PERSISTs read-only.
   - Scenario: a quarantined master whose MySQL just came up writable (pending late report), and a wedged panel session. Re-locking is delayed up to about 10 s in that round. coder/websocket closes the connection on a write timeout, so it is normally one round per broken session.
   - It runs against FR-13(b)'s principle ("read-only first"). The fix is to move the flush after the role-specific work in `GuardTick`.

2. **Minor — SA-4's fail-closed full mark lowers the bar for blocking planned promotions** (`promote_files.go` +751-789, `promote_lock.go` +1023-1031, `cli.go` +226-241).
   - Any holder of REPLICATION SLAVE (R-038's shared `dbpanel_repl`) can now block every planned promotion with 9 replica registrations carrying distinct `report_port` values. Before, it took about 2,000 packet-dropping ports. The block lasts until root runs `forget-replica-port semua`, and the attacker can refill the mark within one guard round. There is no health field, so the operator learns at promotion time.
   - It is availability only and fails closed, the R-098 class.
   - Separately, `semua` forgets the ports of replicas not connected at that moment. A replica that is reachable but has its replication stopped, at a port other than the candidate's, is then not read at the next lock and not listed in `Readable`. If that replica is later emergency-promoted, the guard opens past it. This needs a multi-port topology plus that timing.
   - Suggested: the `semua` output should say that the ports of replicas not connected now are lost, and should name the file to check first.

3. **Minor — FR-4's wider scan window can repeat an old failure** (`startlock_guard.go` diff +1697-1701, +1754-1758). The scan now starts at the managed config's mtime, which can be days old, instead of the lock file's. After an agent restart (`bootFailAt` is memory-only), a bootstrap failure line still in the 64 KiB tail and newer than the config mtime is reported again with its old `At`. That includes one already reported by the previous agent run. This can only happen while MySQL is down and the file had to be rewritten, so the result is a stale `master_start_blocked`, not a wrong safety state.

4. **Minor — SA-6 stops reading a legitimately symlinked error log** (`startlock_guard.go` +1835). On a layout whose `log_error` path is a symlink, a failed start is never reported again (FR-4 included). The only trace is a once-a-minute local warning. This is the trade-off SA-6 asked for; README could name it.

5. **Minor — test gaps in new pins.**
   - FR-11: `TestGuardFaultWarningsLimited` pins only the `ensureLocked` warning. The selfQuarantine branch and SHOW REPLICAS changes are unpinned.
   - SA-10: `TestReadPeerBoundsAnswer` passes without `limitedConn`, since `cutText` alone bounds `Err`. The 256 KiB read bound is not pinned.
   - Existing agent tests: none was loosened. The only removed lines are in `TestForgetReplicaCLI` (assertion strengthened) and the `eventLog` helper (gained an offline switch, default off).

No Critical or Important breakage found. I checked specifically:
- (a) No new path leaves a quarantined or locked master writable. `unlockPromote`'s new branch only refuses or quarantines.
- (b) Lock-time reads are either complete or refused (ctx checked, then budget).
- (c) No start report is dropped before a successful Emit, except past 16 queued, where oldest `ok` goes first.
- (d) Nothing new can hang the guard: FIFO fixed; blocking bounded by item 1.
- (e) No proto change. The `penuh` line is skipped by older parsers.
- (f) See item 5.

### Out-of-Scope Observations

- **Seam, panel `monitor.quarantineStart` handler:** reports can now arrive minutes to hours after the start, repeated only on failed sends. `QuarantineStartEvent` carries no time (`proto/mysql.go:590-594`), so the panel stamps receipt time. Check on the panel seat that:
  - opening `master_start_late` / bypass is idempotent for a repeat of the same result;
  - SP-2's "closed by a leftover check after it" compares against an alert time that cannot predate a check that really preceded the start.
- **Seam, panel planned-promote rollback (`mysql.unlockPromote` caller):**
  - The agent now refuses it with new `ParamError` texts ("replika … tidak bisa dibaca sekarang …; kunci tidak dibuka", "… server ini dikarantina, kunci tidak dibuka").
  - It can emit `monitor.quarantined` and pin the server quarantined from this path.
  - The panel must not assume the master was opened, and must handle the quarantine as it does from the guard.
- **Seam, `monitor.lockExpired` kept event:** `LockExpiredEvent.Reason` no longer contains the host list (`promote_lock.go:672`); `Hosts` does. Confirm no panel code (old or new) reads hosts out of `Reason`.
- **Seam, `mysql.lockForPromote` refusals:** three new `ParamError` refusals (ports full, too many reads, 60 s budget). Each writes no lock and leaves the master writable. The panel's "lock may still stand" handling should not hold the master as locked for these.
- **Emit semantics (`runtime.go:94-105`, `session.go:35-45`, not in the diff):** "delivered" in FR-1 means written to the WebSocket, not acknowledged by the panel. A session that dies right after a successful write still loses the report. Together with the memory-only queue, the snapshot route (Recommendation 3) remains the sturdier design.
- **`writeJSON`'s new size cap also applies to `WritePromoted`** (`promote.go:663`). A `gtid_executed` over ~64 KiB (on the order of 1,400 server UUIDs) now fails `mysql.promote` after the master pin, with `promoting` left behind. Before, it wrote a file that could not be read back. This is practically unreachable.
- SA-7's remaining `writeFileAtomic` temp-file-then-rename in a non-root-writable directory (`realhost.go:186-221`) is untouched. The directory check that SA-7 lacks would cover it too.

### Verdict

**Fix wave, agent side:** Findings remain open: SA-7 (PARTLY: the directory/parent owner and mode check is not done; the descriptor-based fix is correct). All other listed findings are addressed, and there is no new Critical or Important breakage. There are five Minor items, of which the `flushStartEvents` ordering (`promote_lock.go:557`) and the SA-4 full-mark blocking bar / `semua` port loss are worth a follow-up.
