### Finding Verdicts

Line numbers are at e138d86 unless they are marked as diff lines.

**[FR-5: add_databases re-reads the replica at the dump step only when AddDrop is non-empty]**: ADDRESSED. `orchestrator/change_databases.go:314` now runs `(len(AddDrop) > 0 || !jr.addFresh) && !reattach`. Any run that did not read the replica itself (a Retry, an Approve or a resume) now calls `dropAddExisting`, which goes to `waitAddExisting` when it finds a non-empty list it was not approved for. `dropsData` then counts the job as dropping because `WaitReason == WaitAddExisting` (runner.go:615), so the job needs a token and the typed names. The empty-list log line is suppressed only when `AddDrop` is empty (change_databases.go diff 853-858), so nothing that drops data was relaxed. Pin: `TestAddRetryAtDumpWaitsForDatabaseCreatedMeanwhile` checks that the job is waiting at the dump step with `existingText("rekap")` and that no drop happened (`wantNoDrop`). The old code would load the dump and succeed, so this test fails without the fix.

**[FR-6: inventory truncation is silent]**: ADDRESSED. `refreshInventory` and `inventoryCallCut` (runner.go:235-266) return `cut`. `addExisting` (change_databases.go:493) and `enforceWipeOnly` (create_replica.go:1088) refuse with `errInventoryCut` (runner.go:241). The public `RefreshInventory` keeps its signature. Pins: `TestAddExistingRefusesTruncatedInventory` (the job fails, no drop) and `TestEnforceWipeOnlyRefusesTruncatedInventory`. Both fail on the old code: the add job succeeds, and the resync fails with an unrelated message.

**[FR-7: remedy text of the start-lock candidate refusal]**: ADDRESSED, within the scope the controller set (the text). `orchestrator/promote_view.go:91` now says that the agent releases the lock by itself once performance_schema shows both flags PERSISTed ON. With performance_schema off, it says to turn it on and restart MySQL, or to choose another candidate. I checked this against the agent. `guardStartLock` releases the lock when `role != quarantined && readOnlyProvedNow` (startlock_guard.go:160). `readOnlyProvedNow` accepts either the agent's own PERSIST in the current run or `persistedReadOnlyOn` (startlock_guard.go:186-224). The check runs every 5 s round, with a 1-minute retry after a failed write, so "dalam semenit" holds. Pin: `TestPromoteViewRefusesCandidateWithStartLock` asserts that the constant holds "performance_schema", "restart MySQL" and "kandidat lain". These words were absent from the old constant.

**[FR-8 (a): a re-snooze before the digest resets snooze_from]**: ADDRESSED. `store/servers.go:234` keeps `snooze_from` whenever it is set (the digest is still owed). Pin: `TestResnoozeBeforeDigestKeepsFrom` expects from = T0. The old code gave the later time. A side effect is listed as Minor 4 below.

**[FR-8 (b): ClearSnoozeFrom is unconditional]**: ADDRESSED. `ClearSnoozeFrom(SnoozeSpan)` (servers.go:296) clears only `WHERE id=? AND snooze_from=? AND snooze_until=?`. Zero rows on an existing server is not an error, and a missing server is still `ErrNotFound`. The caller is notifier.go:520. The time format round trip (`EndedSnoozes` parses, `formatTime` writes back) is exercised by `TestEndedSnoozesAndClear`: after the clear, `EndedSnoozes` returns nothing, so a format mismatch cannot cause the digest to be re-sent forever. Pin: `TestClearSnoozeFromOnlyForTheSpanSent`. Its RED was a compile failure, but the body checks the behaviour: a snooze started between the send and the clear keeps `snooze_from`.

**[FR-10: master_lock_kept gives unlock-master without the README caution]**: ADDRESSED. `monitor/alerts.go:476-477` adds "pastikan dulu replika itu tidak sedang menjadi master di sisi jaringan lain: unlock-master membuka master tanpa memeriksanya". Pin: the exact-text update in `TestLockKeptWithHostsIsCritical`.

**[FR-12: mysql.recordReplicas runs inside repointMu]**: ADDRESSED. `repointReplica` only sets `out.record` (promote.go:1247). The callers record after releasing the mutex:
- `promoteRepoint` records once, in a deferred call (promote.go:810 and the defer before it).
- `RepointNow` records after its closure that holds the mutex.
- `onOnline` records in a defer that is registered before `repointMu.Lock` and its deferred Unlock, so in LIFO order it runs after the unlock (promote.go:1388).

Pin: `TestRecordReplicasOutsideRepointMu` uses `TryLock` inside the fake handler. The changed expectation in `TestRecordReplicasCalledAfterVerify` (2→1 calls) is what the finding asked for.

