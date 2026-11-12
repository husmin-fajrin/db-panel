# Review: follow-up R-117 / R-119 (bb54853 code, 000f5b5 docs)

Reviewed: `639f619..000f5b5` from the review package, plus targeted reads at HEAD (= 000f5b5) of
`promote_lock.go:540-779`, `quarantine.go:150-329`, `startlock_guard.go:110-405`,
`startlock.go:725-750`, `agent/monitor/monitor.go:315-440`, README.md:712-724, and the two docs at 000f5b5.
No tests were run. Reading the code left no doubt that a test run would have settled.

### Spec compliance — ❌ (code ✅; docs: one untrue statement)

Code against the brief:
- Fix 1. `defer d.sendRoundEvents()` (`promote_lock.go:562`) is registered after `defer d.promoteMu.Unlock()` (`:557`). Defers run LIFO, so every return after `TryLock` sends while `promoteMu` is still held. That covers `:569`, `:574`, `:578`, `:581`, `:588`, `:592`, `:595`, `:597`, and also a panic. A skipped round (`:551-556`) sends nothing. On a master path, `db.Close` (`:590`) runs before the send. On every path, read-only (`guardQuarantined`), `guardStartLock` and the master's `ensureLocked`/`guardExpired` all run before the deferred send. ✅
- Every round that got `promoteMu` still flushes start reports. `emitStart` and `reportQuarantineStart` are only called from inside `GuardTick`: `quarantine.go:317` → `startlock_guard.go:488`, and `guardStartLock` → `watchBootFailure` → `:334`. Making `emitStart` only queue therefore cannot strand a report outside a round, and a report queued in a round goes out at the end of that round. The queue size rule, the drop rule, the event shapes and `flushStartEvents` (`startlock_guard.go:387-405`) are unchanged. ✅
- TDD: the order test and the every-path test exist and use `eventLog` and the real-mysqld rig. Neither sleeps. ✅
- Fix 2 checks:
  - `semua` prints the file path, the sorted, capped port list with "dan N port lain", whether the full mark went, the old confirmation line, and a caution that covers all three clauses of the brief (`cli.go:549-570`).
  - An empty record gives a clear stderr line with the path and exit code 1, as before (`:546`).
  - The single-port message gained one clause (`:529-531`). The switch became an `if`, which is a minimal change and not a restructure.
  - The usage clause is at `:53-55`.
  - The file's effect is unchanged (see promote_files below).
  - README.md:715-722 still matches, so leaving it unchanged is correct. ✅
- Docs:
  - Ringkasan recount: I parsed every `| R-` row under the three "Kelas" sections at 000f5b5 and counted the priority column. Result: A 18 (6/8/4), B 16 (6/7/3), C 83 (14/22/47), total 117 (26/37/54). This equals the table at inventaris:65-68.
  - R-117 is gone from the open lists (inventaris:103, :378).
  - R-119 stays open and is reworded (inventaris:242), and its 5e entry stays.
  - Audit document: the header (:4), the §1 list (:49), the §4.0 SEC-89 row (:131), SEC-88 (:240), SEC-89 (:246, :252), SEC-116 (:510), the §5 R-030 row (:675) and §7 (:719, :734) are all updated. Nothing else was rewritten.
  - The executive-summary counts correctly stay as they were: no SEC status changed.
- **Gap:** the new R-117 text says that `GuardTick` sends "semua yang ditujukan ke panel" last. That is not true at bb54853 (see M1). The statements are at inventaris:324 ("mengirim semua yang ditujukan ke panel … paling akhir, lewat `defer`"), audit:252 ("mengirim semua yang ditujukan ke panel paling akhir lewat `defer`") and audit:510 ("memindahkan semua pengiriman ke panel ke akhir putaran"). Separately, the R-117 risk as worded can still happen; see I1.

### Code quality — Needs fixes (docs and comment wording only; the code change itself is sound)

### Findings

**Critical:** none.

**Important**

