# Security audit — Plan 5a, panel side (web, orchestrator, monitor, notifier, store)

- Date: 2026-11-12
- Code audited: `0912c5f..343898c` (Plan 5a). The checkout is now at `765427b`; `git diff --stat 343898c HEAD` shows only `docs/superpowers/security/2026-11-10-inventaris-risiko.md` (two later commits edit that document), so every file:line below is valid for `343898c` and for HEAD. The cited line numbers were checked again against HEAD after the report was written.
- The audit was written in one sitting that was interrupted after the report file was saved; on resuming, the file was re-read, the line references re-verified, and no part of the dispatch's scope was found left open (what was not covered is listed at the end of section 1).
- Seat: panel side only. The agent side and the whole-branch code review are other seats. Where a panel guard depends on an agent check I read that agent code to follow the data flow and say so.
- Nature: read-only audit of our own code before merge. Nothing was run (no `go test`, no panel, no mysqld); every finding is from reading the code at HEAD, and each names the test that would prove and pin it.

## 1. Scope and method

**Read in full:** `final-web-code.diff`, `final-orchestrator-code.diff`, `final-monitor-store-code.diff`; spec 5.2, 5.5–5.8, 7; plan "Review Focus"; global constraints; `final-ledger-triage.txt`; all open rows of the risk inventory (R-001…R-107). `final-panel-tests.diff` was searched for each property (test names are cited where one pins a property).

**Read at HEAD beyond the diff** (found through `graphify query/explain`, then read):
`web/router.go` (routes, `SecurityHeaders`, `RequireHost`, `limitForm`), `web/handlers_auth.go` (`sameOrigin`, login, `safeNext`), `auth/middleware.go`, `auth/session.go`, `web/handlers_confirm.go`, `store/confirmations.go`, `web/handlers_jobs.go`, `web/handlers_controls.go` (snooze, resync), `web/handlers_mysql.go` (create/rebuild, add/remove databases), `web/handlers_oldmaster.go`, `web/audit.go`, `web/templates/*` changed by the plan, `web/static/app.js` (`confirmModal`, `openDialog`, `pollFragment`, new components), `orchestrator/runner.go` (launch/loop/execute, `Approve`, `Retry`, `confirmDrop`, `Resume`, `cancelJob`, `supersededBy`, `run`, `setStep`), `orchestrator/create_replica.go`, `change_databases.go`, `resync.go`, `snapshot.go`, `oldmaster.go`, `promote.go` (quarantine, repoint, lock refusal), `monitor/alerts.go` (`evaluate`, rules), `monitor/promote.go` (event handlers, close rules), `monitor/state.go`, `notify/notifier.go`, `notify/message.go`, `notify/telegram.go`, `store/servers.go`, `store/alerts.go`, `store/migrate.go`, `hub/hub.go` (event admission), `panel/listener_impl.go`, `panel/servers.go` (handler wrapping). Agent code read only to follow a panel data flow: `preflight.go` (leftover/dedicated/method), `dump.go` (what a load overwrites), `promote_lock.go` (`keepLock` event), `connect.go` (`listDatabases`).

**Passes:** (1) route and middleware table; (2) every caller of `Approve`, `Retry`, the four `Start*` functions and every site that sends `mysql.dropDatabases`, `mysql.cloneFrom`, `mysql.dumpFrom`; (3) each acknowledgement from form field to agent call, including Retry, Approve, Cancel and restart; (4) every agent-supplied string to each sink (HTML, attribute, Alpine, Telegram, log, SQL, audit); (5) alert open/close state machine for the five new kinds and the snooze logic; (6) header set per response class; (7) migration and job-state evolution.

**Attackers considered:** unauthenticated network user; stolen session without the password; an admin tricked by a link or a cross-site page; a compromised agent or MySQL feeding strings and events. **There is no low-privilege panel role**: `users` has no role column and every route uses the same `auth.RequireLogin` (router.go:182-185); the inventory records this as R-091. Object-level authorisation between users therefore does not exist as a control and was not assessed as one.

**Not covered:** the agent side (other seat), browser rendering (stacked `<dialog>` behaviour was read, not exercised), live Telegram behaviour, timing measurements of races.

## 2. Findings