**[FR-15: create_replica dropsData enumeration]**: ADDRESSED. runner.go:611-612 sets `copyLeft := SnapshotGTID == "" && !AdminReplaced` and ignores the method. Pins:
- `TestCreateReplicaDropsUntilCopied` runs every combination of Method × GTID × AdminReplaced × WipeList × WaitReason × Rebuild against an independent oracle.
- `TestRetryDumpCreateFailedAtCopyNeedsToken` checks `ErrJobNeedsConfirm` without a token and `mysql.preflight` before `mysql.dumpFrom`.

**[FR-16: README precision, ledger Task 14 m1/m2]**: ADDRESSED. See Task 14 below. The README also now states that the lock opens when a writable listed replica lacks this server's transactions (README diff 39 and 106-108).

**[SP-1: Approve of a rebuild never re-reads the old master's connections]**: ADDRESSED.
- `Approve` calls `retryConnectionsClear` before `startMu` (runner.go:784). That function covers the `JobWaiting` status.
- In `confirmDrop`, the `ErrConnectionsRemain` rule moved out of the `if retry` block, so it applies to approvals (runner.go:692). An unreadable list is not clear, and the acknowledgement stored at job start never counts.
- Web: `ack.AcceptConnections` is forwarded on approve (handlers_jobs.go:473). The job page reads the connections and renders the `accept-connections` partial tied to `form="form-setujui"` (handlers_jobs.go:333, job_detail.html diff 2100 and 2111).

Pins: orchestrator `TestApproveRebuildRechecksConnections` (refused, one read, no other command, unreadable list also refused, acknowledged approval runs) and the web test of the same name. The e2e helper change is justified; see Out-of-Scope / tests.

**[SP-2: master_start_late laundered by the rebuild's role change; forms trust an older check]**: ADDRESSED.
- `closeStartAlerts` keeps `master_start_late` open while the server is a replica in a state that is not judged (monitor/promote.go:687). A cancel that puts the role back to quarantined leaves the alert open. A ready or attention replica closes it.
- `rebuildTarget` (handlers_oldmaster.go:762) and `deleteWarnings` (:468) show the late-start warning when `startLateOpen` is true (:700; a store error counts as open). The alert closes only through a leftover check stored after it, so an open alert does mean "no check since".

Pins: `TestStartLateSurvivesRebuildUntilCopied` (creating → cancel → ready) and `TestLateStartDistrustsOlderLeftoverCheck` (rebuild form, delete list and data-confirm text). Side effects are listed as Minor 1 and Minor 3.

**[SP-3: agent events other than health are not rate-limited]**: ADDRESSED.
- `Hub.OnEvent` passes the four lock and start events through `allowEvent` (hub.go:257-330): a burst of 10, one token per 5 s, per server. Drops go to the existing `Dropped` counter and drop log.
- The "first of each kind in 10 min passes" bypass is bounded. Kinds are made only from known words: 4 types × 4 results × 2 bypassed values × 3 actions, plus 4 unreadable kinds, is at most about 100 kinds. The worst case is therefore about 100 + 120 events per 10 min per server, against an unbounded flood before. Job events and samples are untouched.

Pin: `TestHubLimitsAgentEvents` (a flood of 2,000 lets through at most burst+2; the first late or failed event after the flood passes; a repeated late is dropped; job.log is not limited; the refill works). The store-level row-count test the finding also suggested was not added. The hub test is enough to pin the bound.

**[SP-4: a job made before a promotion acts on the old master after it]**: ADDRESSED. `supersededBy` refuses seeds/changesDatabases jobs when `topo.MasterServerID != st.MasterServerID` (runner.go:967). I checked that every one of these kinds stores `MasterServerID` at creation (change_databases.go:114 and 147, resync.go:85, create_replica.go:180), so live jobs are not falsely superseded. A rebuild stores the then-current master, which is the topology's master. Pin: `TestRetryRefusedAfterMasterSwapped` (Retry refused, `Superseded` names the master, no command). The old code accepted that Retry.

**[SP-5: the WaitWipe list is shown only through the step message cut at 4 KiB]**: ADDRESSED.
- `WaitList` is set with every `WaitWipe` and reset on every new wait (create_replica.go:460-467).
- The approve dialog lists every name (`p.WipeNames`, handlers_jobs.go:310; job_detail.html diff 2085-2090).
- An older wait stored without `WaitList` whose message ends in `(dipotong)` is refused (`refuseCutWipeList`, runner.go:797; the marker is runner.go:1682).

The hash binding is unchanged, so what is dropped is still exactly the hashed list. Pins: `TestWipeWaitKeepsFullList` (300 names, the message is cut, `WaitList` is whole, the old-format cut wait is refused with no command) and `TestWipeApprovalListsEveryName`.

**[SP-7: tokenless paths to a copy (same defect as FR-15)]**: ADDRESSED.
- Path 1 (Approve of a plain create at `WaitForeignSource`) now needs a token (`copyLeft`). Pinned by the table rows and by `TestForeignSourceNamedBeforeReset`, which now passes `Confirmed`.
- Path 2 (Retry of a dump create at the copy) needs a token and runs the preflight first. Pinned by `TestRetryDumpCreateFailedAtCopyNeedsToken`.
- Path 3 is FR-5.

**[SP-9: an oversized or unreadable lock/start event opens nothing]**: ADDRESSED.
- An unreadable or oversized `monitor.lockExpired` opens the generic kept `master_lock_expired` notice (`keptAnyway`, monitor/promote.go:346).
- An unreadable or oversized start event opens `master_start_late` or `master_start_blocked` through `raiseIfNone` (:591, :654). This closes nothing and never rewrites an open alert.
- An unknown result is still ignored.

Pins: `TestLockKeptOversizeStillAlerts` (5 KiB and "{") and `TestUnreadableStartEventsStillAlert` (opens; an open late alert keeps its message). The changed assertion in `TestMasterLockExpiredFromEvent` is the finding's own fix and is justified. The agent half (no hosts in `Reason`) belongs to the other seat.

**[SP-11: agent strings outside the agentText bounds]**: PARTLY. Items 1-3 are done:
- Item 1: `le.Candidate` and `qe.NewSource` go through `agentText(…, 120)` (monitor/promote.go `lockExpiredMessage`, `quarantinedMessage`).
- Item 2: `Unreadable` and `Incomplete` are bounded in `connectionsOf` (handlers_oldmaster.go:172, :176). The rebuild's `ConfirmText` and Warnings are built from these, so they are bounded too.
- Item 3: `StartLock.Reason` and `OperatorInitFiles` are bounded (at most 10 plus "… dan N lagi") (handlers_servers.go:669-677).

Pins cover item 1 (`TestStartAndKeptMessagesBoundAgentText`) and item 2 (`TestConnectionsBoundAgentText`, the fragment only). **Item 3 has no pinning test.** The code is trivial, but nothing would catch a regression.

Item 4 is open: inventory database names are not validated or flagged at `inventoryCall`. It is not a data-loss exposure, provided the agent's `dbNameRe` check on every name it acts on holds (seam below). A name with a bidi override or another odd character could make the drop list now shown whole in the approve dialog (SP-5) read wrongly. It cannot make the panel drop a different database: the approval is bound to the hashed list, and the agent refuses a name outside its pattern. So the exposure left is display and log confusion only, the same rating the security report gave it.

**[Task 13 m3: a database that equals a FOLLOWED database ignoring case never enters the drop list]**: ADDRESSED. `addExisting` returns an error when a name about to be dropped `EqualFold`s an entry of `st.Databases` (change_databases.go:500). `st.Databases` is what the replica followed when the job started, so the new names are not in it. Every drop list of the add job comes from `addExisting`, in both `addExisting` and `dropAddExisting`. Pin: `TestAddExistingRefusesFollowedNameIgnoringCase` (job failed, error names it, no drop). The old code waited with the list instead of failing.

**[Task 11 wording: the wipe-only refusal advice must fit the job kind]**: ADDRESSED. create_replica.go:1096-1101: a resync keeps "mulai Resync ulang lagi dari halaman replika", while a create or rebuild gets "ulangi pekerjaan ini dari halamannya dan akui database itu di dialog Ulangi". That dialog does offer `wipe_only[]` for create jobs with a WipeList, which drop data and so get the retry dialog. Pin: `TestWipeOnlyRefusalAdviceFitsJobKind` checks the exact suffix for a create job.

**[Task 14 m1/m2: README timing of --print-defaults and unlock after forget-replica]**: ADDRESSED. README diff 146-148: `--print-defaults` runs "semenit sekali (serta segera setelah menulisnya)". Diff 103-108: after `forget-replica`, the next recheck comes within a minute, a second read 60 s later, and the lock opens after the second read.

**[SA-3 README note: the replication password is offered to every recorded host × port; retire replicas with forget-replica]**: ADDRESSED. README diff 114-121, in bold: "Saat replika dipensiunkan, jalankan forget-replica…". It explains that every recorded host is read at every recorded port with the replication account, that the password goes to whoever answers when "Perketat" has not been run, and that "Perketat" closes this.

### New Breakage in the Fix Diff

None that is Critical or Important. None of the following happens: a dropping step running without a token, a name acknowledgement or a fresh re-read; a drop of an unshown database; a critical alert closed early; an unbounded flood path; a broken Retry or Approve of an old job; a weakened test. Minor items:

1. **Minor: the SP-2 keep-open rule is not limited to a rebuilt old master** (`monitor/promote.go:687`). Any replica in `creating`, `failed` or another unjudged state keeps an open `master_start_late` until it becomes ready or attention. Scenario: a faulty or compromised agent of an ordinary replica whose create failed sends `monitor.quarantineStart` `late`, or (new with SP-9) any unreadable start payload. A Critical, never-silenced alert then stays open on a replica page. It says to run "Periksa data tertinggal", which does not exist for a replica. Before the fix, the next tick closed it. This is reachable only by an agent that sends start events from a non-quarantined server, which is the SP-10 class the controller excluded. One cheap tightening: apply the exception only when the alert was opened while the server was quarantined, or when `GetTopologyByOldMaster`/the promotion names the server.

2. **Minor: the rebuild's connection acknowledgement is asked after the copy is made** (`web/handlers_jobs.go:333`, `orchestrator/runner.go:692`). Every approval of a rebuild re-reads the connections, including `WaitReconnect` after a CLONE (`AdminReplaced`), where the old data is already replaced. The checkbox text "akan kehilangan semua yang ditulisnya ke sini" is then inaccurate, and an operator with clients on the freshly cloned server must tick it to continue. It fails closed and is a UX cost only. The report notes this as a design choice.

3. **Minor: a known transaction count is hidden behind the late-start warning** (`web/handlers_oldmaster.go:762` and `:468`). The `startLateOpen` case comes before `Leftover.Transactions > 0` in both switches. When an older check found N transactions and a late start followed, the form and the delete dialog show only "tulisan itu belum diperiksa" and drop the "N transaksi" sentence. The warning still blocks trust in the old result, so this is a loss of information, not a safety gap.

4. **Minor: FR-8 (a) without Telegram configured** (`store/servers.go:234`). `Pass` returns early when Telegram is not configured (notifier.go:322-324), so `snooze_from` is never cleared. With the new CASE, every later snooze keeps the first snooze's start forever. The first digest after Telegram is configured then covers everything since the first snooze. This over-reports in the direction the report accepts. `snooze_from` has no other reader.

5. **Minor (test gap): SP-11 item 3 has no pinning test** (`handlers_servers.go:669-677`).

Test changes checked against item (f): every changed expectation in the report's list is justified by its finding and none is looser.
- `TestDropsData` rows: FR-15.
- `JobAck{Confirmed:true}` in 7 tests: FR-15, with no assertion removed.
- recordReplicas 2→1: FR-12.
- `TestSetSnoozeKeepsFromWhenExtended` now clears first: FR-8 (a).
- `TestMasterLockExpiredFromEvent`: SP-9.
- Kept-lock exact text: FR-10.
- `TestFailsWhenAgentDropsDuringSnapshot` now also expects `mysql.preflight`: stricter.

`approveJobAck(j, rebuild && accept)` ticks `accept_connections` only when the rebuild was started with the acknowledgement. It fails the test when an approval asks for it unexpectedly, so it does not mask a regression, and it makes `TestE2EPromotePlanned` exercise SP-1 end to end.

### Out-of-Scope Observations

- **Seam, SP-11 item 4 / SP-5:** the "display-only" verdict depends on the agent validating every database name it drops or dumps (`dbNameRe` in `mysql.dropDatabases`, `mysql.preflight` leftovers and `mysql.dumpFrom`). The other seat should confirm that `dropDatabases` refuses a name outside the pattern rather than quoting it.
- **Seam, SP-3 × FR-1:** the hub drops limited events silently. The agent's new start-report FIFO (16 entries) sends "until delivered", so the other seat should confirm that "delivered" means the WebSocket write succeeded. If it does, a report the hub drops is lost and not retried. That is fine under the burst-10 and first-of-kind rules, but it means an agent reconnecting with more than 10 queued reports of the same kind loses some. The kinds come from `QuarantineStartEvent.Result`/`Bypassed` and `LockExpiredEvent.Action`.
- **Seam, SP-9:** the panel side assumes the agent's kept `monitor.lockExpired` no longer repeats `Hosts` in `Reason` (agent `promote_lock.go`, kept event). The other seat should confirm this.
- **Seam, SA-3 README:** the README says that after "Perketat" the replication password "tidak dikirim" without a matching certificate. The other seat should confirm that the lock-time and guard reads (`ReadPeer` / `readListed`) use the topology's verify-identity TLS mode before authenticating.
- `leftoverDatabases` (create/rebuild preflight) still uses `RefreshInventory` without the cut flag, as the implementer noted. A cut list only shrinks the drop list (safe direction), but the approval can then show fewer names than the target holds.
- In SP-9, `notice` replaces an open, more specific `master_lock_expired` (for example one naming the candidate) with the generic kept text when an unreadable event arrives. This loses information only.

### Verdict

**Fix wave, panel side:** All findings addressed except one partly addressed finding, with no new Critical or Important breakage. Open: SP-11 item 4 (inventory names not validated or flagged). It is display-only provided the agent's `dbNameRe` seam holds. SP-11 item 3 is fixed but has no test.