- **I1. R-117 is marked handled, but the scenario it describes still happens one round later.** Locations: inventaris:324; audit:252, :675, :719. Mechanism: the guard loop sleeps `GuardEvery` *after* each round finishes (`agent/monitor/monitor.go:315-327`, "never a ticker"). The send still holds the guard for up to the 10 s write timeout, now at the end of round N instead of the start of round N+1. So the time between two read-only enforcements is still 5 s + 10 s in both versions of the code.
  - Failure scenario: a panel session that does not drain, with a start report pending. Round N enforces read-only, then blocks 10 s in `sendRoundEvents`. Between those two points something makes the old master writable again (an operator `SET GLOBAL`, or a restart with the start lock bypassed). The re-lock comes at round N+1, about 15 s later.
  - That is exactly R-117's wording: "penguncian ulang tertunda sampai 10 detik, sekali per sesi rusak".
  - What the fix does improve: the round that meets a broken session now enforces read-only first. A change made during the sleep before that round is fixed at once instead of after 10 s.
  - Fix (docs; the brief prescribed this code change and it is right for its stated scope):
    - In the R-117 "Sudah ditangani" row and the audit's SEC-89 note, say the order is fixed *within a round*.
    - Record the residual: a blocked send still extends the pause before the next round by up to the write timeout, once per broken session.
    - Either keep that as an open line (Rendah/Tinggi, by the inventory's impact rule) or rewrite R-117 to say so.
    - A real code close would take the send off the guard's critical path: a shorter per-send deadline for guard sends, or a send queue drained outside `GuardTick`. Leave that to Rencana 5b.

**Minor**

- **M1. "All panel sends last" is false; one inline send still precedes safety work.** Locations: `promote_lock.go:538-539` (doc comment), inventaris:324, audit:252, :510.
  - `keepLock` sends `EventLockExpired` inline (`promote_lock.go:722`), and so does the unlock path (`:693`). These two are harmless: each is the last action of its round.
  - `selfQuarantine` sends `EventQuarantined` inline (`:765`) *before* `d.lockStart` writes the start lock (`:766`). This runs inside `GuardTick` via `guardExpired` (`:648`, `:671`).
  - Scenario: a master's lock guard self-quarantines while the panel socket does not drain. The new quarantine's start lock is written up to 10 s late. Read-only is already PERSISTed (`:751`), so the danger is small. The code predates this change and the brief did not ask for it.
  - Fix: reword the comment and the docs to "laporan start dan kejadian karantina dari `guardQuarantined`". Optionally swap `:765` and `:766` so the start lock is written before the send.
- **M2. When only the full mark was present, the caution points at a list that has no ports.** Location: `cli.go:551` together with `:567-570`. The output prints "port yang dihapus: tidak ada" and then "salah satu port di atas". Fix: print the caution only when `len(f.Ports) > 0`, or word it without "di atas" in that case.
- **M3. Ports beyond the cap are emptied without being named.** Location: `cli.go:557-561`, `promote_files.go:632-643`. The agent itself writes at most `maxSeenPorts`, so More > 0 only happens with a hand-written file or an older agent's file. In that case the operator is told "dan N port lain", but the file is already empty, so they cannot learn which ports went: the warning is unactionable for exactly those ports. The brief allowed the cap. Suggested fix: list every port (the file is already bounded by `maxRecordSize`), or keep the cap and print "lihat salinan …" after copying the old file aside. Owner's call.

### Flagged item judgments
1. **The `monitor.quarantined` move.** It is correct and within the brief's requirement (b).
   - `qNotice` is set only in `guardQuarantined`, only on its final `return nil` (`quarantine.go:328`), and at the same point where the inline `d.emit` was before. It is never set on an error return.
   - It is consumed and cleared by the same round's deferred `sendRoundEvents` under `promoteMu` (`startlock_guard.go:378-381`). So the event is sent exactly once, in the same situations, and cannot leak into a later round.
   - The delivery semantics are unchanged: one try, not kept. The order of sends is also unchanged. In the old code the top flush and the `emitStart` flush ran before the inline quarantined emit, so a blocked session lost the quarantined notice in the old code too.
   - `guardQuarantined` has no caller other than `GuardTick` (`promote_lock.go:567`).
   - `mysql.quarantine` (the requarantine and down paths) emits no `monitor.quarantined`.
   - `selfQuarantine`, which `mysql.unlockPromote`'s self-quarantine and `guardExpired` both use, still emits inline (`:765`), so its timing is unchanged.
   - CLI `set-role` touches none of this.
   - No event is lost.
2. **Reports queued in a round go out at the end of that round.** Confirmed: every `emitStart` call site is inside `GuardTick`.
3. **The three early-return subtests pass on the old code.** That is correct and acceptable. On the old code the flush ran at the top of the round, so they could not fail there. They guard against a naive move of the flush to the end of the round.
   - `TestGuardSendsReportsAfterReadOnlyAndHousekeeping` and `…/locked_master` do fail on the old code. In the old code the top `flushStartEvents` sends the kept `late` report while `super_read_only` is OFF and the start lock file is still missing, and on the master it sends before `ensureLocked`.
   - No existing test changed its expectation. `startlock_guard_test.go` only gained lines. `TestForgetReplicaPortCLI` only gained stricter `&&` conditions and new assertions.

### Other checks
- **Locking.** The send runs under `promoteMu`, as before, so it cannot run after `Unlock` or race another round. Rounds never overlap anyway. No deadlock: `Events.Emit` does not touch `promoteMu`. `setStartLockState` only takes `startLockMu` and does not send.
- **Logging.** No new repeating log. `startSendWarned` still limits the flush warning, and `qNotice` is one-shot.
- **promote_files.go.** It adds `ForgottenPorts`, `ForgetAllReplicaPorts` and `forgetAllSeenPorts`. `ForgetReplicaPort(path, 0)` now delegates to them. The "changed" condition (`len(Ports) > 0 || Full`) is equivalent to the old `!(len(seen) == 0 && fullAt.IsZero())`, and the same empty write happens. The effect is identical. The file is outside the brief's file list, but the change is needed so the printed list comes from the same read as the write. Accept.
- **Protocol, dependencies, paths.** No change in `internal/proto`, no new dependency, and no model names or `.superpowers/` paths in the diff.

### ⚠️ Cannot verify from diff
- That the implementer's `make test` and `make test-e2e` runs were on bb54853's tree. I have only the report's word for this.
- Whether the session's write timeout is exactly 10 s and closes the session afterwards (so it is "once per broken session"). I took that from the R-117 text and did not read `session.send`.

### Verdict
The code change meets the brief and is safe. Spec ❌ and quality "Needs fixes" are for the docs only:
- correct the "semua … paling akhir" claim (M1 wording);
- record the inter-round residual of R-117 (I1).

M2 and M3 are optional polish.