| ID | Severity | OWASP 2021 | Title | Where |
|---|---|---|---|---|
| SP-1 | Medium | A04 Insecure Design (CWE-367) | Approving a rebuild of the old master never re-reads its connections; the check is made at job start, the drop happens after a wait of any length | `orchestrator/runner.go:755-771`, `:633-669`; `orchestrator/create_replica.go:86-99` |
| SP-2 | Medium | A04 / A09 | `master_start_late` is closed by the role change a rebuild job makes, a cancelled rebuild does not bring it back, and the rebuild and delete forms trust a "Periksa data tertinggal" result older than the late start | `monitor/promote.go:627-660`; `web/handlers_oldmaster.go:726-733`, `:459-475`; `orchestrator/runner.go:1089-1096`; `orchestrator/create_replica.go:177` |
| SP-3 | Medium | A04 (CWE-770) | Agent events other than health reports are not rate-limited; the new event alerts open and close at the agent's send rate (alert rows, unthrottled Warn lines, the monitor's global lock) | `hub/hub.go:230-235`; `panel/listener_impl.go:57-61`; `monitor/promote.go:340-376`, `:514-535`, `:573-625` |
| SP-4 | Low | A04 | A job created before a promotion keeps the old master's id: after the promotion its Retry checks `wipe_only` against the old master and copies from the new one | `orchestrator/create_replica.go:298`, `:1081-1100`, `:404`; `orchestrator/runner.go:867-920`; `orchestrator/promote.go:791-797` |
| SP-5 | Low | A04 | The list a `WaitWipe` approval drops is shown only through the step message, which is cut at 4 KiB | `orchestrator/runner.go:102`, `:1501-1503`; `orchestrator/create_replica.go:461-477`; `web/templates/job_detail.html:13`; `web/handlers_jobs.go:207-210` |
| SP-6 | Low | A04 (CWE-362) | A queued approval is bound to the step index, not to the wait it was given for | `orchestrator/runner.go:439-456`, `:501-504`, `:519`; `orchestrator/create_replica.go:457`; `orchestrator/change_databases.go:275`, `:543` |
| SP-7 | Low | A04 | Tokenless paths to a copy are wider than R-105 records | `orchestrator/runner.go:582-597`; `orchestrator/create_replica.go:465-471`, `:784`; `orchestrator/change_databases.go:310-315` |
| SP-8 | Low | A09 | The audit log does not record what was acknowledged (connections, `wipe_only` on Retry, the approved drop list) | `web/handlers_mysql.go:663-664`; `web/handlers_jobs.go:564-566` |
| SP-9 | Low | A04 | An oversized or unreadable `monitor.lockExpired` / start event is dropped without opening any alert | `monitor/promote.go:344-347`, `:577-580`; cross-reference `agent/drivers/mysql/promote_lock.go:593`, `:634` |
| SP-10 | Low | A04 | Event alerts are accepted from a server whose role cannot have the condition; a replica's agent can hold a permanent `master_lock_kept` that tells the operator to run `unlock-master` | `monitor/promote.go:363-371`, `:485-499`, `:466-469` |
| SP-11 | Low | A03 / A05 | Agent strings that bypass the new `agentText` bounds | `monitor/promote.go:284-289`, `:380-391`; `web/handlers_oldmaster.go:166-175`, `:704-717`; `web/handlers_servers.go:664-669`; `orchestrator/runner.go:231-252` |
| SP-12 | Low | A01 / A05 | `?buka=ulangi` and `?buka=resync` open a destructive dialog on a plain GET, also from another site or through `/login?next=` | `web/handlers_jobs.go:318-326`; `web/handlers_mysql.go:830`; `web/static/app.js:534-540` |
| SP-13 | Info | A04 | The never-silenced kinds are an allow-list: a critical kind added later is silenced by default | `notify/notifier.go:51-64`; `notify/message.go:32-51` |
| SP-14 | Info | A01 | No test pins login and CSRF on `POST /jobs/{id}/retry/prepare` (nor cross-site on approve and retry) | `web/router.go:237-240`; `web/web_controls_test.go:514-527` |
| SP-15 | Info | A05 | Header hardening that is optional | `web/router.go:264-293` |

Counts: Critical 0, High 0, Medium 3, Low 9, Info 3.

No finding is a data-dropping path without a valid confirmation: every call of `mysql.dropDatabases` is behind a token consumed for that job or action (section 5).

## 3. Findings in detail

### SP-1 (Medium) — Approve of a rebuild never re-reads the old master's connections

**Attacker / starting position:** none needed; an operator following the normal flow. It is a time-of-check/time-of-use gap in a safety acknowledgement.

**Code.** `StartCreateReplica` reads the old master's connections, or takes the acknowledgement, before the job exists (create_replica.go:86-99). `Retry` reads them again (`retryConnectionsClear`, runner.go:700-729, used at :783 and enforced in `confirmDrop` at :666-668). `Approve` does not: it calls `r.confirmDrop(j, ack, false, true)` (runner.go:768), where the fourth argument is the "connections are clear" answer hard-coded to true, and `confirmDrop` only looks at connections under `if retry` (runner.go:649-669). The only callers of `oldMasterConnectionsClear` are those two (create_replica.go:89, runner.go:719).

**Path.**
1. The operator opens "Bangun ulang sebagai replika". The connection list is complete and empty, so the form asks for no acknowledgement; the orchestrator's own read agrees and the job starts.
2. The job reaches "Periksa server tujuan". The old master holds the same databases as the new master, so the agent reports `leftover` as needing confirmation (agent `preflight.go:536-561`) and the job waits with `WaitWipe` (create_replica.go:465-477). This wait happens on every real rebuild; the form's `ConfirmWipe` never stands in for it (create_replica.go:429).
3. The job waits for a person. There is no age limit on an approval (the 24 h limit is in the `retry` branch only, runner.go:649-656).
4. Meanwhile an application reconnects to the old master (a restarted app server, a cron job, a report tool with the old address).
5. The operator approves with a fresh `job_approve` token. Nothing reads the connections. The run drops the databases and loads the dump, during which read-only is lifted on the old master (R-003). What that application writes during the load is overwritten.

**Harm.** The loss the `accept_connections` acknowledgement exists to make explicit ("Saya paham N koneksi masih ada dan tulisannya hilang") happens with no acknowledgement shown or given. Spec 5.5 requires the check "saat job dimulai" and the code meets that letter; the gap is that the destructive step is not at job start but behind a wait.

**Fix (proportionate).** In `Approve`, for `KindCreateReplica` with `RebuildOldMaster` while the copy is not made yet (`SnapshotGTID == "" && !AdminReplaced`), read the connections before `startMu` exactly as `Retry` does and return `ErrConnectionsRemain` unless `ack.AcceptConnections`. In the web layer: set `ack.AcceptConnections` on approve the way it is set on retry (handlers_jobs.go:459), and render the existing `accept-connections` partial inside the approve dialog for such a job (it already takes `.Server` and `.AcceptLabel`).

**Test that pins it.** `TestApproveRebuildRechecksConnections` (orchestrator): rebuild job waiting on `WaitWipe`, fake agent answers `mysql.connections` with one row; `Approve(id, JobAck{Confirmed:true})` returns `ErrConnectionsRemain`, the job stays waiting and no `mysql.dropDatabases` is called; with `AcceptConnections:true` it runs. A web test that the approve dialog of that job contains `name="accept_connections"`.

