### Item Verdicts

- **F1 (W-1) — job page draws the red-only gate list, disables Ulangi/Setujui, shows D2; no agent call on the GET** — ADDRESSED.
  `internal/panel/web/handlers_jobs.go:344-349`: when `(CanApprove || CanRetry) && DropsData` and `orchestrator.JobGateInput(job)` is gated, `p.Gate = a.gateFor(in, true)`. The POST (`handlers_jobs.go:494-503`) builds its input with the same `JobGateInput(job)` under the same `drops` condition, so GET and POST judge the same input. `web/templates/job_detail.html`: `{{template "gate-list" .Gate}}` above the buttons; `disabled` while `.Gate.Off` on the Setujui opener, both Setujui submit buttons, the Recheck button, "Ulangi dari tahap ini" and "Ya, ulangi"; D2 when `.Gate.RedNote`. Checked that the disabled set cannot lock an operator out of a step that would clear a red row: the approval row is yellow (not red) with `Approval == nil` (`gate.go:607-608`), and `Recheck` exists only for `WaitRestart` (a prepare-master job, which has no job gate). `gateFor` calls `Orchestrator.Gate` only (store, Monitor view, hub map, and for a rebuild job the cached 1-second port dial that every other rebuild screen already makes); `TestJobPageRedGate` asserts `wantCalls(t, f)` empty. Agent text stays inside the list (`outsideGateLists` assertion). Server-side refusal unchanged.
  Test note: `TestJobPageRedGate` only asserts `TargetID != 0` for the GET's input; that GET and POST use the same input is true by construction (same function), not by assertion.

- **F2 (C-1 = W-2; R40) — "Akun panel" row after an upgrade** — ADDRESSED (a, b and c).
  a. `gate.go:204` `textG10bf`, `gate.go:513-545`: fix set only when the row ends yellow and at least one *connected* inventory has an empty account; other rows' G10b keep no fix. The implementer's narrowing (no fix for a server whose MySQL is not connected) is right against the page: the Segarkan button is inside `{{if .Inventory.Connected}}` (`web/templates/server_detail.html:236,271-272`). Rendered text equals the ruling's sentence (`gate_test.go` `wantG10bAccountF`).
  b. README "Pembaruan" new step 2 (after the agent update, which is the right order: an old agent's answer carries no account) with its one-sentence reason; guide 8 intro and "Akun panel" sub-section changed in README and `panduan.html` alike (word-level comparison of the page hunks against the README hunks shows the same sentences).
  c. `promote.go:1431-1467`: a second goroutine of `OnOnline` (same `wg`, started under the same `closed` check) runs `refreshUnknownAccount(r.base, id)`; it reads the stored inventory and calls `RefreshInventory` only for `Connected && Account == ""`. Findings on the points the dispatch named:
     - Locks: none of `startMu`, `repointMu`, the hub's mutexes or the Monitor's mutex is held across the call. Only `invMu`, and only around the store read-modify-write after the answer (`runner.go:313-326`). `OnOnline` returns at once (test holds `startMu` and the agent's answer).
     - Bound and shutdown: the call is bounded by `callTimeout` (2 min, `runner.go:269`), ends when the connection is closed or replaced (`WSConn.Close` fails pending calls first) and when `r.base` is cancelled by `Close`. One goroutine per connect.
     - How often: once per connect while the stored account is still empty. Against an agent of the previous version (its answer has no account) it repeats on every connect, and logs "inventory read on connect (MySQL account was unknown)" although the account is still unknown (Minor, below).
     - What a hostile agent gains: nothing is stored that Segarkan would not store: `Connected`, version, series, database list (cut at `maxInventoryDBs`), missing grants, account through `storedAccount` (one line, 255 bytes). The web handler behind Segarkan has no guard this path skips (`handlers_mysql.go:305-318`). The one new thing is timing: an agent that keeps its account empty has its own inventory row rewritten on each connect instead of only on an operator read or a job step. Every decision that drops data reads the agent again in the orchestrator, so this gives no new outcome.
     - Race with Hubungkan/Segarkan: the agent call is outside `invMu`, so two answers are stored in arrival order, not in the agent's order; an older account can end stored if the operator's Hubungkan lands in the same moments as the connect. The same race already exists between two operator clicks; result is corrected by the next Segarkan, which is what G6f tells the operator. Not a blocker.
     Test: `TestOnOnlineRefreshesUnknownAccount` (start lock held, answer held, row turns red for root, second connect and an unconnected server ask nothing).

- **F3 (C-2; R41) — `backup_stale` unknown while the agent is away or its report is older than `StaleAfter`** — ADDRESSED.
  `alerts.go:108-110` (`ruleCtx.online`), `:389-410` (rule), `:417-422` (`backupReportCurrent`), `:824` (`online: s.online`). Open returns false when not current; close returns true for `unset`, otherwise only `ok && current`.
  - Cannot leave an alert open for ever that should close: role change and promote still close through `!isMaster(v)` / `!judged` (`alerts.go:749-758`), a cleared folder closes even with the agent away, a deleted server's alerts go by cascade. An agent that never returns keeps the alert open, as before the fix. One changed case: raising the age limit while the agent is away no longer closes an open alert until the next report; that is the ruling's "neither opens nor closes".
  - Cannot stop a truly stale backup from alerting once the agent is back: every live report is evaluated at receipt (`monitor.go:563-565`) with `ReceivedAt ≈ now` and `online` true.
  - "Older than StaleAfter" is measured on the panel's clock: `ReceivedAt = panel now − reportAge(TakenAgoMS)` (`monitor.go:554`, age clamped to ≥ 0), restored from the store after a restart (`state.go:161`). The agent's number can only make a report count as older, never newer.
  Tests: `TestBackupStaleNotFromExtrapolation` covers the four cases the item names (agent away with old ok report; open alert kept, also for a connected silent agent; agent back with a fresh stale report opens; first tick of a new Monitor after 62 h).

- **F4 (A-1 = C-5; R39) — truncated scan keeps its judgement and gains the sentence** — ADDRESSED.
  `alerts.go:1101-1103` (`BackupVerdict.Truncated`, set only on the agent's `ok` path, `:1136`), `:1178-1193` (`BackupTruncatedText`, `withTruncated`), `:1218-1220` (G8a, G8b). Card: `backup_folder.go:143-145` for ok+truncated; stale/empty through `BackupMessage(As)`. Alert and gate row detail go through `BackupMessage` (`alerts.go:398`, `gate.go:597`); these are all the call sites of `JudgeBackup`/`BackupMessage`. The sentence is letter for letter the ruling's; the number matches `maxBackupEntries = 10000` (`internal/agent/monitor/backup.go:26`). The sentence is a constant: no agent text passes. Telegram: alert line is icon + message (`notify/message.go:148`), about 300 bytes plus a bounded server name; `TestBackupStaleAlertTruncated` asserts ≤ 1024. A green gate row shows no words, so ok+truncated adds nothing there (judgement kept green; the error of a truncated scan is always toward "older"). README "Cadangan (backup)" has the fact; guide 8 quotes the sentence and `TestGuideRedRowsMatchGate` now holds it to the constant.

- **F5 (C-3 = A-2) — one configure per server at a time** — ADDRESSED.
  `monitor.go:127-132, 168, 410-417, 457-467`. The per-server lock is held from `GetServer` through `hub.Call` to `m.report`. Checked:
  - Callers: `configure` runs only from `goBg` (OnOnline, Reconfigure, ConfigureServer); none holds `m.mu`. `configLock` releases `m.mu` before `Lock`. Nothing takes a config lock while holding `m.mu`, the hub's locks or `startMu`: no cycle.
  - Hold time: each holder gets its own `configureTimeout` (30 s) after it has the lock; an offline agent returns `ErrOffline` at once; a replaced or closed connection fails the pending call at once (`conn_ws.go` `stop()`), so a reconnect's configure is not held behind a dead connection's call.
  - Pile-up: against an agent that is connected but does not answer, N saves drain in up to N × 30 s (before: all in 30 s, in parallel and unordered). Each waiter reads the row fresh, so the last to run sends the last folder. Acceptable.
  - Shutdown: `Close` cancels `m.ctx`, the holder's call ends, each waiter's call returns at once on a cancelled context (`conn_ws.go:82`). `TestConfigureLockDoesNotHoldClose`.
  - Server deleted: the waiter's `GetServer` fails and it returns. The map entry is never removed (Minor, below).
  - Other servers are not held (`TestConfigureInOrderPerServer`).

- **F6 (C-4) — `JudgeBackup` clamps the agent's integers** — ADDRESSED.
  `alerts.go:1128-1141`: negative age → 0, saturating addition, `Size = max(NewestSize, 0)` and size 0 is `empty_file`. No new green: a negative age read "ok" before too; huge age and size ≤ 0 are now red where they were green. `staleAge`/`BackupAge` do integer division only, no overflow with a saturated age (`alerts.go:1238-1261`). Three table cases added.

- **F7 (C-6) — a gate that cannot judge refuses each gated start; emergency promote still starts** — ADDRESSED.
  `gate_enforce_test.go` `TestStartRefusesWhenGateCannotJudge`: seven gated starts and a Retry refused with the gate's own error (not `ErrGateRed`), nothing written; emergency promote succeeds. The hook `gateFault` (`runner.go:182-184`, `gate.go:264-268`) is an unexported field assigned only in `gate_enforce_test.go` (no non-test assignment in `internal` or `cmd`); when set it can only return an error (fail closed) and, when it returns nil, the gate judges as usual; with nil the production path is one nil check.

- **F8 (W-3) — refused resync shows the list G14 points at** — ADDRESSED.
  `handlers_controls.go:221-224` (handler refusal), `:251-254` (`*GateError` from `StartResync`), `:400-408` (`resyncRefused`, `OpenResync: true`). `OpenResync` only sets `data-open` on `#modal-resync` (`replica_detail.html:178`); no agent call is added (the lists in the dialog are the stored ones, and both the handler and the orchestrator check the wipe list again). `TestResyncRefusalOpensDialog` covers both refusals, another start error (closed) and a plain GET (closed).

- **F9 (guide texts; page and README together)** — ADDRESSED.
  Every sub-point is in README and `panduan.html` with the same words:
  - W-4: "Bila tertulis `dikarantina`" (page shows "dikarantina", `handlers_servers.go:681`).
  - W-5: (i) step 5 names **Alamat untuk replika** on the new master's page (`master_detail.html:160`, the same `SourceHost:SourcePort`) and that another spelling makes the agent refuse again; (ii) step 3 starts with `hostname` compared with **Hostname** on the old master's server page (`server_detail.html:97`; both come from the kernel host name, `probe/system.go:41`), "Bila berbeda, berhenti", and step 4 says "Di server yang sama (master lama)" directly before `set-role quarantined`; (iii) closing paragraph as required.
  - W-7: step 9 names **Sumber data** and **Alamat IP** (`internal/agent/cli.go:526`).
  - W-11: "halaman server replika itu"; `reset-admin` asks for `ya` (`cli_admin.go:41,499`); guide 1 step 4 `sudo systemctl restart dbpanel-agent`; guide 3 step 1; guide 6 step 5 names the outcome exactly as `cli.go:687` prints it and adds no command.
  Rules: the only new command is `hostname`. No new step turns read-only off, opens a server for writes, removes a lock or deletes data. Guides 5 and 7 have no changed line in either file (the page hunks touch guides 1, 2, 3, 6, 8, 9 only; README 2649-2868 is untouched). Conditions before `set-role quarantined`, `set-role master` and `approve` are unchanged and still directly before them.

- **F10 (README)** — ADDRESSED.
  Keamanan: the row list now has versi agent and akun panel; the backup bullet says what the unconfirmed form reaches and what a pointed path discloses (status and reason codes: exists, is a folder, holds a regular file within three levels, more than 10,000 entries; age and size). Pembaruan step 1: planned actions refused, agent stays connected and monitored. Prasyarat: the port sentence says what the code does (`gate.go:631-657`: on the skip paths the row is returned with zero status, i.e. shown "Terpenuhi" without a dial), not "not shown"; the implementer's deviation from the item's wording is correct. `unlock-master` comment: "kedua flag OFF sekarang". Backup card sentence for an agent of the previous version: present.

- **F11 (owner's list)** — ADDRESSED.
  Plan file, section "Teks baru untuk dibaca pemilik" only. W-9's three points are there (`permintaan tidak valid`; the mark on `master lama %s masih punya pekerjaan yang belum selesai`; the paragraph naming "Prasyarat", "Cadangan (backup)", "Pembaruan", "Keamanan"). Every operator text of this wave is listed and marked "tidak ada di rencana": G10b fix, the truncation sentence (gate row, card, alert/Telegram), the option form of D2, the job page's list and disabled buttons, and a new sub-section with each guide and README change. I compared the three code texts with the list letter for letter and found no difference, and found no changed operator sentence in the diff that the list lacks.

- **F12 (W-12) — `/panduan` and a promote page in the header and inline-script tests** — ADDRESSED. `web_test.go` `TestSecurityHeaders` and `TestPagesHaveNoInlineScript`: `/panduan` added to both loops; a promote page with a red gate list (`data-gate-list` asserted present) added to both.

- **F13 (W-6) — option wording under the disabled drop checkbox** — ADDRESSED. `_gate.html` `gate-off-option-note`; `replica_databases.html:96`; listed in F11; `TestDropOptionOffNote` and the per-case note in `TestRedRowDisablesButton`. Every other placement keeps D2.

Tests: the fix report names the covering tests per item with RED and GREEN output and the three gate results; each claim I checked against the diff holds. I ran no test: no doubt arose that a run would answer and reading did not. The two adjusted tests still catch what they were written for: `TestJobRetryGatedBeforeToken` still asserts the handler's exact input first and that the same token is accepted after the refusal (the loosened count is the intended second read for the page's list); `TestBackupStaleAlertOpensAndCloses` still has the tick open the alert by extrapolation (48 h − 60 s report, +65 s) and move the wording to "49 jam". The fake monitor agent's held `monitor.configure` now ends with its caller's context, as a WebSocket call does; no assertion depends on the old behaviour.

The diff (2,979 lines) was read in full, in six passes.

### New Breakage in the Fix Diff

No Critical or Important finding. Minor:

1. **Minor — F2a fix sentence points at "server itu" when the row names two servers.** `gate.go:541-544`. Scenario: a resync whose master's MySQL is not connected (G10b, no fix) and whose replica is connected without an account: the row has two sentences and one fix "Klik Segarkan di kartu MySQL halaman server itu"; on the master's page there is no Segarkan button. Smallest fix: none needed for merge; or name the server in the fix.
2. **Minor — guide 8 "Akun panel" gives Segarkan as the fix for the yellow text in every case.** README 3491-3496 and the page. For a server whose MySQL is not connected the gate shows the same yellow sentence with no fix, and the server page has "Hubungkan MySQL", not Segarkan. Not harmful; one clause ("bila kartu MySQL belum terhubung, hubungkan dulu") would close it.
3. **Minor — F2c against an agent of the previous version.** `promote.go:1458-1467`. The read repeats on every connect and logs "inventory read on connect (MySQL account was unknown)" while the account is still unknown; a mid-job agent restart with an unknown account can also store an inventory read during a load, corrected by the job's own final read or the next Segarkan. Smallest fix: log the Info line only when the answer carries an account.
4. **Minor — F2c writes the inventory without an audit entry.** The same read by Segarkan is audited as `mysql_refresh`; the automatic one is only logged. For the owner to decide.
5. **Minor — `configMu` is never pruned.** `monitor.go:457-467`. One small mutex per server id ever configured stays after the server is deleted. Bounded by the number of servers created; a `delete` in `OnOffline`'s deleted-server branch would close it.
6. **Minor — misplaced comment.** `handlers_controls.go:400-409`: `resyncRefused` was inserted between `replicaErrorStatus`'s doc comment and its function, so that comment line now heads the wrong function.
7. **Minor — README wrapping.** Three changed README lines run long (1173-1176, 1512-1514, 3688-3689). Cosmetic.

### Out-of-Scope Observations

- The backup card (`backup_folder.go:136`) and the gate's backup row (`gate.go:588`) still judge by extrapolation in cases the alert now treats as unknown: the card always, the gate row for a connected agent whose report is older than `StaleAfter` (R25 covers only "not connected"). An operator can see a red card or row "berumur 3 hari" with no `backup_stale` alert.
- Guide 3 step 3 ("Hentikan jadwal itu di master lama") still assumes the old master can be entered (the implementer notes it; the ruling named step 1 only).
- README "Sesudah memperbarui ke versi dengan daftar Prasyarat" still leaves out Siapkan sebagai master, Lepas database with drop, and Ulangi/Setujui of a data-dropping job from what is refused (W-2's "while there"; not in F10).
- The job page reads the gate before the superseded check (deferred Task 10 item the web reviewer suggested taking with W-1; not in F1's text).
- `POST /jobs/{id}/retry/prepare` still refreshes two inventories on a stale page whose gate is red (implementer's note).
- The guide test's `^textG[0-9]+[a-z]*$` silently skips a gate text constant named otherwise (implementer's note).

### Verdict

**Fix wave:** All items addressed, no new Critical/Important breakage. F1–F13 are each ADDRESSED; seven Minor notes on the fix itself and six out-of-scope observations go to the owner's list.