### SP-2 (Medium) — `master_start_late` can be closed without the data ever being checked, and rebuild/delete do not look at it

**Attacker / starting position:** none; an operator. The precondition is a late start (the start lock did not hold when MySQL started, the case spec 5.2 raises the Critical alert for).

**Code.**
- `closeStartAlerts` closes `master_start_late`, `master_start_bypassed` and `master_start_blocked` as soon as the server's role is not `quarantined` (monitor/promote.go:644-645).
- Starting a rebuild registers the old master as a replica at once (`AddReplica`, create_replica.go:177), so the monitor resolves its role as replica (monitor/state.go:263-287) and the next tick, 5 s later (monitor.go:20), closes the alert with "server tidak lagi menjadi master lama (dikarantina)".
- Cancelling that job puts the quarantined role back (`keepQuarantined`, runner.go:1096). The three alerts are opened only by agent events (monitor/promote.go:612-617; `master_start_blocked` at :587), which the agent sends once per MySQL start, so nothing opens them again. `master_start_unguarded` is rule-based and does come back; the other three do not.
- The rebuild form warns about left-behind data only when no check exists or the last one found transactions (handlers_oldmaster.go:726-733, `LeftoverOf` at orchestrator/oldmaster.go:61-67). It compares no times. The delete dialog does the same (handlers_oldmaster.go:459-475). Neither the web package nor the orchestrator references the start alerts at all (grep: only labels in handlers_alerts.go:44-47).

**Path A (the alert is laundered).** Late start at T2 → Critical alert opens → operator starts a rebuild; the job waits at `WaitWipe` → the tick closes the alert → the operator cancels (or the job is superseded) → the server is an old master again with no alert. The Telegram group saw "ditutup: server tidak lagi menjadi master lama".

**Path B (the form says nothing).** "Periksa data tertinggal" ran at T1 and found 0 transactions. Late start at T2 > T1: applications write to the old master. The alert is open, but the rebuild form shows no warning (the stored result says 0) and the confirmation text says nothing about it. The operator types the name and the password; the writes made after T1 are dropped with the rebuild. Delete has the same shape.

**Harm.** Writes accepted by the old master during a late start are lost with no warning at the point of decision, and the one alert that says so can disappear without the check it asks for.

**Fix.**
1. Make the form honest: in `rebuildTarget` and `deleteWarnings`, treat the leftover result as "belum diperiksa" when an open `master_start_late` exists for the server or the result's `checked_at` is older than the last late start. The cheapest reliable source is the alert itself: do not close it on a role change (point 2), and look it up with `GetOpenAlert`.
2. In `closeStartAlerts`, do not close `master_start_late` for a role change while the server is a replica in state `creating` of a rebuild job (keep it until `leftoverCheckedAfter`, or until the rebuild's copy succeeded, after which the data is gone and the alert is moot). The other two kinds may close as now.

**Test that pins it.** Monitor: open `master_start_late`, change the store role to replica (creating) and back, run `tick()` in between: the alert is still open. Web: promotion with `leftover_json` checked at T1, alert opened at T2 > T1: the rebuild form and the delete dialog contain the "Data tertinggal belum diperiksa" warning.

### SP-3 (Medium) — Agent events other than health reports are not rate-limited

**Attacker / starting position:** a compromised agent on any paired server (root on one database server; inventory class B), or a faulty agent in a loop.

**Code.** `Hub.OnEvent` limits only `proto.EventHealth` (hub/hub.go:230-235); lag samples are limited in the monitor. Every other event goes straight to `Monitor.HandleEvent` (listener_impl.go:57-61; only the debug log line is limited, :62-70). Plan 5a adds three event-driven kinds and the kept-lock alert:
- `handleStartEvent` (monitor/promote.go:573-625): result `bypassed` opens `master_start_bypassed` (`raiseLocked` → `OpenAlert`, :514-535); result `ok` closes it (:604-609). Alternating the two creates one alert row per pair.
- `raise` for `master_lock_kept` (:366-371) rewrites the message whenever the host set differs (:518-523): one SQLite write per event.
- Each unreadable, oversized or unknown event writes a Warn line with no limit (:345, :353, :360, :578, :584, :592, :599).
- All of it runs under the monitor's single mutex (`m.mu`), which the health path of every server also needs.

**Path.** The agent sends `monitor.quarantineStart` with `{"result":"bypassed"}` and `{"result":"ok"}` in a loop over its authenticated WebSocket. For a server that is not quarantined the 5 s tick also closes what was opened, so the loop works for any role.

**Harm.**
- `alerts` grows by one row per cycle; closed rows are kept for the retention period, so the table and `panel.db` grow at the agent's rate until the panel host's disk fills (jobs, audit writes and health storage then fail for every topology).
- The alert history (500 rows, handlers_alerts.go:16) fills with the flood and pushes real alerts out of view.
- Warn lines flood the panel's journal. The audit log's second copy goes to the same journal (web/audit.go:97-105); journald's per-service rate limit drops lines of a flooding service. That consequence is reasoned from journald's documented behaviour, not tested here (section 6).
- Telegram is bounded: a reopened kind is "berkedip" at most once per 5 minutes per server and kind (notifier.go:29, :417-444).

The health path was hardened against exactly this attacker in Plan 4c; the event path was not, and 5a widens it. Not on record: R-026 covers trust in what an agent reports, R-057 covers audit rows made by a session.

**Fix.** One token bucket per server for events that are neither health reports nor job events, in `Hub.OnEvent` next to `allowReport` (for example burst 10, one token per 5 s; an honest agent sends these once per MySQL start, once per lock decision and at most once a minute while a lock is kept). Count drops into the existing `Dropped` counter and log them through the existing once-a-minute drop log.

**Test that pins it.** Hub test: 1 000 `monitor.quarantineStart` events for one server reach the listener at most burst-many times; a store-level test that the same flood leaves at most burst-many `master_start_bypassed` rows.

### SP-4 (Low) — A job created before a promotion still acts on the old master after it

**Code.** A job's master is fixed when it is created (`jr.master()` returns `st.MasterServerID`, create_replica.go:298). The topology's source address is read fresh on every run (create_replica.go:404, :703). A promotion leaves a replica in state `failed` alone (promote.go:791-797), and `supersededBy` does not compare the job's master with the topology's (runner.go:887-917: topology id, role, state and databases only). `masterProblems` does not look at read-only (prepare_master.go:355-373), so the "Periksa master" step passes on a quarantined old master.

**Path.** Resync of replica R2 fails at "Salin data". Within 24 h the master is promoted away. The operator opens "Ulangi" on the old job with a token. The retry dialog groups the wipe list on the **old** master's inventory (`retryWipeGroups`, handlers_jobs.go:507-517, with `params.MasterServerID`), `enforceWipeOnly` reads the old master's agent (create_replica.go:1082), the preflight names the **new** master as peer, `mysql.dropDatabases` runs (create_replica.go:721-728), and the dump account is created on the old master (snapshot.go) while the dump connects to the new one.

**Harm.** The guard 5a adds ("every database the master lacks is acknowledged") is evaluated against a server that is no longer the master. When the new master is a partial replica (R-006), a database the old master has and the new one lacks is shown under "ada di master, disalin ulang", dropped from the replica and never copied back. The data still exists on the quarantined old master, and the agent's `dedicated` check blocks rebuilding that server while it holds such a database, which is why this is Low. The usual outcome is a job that fails after its drop.

**Fix.** In `supersededBy`, for `seedsReplica` and `changesDatabases` kinds, refuse when `topo.MasterServerID != st.MasterServerID` ("master topologi ini sudah berganti sejak pekerjaan ini dibuat").

**Test.** Failed resync job, `SwapMaster`, then `Retry(id, JobAck{Confirmed:true})` is refused and `Superseded(id)` is not empty.

### SP-5 (Low) — The `WaitWipe` list is displayed through a 4 KiB-capped message

**Code.** The approve dialog for `WaitWipe` says "Database yang disebut di atas DIHAPUS" (handlers_jobs.go:207-210). "Di atas" is the waiting step's message (job_detail.html:13), which `setStep` passes through `redact.Quotes` and cuts at `maxStepMessage` = 4 KiB (runner.go:102, :1501-1503). The approval is bound to the hash of the full list (create_replica.go:452-457), and the full list is dropped (create_replica.go:490, :721-728).

**Path.** An old master (or a target with leftovers) with a few hundred databases: the names past 4 KiB are not shown, the message ends in " …(dipotong)", and one token approves dropping all of them.

**Harm.** The list shown is not the list acted on for long lists. The cut is marked and a token is still needed, hence Low. The Resync dialog, the Retry dialog and the add-existing dialog do not have this problem: they render from job state (`_wipe.html`, `jobPage.TypeDatabases`).

**Fix.** Store the shown list in job state (as `AddExisting` is stored next to its hash) and render it in the approve dialog from state; keep the step message as a summary.

**Test.** Web test: a waiting job whose leftover list is longer than 4 KiB shows every name in the approve dialog.

### SP-6 (Low) — A queued approval can be consumed by a later wait of the same step

**Code.** `launch` queues an action behind any live goroutine whose `pending` is empty, on the assumption that the goroutine is finishing (runner.go:439-456). `execute` then accepts a queued approval when the job is waiting at the same step index (runner.go:501-504) and runs the step with `approved=true` (:519). The step compares the fresh list with the hash stored when the **current** wait was created (create_replica.go:457; change_databases.go:275, :543), so an approval given for wait A matches wait B.

**Path.** Approve #1 starts the goroutine. Approve #2 (a second valid token; or a Retry followed by an Approve) arrives before the goroutine has written status `running` (`Approve` only checks the stored status, runner.go:762), passes `confirmDrop` — for add-databases with typed names checked against list A — and is queued. Run #1 finds the list changed and waits again at the same step with list B. The queued approval then approves B: its databases are dropped without ever being shown, and for add-databases without their names typed.

**Why Low.** It needs two password-confirmed tokens inside a window of about a millisecond and a list that changes in between. It is a hole in the invariant "an approval is bound to exactly what was shown", not a practical attack.

**Fix.** Carry the wait's identity in the action (`WipeHash` / `AddExistingHash` read under `startMu` in `Approve`) and have `execute` drop a queued approval whose hash is not the stored one; or have `Approve` return `errBusy` when a goroutine is live and the stored status is not the one that goroutine last wrote.

**Test.** With the existing `afterReopen`-style hook: launch an approval, queue a second one before the first writes `running`, make the fake agent return a different list: the job waits and no drop is sent.

### SP-7 (Low) — Tokenless paths to a copy beyond what R-105 records

R-105 records a tokenless Retry of a plain create that failed at steps 1–2. From `dropsData` (runner.go:582-597) the same class is also reached by:
1. **Approve of `WaitForeignSource`** on a plain create: no wipe list, not a rebuild, wait reason `foreign_source` → `dropsData` is false → no token and no age limit; the same run may choose CLONE on the form's original `ConfirmWipe` (create_replica.go:465-471, :694).
2. **Retry at "Siapkan replika" or "Salin data" with method `dump`** on a plain create: `dropsData` false → `preflightBeforeDrop` returns at once (create_replica.go:784) → `mysql.dumpFrom` loads again with no fresh preflight and no 24 h limit. The dump drops no database, but a mysqldump load replaces every table it carries (agent `dump.go:238-251` uses mysqldump's defaults). The target has been `super_read_only` since "Siapkan replika", which is what keeps this small.
3. **Retry of add-databases from the dump step when `AddDrop` is empty**: the existing-database read is skipped (change_databases.go:310-315). This is ledger item Task 13 m1 (marked for the final fix wave).

None reaches `mysql.dropDatabases`. Recommendation: widen R-105 to these three, and close 3 with m1's fix (read again whenever `!addFresh && !reattach`). For 1 and 2 the smallest change is to run `preflightBeforeDrop` for every `create_replica`, not only for `dropsData` ones.

**Test.** For 2: a plain dump-based create failed at "Salin data"; on Retry the fake agent must see `mysql.preflight` before `mysql.dumpFrom`.

### SP-8 (Low) — The audit log does not say what was acknowledged

- Rebuild start: the detail holds `rebuild_old_master` but not `accept_connections` or the count acknowledged (handlers_mysql.go:663-664).
- Retry and Approve that succeed: the detail is `job_id` and `kind` only (handlers_jobs.go:566). `wipe_only`, `accept_connections` and the list of databases approved for dropping appear only in the web-layer refusal (:466-467) and in the job log, which is not hash-chained and is deleted with its server.
- R-067 is slightly worse than recorded: a resync's detail now carries `wipe_list` and `wipe_only`, so the 2 KiB cap that replaces the whole detail with `{"dipotong":true}` is reached with about half as many databases.

**Fix.** Add `accept_connections` (and the count shown) to the create/rebuild detail; add `wipe_only`, `accept_connections` and, for an add-existing approval, the approved names to the job action's detail; for long lists store a count and a SHA-256 of the list.

**Test.** Extend `TestRetryRebuildRechecksConnections` / `TestApproveAddExistingTypedNames` (web) to assert the audit detail.

### SP-9 (Low) — An oversized or unreadable lock/start event opens nothing

`handlePromoteEvent` and `handleStartEvent` ignore a payload over 4 KiB or one that does not parse (monitor/promote.go:344-347, :352-361, :577-580, :582-592). For "close nothing on a guess" that is right. For opening it fails open: the agent's kept-lock event carries the unreadable hosts twice, in `Reason` and in `Hosts` (agent `promote_lock.go:593`, `:634`), so about 80 host:port entries exceed 4 KiB and the panel then opens neither `master_lock_kept` nor `master_lock_expired`. `master_locked` (rule-based, snoozable) is the only signal left. At the expected scale (a few replicas) this does not happen; R-102 describes the same scale for the guard itself.

**Fix.** On an oversized or unreadable `monitor.lockExpired`, open the `master_lock_expired` notice with the generic kept text instead of returning; on an unreadable `monitor.quarantineStart`, open `master_start_unguarded`-style wording ("laporan kunci start tidak terbaca"). Agent side (other seat): do not repeat the hosts in `Reason`.

**Test.** `TestLockKeptOversizeStillAlerts`: a 5 KiB kept event leaves one open critical alert.

### SP-10 (Low) — Event alerts are not tied to the sender's role

Any paired server's agent can send `monitor.lockExpired` with `action:"kept"` and hosts; `raise` opens `master_lock_kept` for that server whatever its role (monitor/promote.go:366-371). For a server that is a replica `lockKeptOver` never returns a reason (:485-499) and the kind has no age limit (:466-469), so the alert stays open for good. Its text, with up to 10 agent-chosen host strings of 120 characters, tells the operator to run `sudo dbpanel agent unlock-master` on the master (monitor/alerts.go:465-474) and links to `/masters/<id>`. It is critical and never silenced. A compromised replica agent can use it to push an operator towards opening a master's promote lock by hand during a planned promotion.

**Fix.** In `handlePromoteEvent`, raise `master_lock_kept` only when the monitor resolves the sender as a topology's master (otherwise log once and ignore); in `handleStartEvent`, raise only for a server resolved as quarantined (the tick closes the others anyway).

**Test.** A kept event from a replica opens no alert.

### SP-11 (Low) — Agent strings outside the new bounds

Plan 5a added `agentText` (control and format characters removed, length capped) for the strings it introduced in alerts. Not covered:
1. `le.Candidate` and `qe.NewSource` in `lockExpiredMessage` and `quarantinedMessage` (monitor/promote.go:284-289, :380-391) go into the alert and Telegram as given, bounded only by the 4 KiB payload: line breaks can fake lines of the message, and a text over Telegram's 4 096 limit is refused and retried on every pass. Pre-existing, and `master_lock_expired` / `master_quarantined` are now on the never-silenced list.
2. `ConnectionsResult.Unreadable` and `.Incomplete` (new in 5a) are shown as given (handlers_oldmaster.go:166-175) and copied into the rebuild's password-confirmation text (`ConfirmText`, :704-717, :734-737). HTML and attribute escaping are correct (`html/template`; `x-text` in the dialog), so this is text only, up to the hub's 1 MiB message limit, inside the dialog where the operator types the password.
3. `StartLock.Reason` and `OperatorInitFiles` on the server page (handlers_servers.go:664-669) are uncapped up to the 64 KiB report limit. Escaped.
4. Database names from `mysql.inventory` are stored and shown unvalidated (runner.go:231-252; the agent's `listDatabases` does not filter). They are rendered with `whitespace-pre-line` in wait messages and joined with "\n" in `hashList` (create_replica.go:577-580). Anything acted on is validated by the agent (`dbNameRe` in its preflight, drop, dump and filter commands), so a strange name cannot cause a wrong drop; it can only make a dialog or a log line read wrongly.
5. Telegram messages are plain text (no `parse_mode`, telegram.go:66), so there is no markup injection; Telegram still turns a URL in an agent's reason into a link.

**Fix.** Pass 1–3 through `agentText` with a limit (300 for reasons, 120 per host, 10 init files); validate inventory names at `inventoryCall` with the agent's pattern and show a non-matching name escaped and flagged.

**Test.** Extend `TestStartAndKeptMessagesBoundAgentText` with a candidate and a new source containing "\n" and 5 000 characters; a web test with an `Unreadable` of 10 000 characters and a bidi override.

### SP-12 (Low) — `?buka=` opens destructive dialogs on a plain GET

`GET /jobs/{id}?buka=ulangi` renders the retry dialog open (handlers_jobs.go:318-326; `openDialog`, app.js:534-540) and, for a rebuild job, calls the old master's agent for its connections during the GET (handlers_jobs.go:529-531). `GET /replicas/{id}?buka=resync` opens the Resync dialog (handlers_mysql.go:830). Neither runs the refresh that `/retry/prepare` and `/resync/prepare` exist for, so the grouping shown comes from stored, possibly stale inventories. The session cookie is `SameSite=Lax`, so a link from another site, or `/login?next=/replicas/2?buka=resync` (`safeNext` allows it), lands the admin on a page with the destructive dialog already open. The password (and the typed name for Resync) is still required, and the orchestrator's fresh read still decides; a stale grouping fails closed. Job ids are 128-bit random; replica ids are small integers.

**Fix.** Honour `buka` only when `Sec-Fetch-Site` is `same-origin` (every browser the UI supports sends it), or replace the query flag by a one-time flash set by the prepare handlers.

**Test.** `GET /replicas/2?buka=resync` with `Sec-Fetch-Site: cross-site` renders `data-open="false"`.

### SP-13 (Info) — Never-silenced kinds are an allow-list

`unsnoozableKinds` (notifier.go:51-64) lists the kinds a snooze never silences; `TestUnsnoozableKindsPinned` pins today's set. A critical kind added in a later plan is silenced by default until someone adds it. Suggested: a test that every kind of `monitor.AllKinds()` is in exactly one of two explicit sets (never silenced / may be silenced), so adding a kind fails the build until it is classified.

### SP-14 (Info) — No test pins the guards of the new route

`POST /jobs/{id}/retry/prepare` is wrapped by `authedPost` (router.go:239) and is safe by construction. `TestCSRFOnNewRoutes` iterates a table from an earlier plan (web_controls_test.go:514-527) that has no job route; cross-site refusal on job routes is tested for `cancel` only (web_mysql_jobs_test.go:788-793). Suggested: one test that walks every registered POST pattern and asserts 403 without `Origin`, 403 with a foreign `Origin`, and a redirect to `/login` without a session.

### SP-15 (Info) — Optional header hardening

See section 4. Nothing is missing that a finding depends on.

## 4. Security headers review

**Where set.** `web.SecurityHeaders` (router.go:272-293) wraps the UI mux (router.go:258) and again the outer handler that also carries the agent bootstrap endpoints and the 421 answer (panel/servers.go:228); the plain-HTTP redirect handler is wrapped too (tlsui.go:903).

| Header | Value in code | Assessment |
|---|---|---|
| Content-Security-Policy | `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'` | Strict. No `unsafe-inline`, no `unsafe-eval`. `frame-ancestors`, `form-action`, `base-uri`, `object-src` all present. `font-src` falls back to `'self'`. |
| X-Frame-Options | `DENY` | Present, consistent with `frame-ancestors 'none'`. |
| X-Content-Type-Options | `nosniff` | Present. |
| Referrer-Policy | `same-origin` | Present; the reason it is not `no-referrer` is documented (Origin on same-origin form posts). |
| Permissions-Policy | `camera=(), microphone=(), geolocation=()` | Present, short. Optional additions: `payment=(), usb=(), serial=(), bluetooth=(), display-capture=(), browsing-topics=()`. |
| Cross-Origin-Opener-Policy | `same-origin` | Present. |
| Cross-Origin-Resource-Policy | `same-origin` | Present. |
| Strict-Transport-Security | `max-age=31536000`, only when `r.TLS != nil` | Correct for a panel reached by IP or a single host name; no `includeSubDomains` by design. HSTS does not apply to IP literals (R-008 / SEC-57, on record). |
| Cache-Control | `no-store` by default; static files `public, max-age=3600`; SSE `no-cache` | Correct for the connection list, job logs and alert pages. |

**Per response class added or touched by 5a** (all pass through the same wrapper, so the set is identical):

| Response | How produced | Headers |
|---|---|---|
| Full pages (job page with retry and approve dialogs, replica page, old-master page, alerts, settings) | `renderStatus` (render.go:221, :235), `text/html; charset=utf-8` | full set, `no-store` |
| 400 / 403 / 409 renders added by the plan (`renderJob` after a refused token or acknowledgement; `confirmRefused` for kind `job`) | same renderer with the status | full set, `no-store` |
| Fallback errors (`http.Error`: 404 job, 403 CSRF, 413, 500) | `text/plain; charset=utf-8` + `nosniff`; Go's `http.Error` keeps the other headers (checked in Go 1.26.10 `net/http/server.go:2321-2339`) | full set, `no-store` |
| Fragment (`/servers/{id}/connections-fragment`) | renderer, HTML | full set, `no-store` |
| JSON (`/confirm`, `/jobs/{id}/log`, `/alerts/count`) | `application/json` | full set, `no-store` |
| Redirects (303 after prepare, retry, approve, snooze) | `http.Redirect` | full set, `no-store` |
| Static 404 | Go's file server removes `Cache-Control` on its error path | no `Cache-Control`; not sensitive |

**Cookies.** `__Host-dbpanel_session`: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, no `Domain` (auth/middleware.go:55-64). Lax rather than Strict is what makes SP-12 reachable from another site; the POST side is covered by `sameOrigin`.

**Inline code.** The changed templates contain no `<script>` body, no `style=` attribute, no `on*=` handler and no `javascript:` URL (searched). The three script tags are external (layout.html:7-12). New Alpine components (`confirmSnooze`, the `confirmName` getter) are registered in `app.js`; templates pass data through `data-*` attributes and bound getters, never as an expression built from server data. No `template.HTML`, `template.JS` or `x-html`; the single `template.HTMLAttr` (render.go:99) is fed from a fixed menu key. `innerHTML` is used only for two server-rendered fragments (connections, app.js:713; status box, app.js:783), both escaped by `html/template`.

**Gaps.** None that weakens a control. Optional: a longer Permissions-Policy; `Sec-Fetch-Site` as a second CSRF signal on POST and as the gate for SP-12.

## 5. Checked and found sound

**Access control and CSRF**
- Every state-changing route is `authedPost` = login → `sameOrigin` → `limitForm` (router.go:183-185, :193-250), including the new `POST /jobs/{id}/retry/prepare` (:239). No state change is reachable by GET; the GETs that call an agent only read (connections fragment; SP-12's `?buka=ulangi`).
- `sameOrigin` rejects a missing, `null` or foreign Origin/Referer (handlers_auth.go:165-178). Form bodies are capped at 64 KiB (router.go:322-346), which also bounds `confirm_db[]` and `wipe_only[]`; the checks over them are at worst quadratic in a few thousand names.
- Job ids are checked against `^[0-9a-f]{32}$` before any lookup (handlers_jobs.go:30, :391-396) and are 16 random bytes (runner.go:274-278).

**Confirmation tokens**
- 32 random bytes, stored as SHA-256 (handlers_confirm.go:183-188, :126-129); 2-minute life; consumed by one atomic `UPDATE … WHERE … used_at IS NULL AND expires_at > ?` bound to username, action, target kind and target id (confirmations.go:36-52). A double submit loses the race in SQLite.
- A job token's action is `job_retry:<id>` or `job_approve:<id>`, issued only with kind `job`, a well-formed id and a positive target id (handlers_confirm.go:102-117, :146-151), and consumed against the job's own server id from the store (handlers_jobs.go:449). A token for another job, another server, the other action, or a `resync` token is refused: pinned by `TestJobTokenBoundToJob` (web_confirm_test.go:487-537).
- The password check shares the login's per-user budget and reserves its slot before bcrypt (handlers_confirm.go:154-182).
- Token bound to the user, not the session: on record as R-073.

**Every path to a drop**
- `Approve` and `Retry` are called only from `approveJob` / `retryJob` (handlers_jobs.go:410, :414). `launch` is called from `createJob`, `Approve`, `Retry`, `Cancel` only (runner.go:288, :771, :818, :939).
- `Resume` after a restart fails every running or queued job (runner.go:945-1003); nothing continues by itself, and a waiting job keeps waiting. `preflightFresh` and `addFresh` live in memory only, so any new run reads again before a drop (runner.go:1336-1343, fields `preflightFresh` and `addFresh`).
- `mysql.dropDatabases` has three call sites: the wipe list (create_replica.go:724), a removal with data (change_databases.go:396), the approved existing databases of an add (change_databases.go:554). Each is reached only with `dropsData` true for the job, so `confirmDrop` demands `ack.Confirmed` on every Approve and Retry (runner.go:633-640), and the first run follows a token consumed by the start handler (resync handlers_controls.go:208; rebuild handlers_mysql.go:644; removal handlers_mysql.go:1011) or by the approval that created the list.
- Enforcement is in the orchestrator: the web's `DropsData` answer only decides whether to ask; the orchestrator computes it again under `startMu` and returns `ErrJobNeedsConfirm` (pinned by `TestApproveLeftoverNeedsToken`, `TestRetryResyncFailedAtCopyNeedsToken`, `TestRetryCloneCreateFailedAtCopyNeedsToken`). Unreadable params count as dropping (runner.go:601-607).
- 24 h limit on Retry, from `ConfirmedAt` or else the job's creation, so a job from before the upgrade fails safe (runner.go:649-656; `TestRetryRefusedAfter24h`).
- `mysql.cloneFrom` on Retry or Approve is covered by `cloneLeft` (runner.go:588-590). The remainder is R-105 and SP-7.

**Acknowledgements**
- `wipe_only[]`: the web check uses stored inventories (handlers_controls.go:171-204; handlers_jobs.go:482-503); the deciding check is `enforceWipeOnly` against a fresh read of the master, failing closed when the master cannot be read, before the preflight and therefore before any drop, on start, Retry and Approve alike (create_replica.go:410-414, :1081-1100; `TestPreflightUsesFreshMasterInventory`, `TestPreflightRefusesWhenMasterInventoryUnreadable`, `TestRetryResyncNeedsWipeOnlyAgain`). An empty wipe list is not a bypass: the agent fails the preflight for any database outside the list that the master lacks (`dedicated`, agent preflight.go:531-547) and asks for a tokened approval for those the master has (:548-562). Duplicates are refused or compacted; names compare exactly; only listed names are stored (resync.go:82-84; runner.go:657-665). A truncated master inventory makes names look replica-only, which fails safe (R-104 records the unsafe direction for add-databases).
- `accept_connections` at start: the handler re-reads and forwards the tick only when the form asked for one (handlers_mysql.go:688-695); without it the orchestrator reads again and refuses anything but a typed, readable, complete, empty answer (create_replica.go:86-99, :213-215). On Retry the stored acknowledgement never counts (runner.go:700-729). Gap: Approve, SP-1.
- `confirm_db[]`: compared in the orchestrator against the stored list, trimmed, exact case, as a set; an empty stored list never matches (runner.go:645, :683-693). The list is read fresh at the step, and an approval counts only when the fresh list's hash is the stored one (change_databases.go:262-279, :530-548). A Retry of that wait re-checks and never approves (`approved` is false for a retry, runner.go:519). Pinned by `TestApproveAddExistingNeedsTypedNames`, `TestAddExistingWaitsBoundToHash`, `TestAddExistingRetryRechecks`.
- Typed replica name for a removal with data is checked on the server (handlers_mysql.go:1017; `TestRemoveDatabasesDropNeedsTypedName`); the Alpine getter only decides whether the dialog asks.

**Injection and encoding**
- All new store functions use placeholders (store/servers.go:229-293; store/alerts.go:170-173). Times are fixed-width UTC RFC 3339, so the string comparisons in `SetSnooze` and `EndedSnoozes` order correctly (store.go:109).
- Agent strings in templates are escaped by `html/template` in text and attribute context (`_connections.html`, `_wipe.html` `value="{{.}}"`, `server_detail.html`, `data-confirm-text`); `TestCutoverEscapesAgentText` pins the connections fragment.
- The five new alert texts bound the agent's words with `agentText` and cap the host list at 10 (monitor/alerts.go:393-475; `TestStartAndKeptMessagesBoundAgentText`).
- Telegram messages are plain text and the token is scrubbed from errors (telegram.go:59-100). The digest contains only the validated server name, kind names and times, at most 30 lines (message.go:282-321).
- Audit outcome and username are made one line before hashing (audit.go:85); the token and password are never audited.

**Alerts and snooze**
- `master_start_unguarded` is judged whatever the role and opens for a report with no start lock state at all, so an old agent reads as not guaranteed (monitor/alerts.go:344-370). An open one stays open across a panel restart with the agent offline (the close rule needs a report that says locked). The unjudged case is R-107.
- A start event closes an alert only with a result the panel knows; an unknown result changes nothing (monitor/promote.go:595-610). Nothing is closed while the role is unresolved (:639-641).
- A snooze lets the first message and the recovery of a never-silenced kind through and holds only repeats (notifier.go:381, :459). A failed first send is retried during the snooze because `notify_count` rises only on a delivered message (store/alerts.go:190-197; `TestCriticalFailedSendDuringSnoozeIsRetried`). Kinds are set by panel code only; an agent cannot choose a kind string.
- Snooze hours come from a fixed list (handlers_controls.go:27, :323-328). Snooze state is per server and re-read from the store on every tick (monitor.go:592). The digest is owed in the store, survives a restart, and is cleared only after Telegram accepted it (notifier.go:499-524).

**Data integrity**
- Migration 0011 runs in one transaction with its version row and is skipped once applied (migrate.go:97-116); the column is nullable and NULL means "nothing owed". An older binary still works on the migrated table (columns are named in every query).
- New `jobState` fields are `omitempty` and their zero values are the safe ones: no `ConfirmedAt` → creation time; no `WipeOnly` → nothing acknowledged; no `AddDrop` → nothing dropped.

## 6. Unconfirmed suspicions

1. **Journald drops audit copies during a log flood (part of SP-3).** I confirmed the Warn lines are not limited and that the audit copy goes to the same logger. That journald then drops the service's lines is from its documented rate limit, not observed here.
2. **`master_lock_kept` closed by a backlog report.** `lockKeptOver` closes on a healthy master report received after the alert opened (monitor/promote.go:495). I did not trace whether a backlog report taken before the lock but delivered after it can set that status; the agent repeats its kept event and `raise` would reopen the alert.
3. **Schemas the inventory does not list.** `addExisting` trusts `mysql.inventory`. I read `listDatabases` (only the four system schemas are excluded) but not every path that could hide a name from `information_schema.schemata`.
4. **Stacked dialogs.** `confirmModal` closes the source dialog and submits its form with `form=`-associated inputs (`confirm_db[]`). Read, not exercised in a browser; the ledger already asks for a manual check.
5. **Page latency.** `GET /jobs/{id}?buka=ulangi` for a rebuild job waits for the old master's agent up to the direct-read timeout before it renders. I did not measure it.

## 7. Residual risks, inherent or accepted, and whether they are on record

| Risk | On record |
|---|---|
| One admin account, no roles; every session sees every job log over SSE | R-091 |
| Token bound to the user, not the session | R-073 |
| A stolen session can still snooze (never-silenced kinds go out), cancel jobs, refresh inventories and start tokenless jobs | by design (spec 5.7); no rate limit on these posts: R-057 |
| Critical kinds can be switched off globally in Telegram settings | R-106 |
| Quarantined server that never reported opens no `master_start_unguarded`; no digest for a snooze running at the migration | R-107 |
| Inventory cut at 5 000 databases lets add-databases miss an existing one | R-104, fix planned in the final wave |
| Tokenless Retry of a plain create at steps 1–2 | R-105 — **wider than recorded**, see SP-7 |
| Audit detail over 2 KiB replaced whole | R-067 — **slightly worse**, see SP-8 |
| Telegram is an external channel carrying server names and internal addresses | R-055 — 5a adds replica host:port lists (up to 10), the lock file path and the agent's reason (300 characters) to it |
| Data from an agent is trusted (start lock state, connection list, events) | R-026, R-095; SP-3, SP-10 and SP-11 are the parts that code can reduce |
| Connection list and salvage answers not paginated | R-061 |
| A new snooze started before the last digest went out loses that digest; a failing digest is retried every pass | ledger, Task 10 minors; not worse than recorded |
| `kept` with hosts does not close an open `master_lock_expired`; a changed host set sends no new message | ledger, Task 7 minor; not worse than recorded |
| One snooze dialog per row on the alerts page | ledger, Task 10 minor; bounded by the 500-row history |
| `prepareRetry` not gated on the job's state; `?buka=ulangi` inventory error gives 500 | ledger, Task 12 minors; confirmed, no security effect beyond SP-12 |
| Old master writable during the rebuild load | R-003; SP-1 is the part of it the acknowledgement was meant to cover |
| A compromised panel can order any drop | class B (R-019, R-021); spec 5.6 says the `wipe_only` enforcement is not a defence against it |

Not on record and proposed as new rows: SP-1, SP-2, SP-3, SP-4, SP-5, SP-6, SP-9, SP-10, SP-12.
