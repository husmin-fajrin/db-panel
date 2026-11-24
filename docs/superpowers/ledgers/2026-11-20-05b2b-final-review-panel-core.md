# Final review — panel core — Plan 5b-2b (78b1bb9..d12a4df)

Scope: `internal/panel/orchestrator`, `monitor`, `store` (migration 0013), `redact`, `hub`, `notify`,
`panel.go`, e2e tests in `internal/panel/*_test.go`.

How it was read: the production diff (`final-panel-core-prod-…diff`, 2949 lines) in full, in four
passes; the surrounding code in the tree where a rule depends on it (the whole of `StartPromote`,
`StartCreateReplica`, `StartResync`, `Approve`, `Retry`, `Monitor.report/ingest/load/tick/evaluate`,
hub `Attach/Detach/IsOnline`, the agent's `monitor.configure` handler); the test diff by test name where a
rule had to be checked. No test that starts MySQL was run. One throw-away probe was run with
`go test -overlay` (file outside the tree, nothing written to the repo) to confirm C-2.

## Strengths

- **One gate, fail-closed, in the right place.** `Runner.Gate` (gate.go:253) reads only the store, the
  Monitor's views and the hub's connection map; `refuseRedGate` (runner.go:672) refuses on a red row *and*
  on a gate that cannot judge. In every gated `Start*` it is the last check before the first store write
  and it runs under `startMu` (promote.go:295-306, create_replica.go:182-194, resync.go:86,
  change_databases.go:115/153, prepare_master.go:65). `TestStartRefusesOnRedRow` asserts for each action
  that no job, no replica row and no promotion exist afterwards and exactly which agent commands were sent.
- **Emergency promote.** The dead-master tick is checked at promote.go:205, after the mode is decided and
  before `AddPromotion`; the test covers a posted mode and an implied one (`Mode: ""`) and asserts that
  nothing was recorded. The only new refusal an emergency can meet is that tick.
- **K4 kept by construction.** `gateActionForJob` has no case for the promote kinds, and `JobGateInput`
  requires `dropsData`; `TestRetryGatedOnlyWhenDataIsDropped` runs a Retry of a failed promote with a red
  row.
- **R22 done carefully.** `forgetBackup` (state.go:663) replaces the snapshot by a copy (views already
  handed out are not changed), rewrites the stored health row so a restart does not bring the old report
  back, and leaves an uncommitted backlog batch to its own commit. The test checks all three.
- **Port probe.** One dial per master per 10 minutes, shared by concurrent gates; the `done` channel is
  closed in a `defer`, a panic is recovered into "closed" and not cached; the map's mutex is never held
  across the dial.
- **Agent text.** Nothing free-form from an agent reaches a row, an alert or Telegram unbounded: version
  and role pin are cut to 32 characters, the backup reason goes through a closed word map, the account is
  never shown, `GateError` and the audit's `skipped_red` carry labels/keys only.
- **R6/R7.** `hub.SentByAgent` separates an agent that answers `offline` from a dropped link exactly at the
  two sites the plan names; the promote start keeps the careful reading.

## Issues

### Critical (Must Fix)

None.

### Important (Should Fix)

**C-1 — After the upgrade the "Akun panel" row is yellow on every existing server, never resolves by
itself, has no fix text, and the guide tells the operator to wait; a `root` account therefore passes
planned actions unnoticed.**
`internal/panel/orchestrator/gate.go:504-527` (`accountRow`, branch `inv.Account == ""` → G10b, no Fix);
`combine` gate.go:362-368; migration `0013_gate.sql` (`account` defaults to `''`); README 3413-3417 and
3829-3835.
What is wrong: `Inventory.Account` is only written by `inventoryCallCut` (runner.go:265), which runs on
"Hubungkan MySQL", "Segarkan" and inside some job steps for the *target/candidate*. Nothing asks an
existing server for its account after the upgrade. R30 makes the unknown account yellow (correct: not
green), but R28 gives G10b no fix, and README/Panduan say of every G10b row "tunggu laporan berikutnya,
lalu muat ulang" — for this row no report ever comes.
Scenario: a hospital upgrades from the previous version; master `db1` was connected with `root` (the
common case the row exists for). The operator follows "Pembaruan": agents updated (Versi agent green),
opens Promote…: "Akun panel — Perlu perhatian — Akun panel belum diketahui: agent db1 belum
melaporkannya." No fix line. They wait, reload, nothing changes; the button is enabled, the planned
promote runs with the panel still logging in as `root`. The red rule of spec 5.7 is silently not applied
to exactly the installs it targets, until someone happens to click Segarkan.
Smallest fix I would accept: (a) in `accountRow`, when the row ends yellow, set its Fix to a sentence that
names the button, e.g. `Klik Segarkan di kartu MySQL halaman server itu, lalu muat ulang halaman ini.`
(new text → owner's list; test `TestGateAccountUnknownWithoutInventory` pins `""` today), and add the same
step to README "Pembaruan" (before step 2) and to the G10b sentence of the guide. Better, and still small:
(b) in `Runner.onOnline`, when the stored inventory is `Connected` with an empty `Account`, call
`RefreshInventory` in the background (read-only on the agent), so the row judges itself after the agent
update. (a) is the minimum; (b) removes the operator step.

**C-2 — `backup_stale` opens from extrapolation alone while the master's agent is away, and right after a
panel restart that followed a long stop.**
`internal/panel/monitor/alerts.go:384-409` (rule) with `JudgeBackup` alerts.go:1109
(`age = NewestAgeSec + (now − ReceivedAt)`); `Monitor.load` state.go:127-169 restores the stored
snapshot and `ReceivedAt`; `tick` monitor.go:641 evaluates 5 s after start.
What is wrong: R25 took this case out of the *gate* ("an offline master would read `stale` from
extrapolation alone"), but the alert rule still judges it. The plan's own rule for the alert is "unknown
neither opens nor closes".
Scenarios (both confirmed with a throw-away overlay test, output: `Berkas terbaru di folder cadangan
master db-master berumur 3 hari 7 jam (batas 48 jam).`):
1. Nightly backup, limit 48 h. The panel host is off from Friday evening to Monday morning (62 h); the
   last stored report said "17 h old". On start the first tick opens `backup_stale` for every master with
   a folder and Telegram sends "berumur 3 hari 7 jam", although last night's backup exists. Seconds later
   the agent reconnects, reports 7 h, and a "pulih" follows. One false alarm + recovery per master after
   every long panel stop.
2. The master's agent is disconnected for more than (limit − age) hours while MySQL and the backup job run
   fine: the alert opens and its text grows every hour with an age the panel does not know, next to
   `agent_offline`.
Direction is safe (a false alert, never a missed one) and there is no storm (one alert per master; hourly
text changes do not notify), which is why this is not higher. But it tells a non-expert operator something
false about their backup, and no test covers it.
Smallest fix: in the rule, treat the verdict as unknown (neither open nor close) when the last report
cannot be trusted as current: pass `s.online` into `ruleCtx` and return "not open / not close" when
`!ctx.online || ctx.now.Sub(v.ReceivedAt) > ctx.set.StaleAfter` (an online agent reports at least every
SummaryEvery, so honest extrapolation stays within that bound; the existing "47 h + 1 h tick" case keeps
working because the agent re-reports within the summary interval — adjust that test to send a report or
keep the clock step under StaleAfter). Add the two scenarios above as tests.

### Minor (Nice to Have)

**C-3 — Two `monitor.configure` calls for one server can reach the agent out of order (named item 4).**
`internal/panel/monitor/monitor.go:368-373` and `403-440`: `configure` reads the server row, then takes
`m.mu` for the settings, then calls the hub; nothing orders two goroutines of the same server.
Failure: G1 (spawned by save A) reads folder A and is held on `m.mu` (a tick evaluating alerts, a health
row being written); save B completes and G2 reads B and sends; G1 then sends A. The agent ends on A, the
store says B; A's "ok" report is judged as B's: card fresh, `backup_stale` closed as recovered, gate row
green, until the next configure of that server (reconnect, any settings save, another folder save).
How real: it needs the second save (store write + audit + goroutine start + send) to fit inside G1's
read-to-send gap, i.e. milliseconds; a double click sends the same values and is harmless (both goroutines
read the store when they run). Two operators or a script could hit it; a person at one form practically
cannot. What makes it worth closing is that the result is silent, is a false green on a safety row, and
lasts until the agent reconnects. The same micro-window exists between `OnOnline`'s configure and a save.
Smallest sound fix (panel only, ~10 lines): serialise per server the pair "read server row + hub call" in
`configure` (a `map[int64]*sync.Mutex` guarded by `m.mu`, its lock held across the call; `m.mu` itself
stays free during the call). Because every save spawns its configure after its store write, the last
serialised section always carries the last stored folder. A generation number echoed in `BackupState`
(panel picks it, agent returns it; a number, not the path, to keep "codes and numbers only") would also
close the refill window (deferred Task 8), but needs a protocol and agent change — can wait.

**C-4 — `JudgeBackup` trusts the agent's integers.** alerts.go:1109-1116. A huge `NewestAgeSec` wraps the
sum negative and reads "ok"; a negative one reads "ok"; a negative `NewestSize` passes the 0-byte check.
An honest agent clamps to ≥ 0 (agent `secondsSince`), and a hostile agent can say "fresh" outright, so
this is robustness, not a hole. Fix: `age := max(b.NewestAgeSec, 0)`, saturate the addition, and judge
`b.NewestSize <= 0` as empty.

**C-5 — The panel ignores `BackupState.Truncated`.** alerts.go:1092-1118 (and nothing else in
`internal/panel` reads it). When the 10,000-entry limit ends a scan after a file was found, the agent
reports the newest of the entries it happened to read, and the panel judges it as the folder's newest. A
folder with more than 10,000 entries (per-table dumps × retention) can then read "berumur 9 hari" red,
with fix "Jalankan cadangan master…", which does not help; nothing tells the operator about the limit.
Safe direction (wrong red). Fix: when `Truncated` and the verdict would be stale/empty, either answer
`unknown` with its own sentence or append the limit to G8a (new text → owner).

**C-6 — No orchestrator test for "a gate that cannot judge refuses the start".** runner.go:672-678. The
web test uses a fake orchestrator; if `refuseRedGate` stopped returning the gate's error, no test in
`internal/panel/orchestrator` would fail. One case in `TestStartRefusesOnRedRow` (e.g. a rebuild whose
`MasterID` has no topology, or a store fault) closes it.

**C-7 — `View.Backlog` is not persisted.** state.go:161 (`load` restores `receivedAt` and the snapshot,
not `snapBacklog`). After a restart a stored backlog snapshot is read as a live one by `ClockSkew`
(monitor.go:90), against R-focus 1's intent. Effect is tiny: the clock row also needs the agent online,
and the first live report (the answer to `monitor.configure`) follows within moments. Note only; fix by
storing the flag or by treating a snapshot loaded at start as backlog until a live report arrives.

## Named items — explicit verdicts

### 1. The gate has no side door — not a defect

Every path that creates a job or changes role/replication, and how it meets the gate:

| Path | Gate |
|---|---|
| `StartPromote` planned (promote.go:297) | passes `Gate` under `startMu`, before `AddPromotion`; backup tick checked after |
| `StartPromote` emergency | exempt by plan; needs the D5 tick (promote.go:205) |
| `StartCreateReplica` plain / rebuild (create_replica.go:184-194) | passes `Gate` (`create_replica` / `rebuild_old_master` with `Approval=true` after `requireRebuildApproval`), before `RemoveReplica`/`AddReplica`. The flag cannot be used to dodge rows: a quarantined target without `RebuildOldMaster` is refused by `refuseAwaitingRebuild`, a non-quarantined one with it at line 215 |
| `StartResync` (resync.go:86) | passes, before `SetReplicaState` |
| `StartAddDatabases` (change_databases.go:115) | passes |
| `StartRemoveDatabases` with drop (:153) | passes; without drop: exempt by plan |
| `StartPrepareMaster` (prepare_master.go:65) | passes |
| `Retry`, `Approve` (runner.go:991, 924) | pass `refuseRedJobGate` before `confirmDrop` when `dropsData`; a promote job and any non-dropping remainder: exempt by spec/K4 |
| `StartLeftover`, `StartSalvage`, `StartMySQLTLS`, `StartTightenTLS`, `RenewMySQLTLS` | exempt by plan |
| `StopReplica`, `StartReplica`, `Relock`, `UnlockMaster`, `DetachReplica`, `RepointNow`, `DeleteServer`, `DeleteUnquarantined`, `Cancel` (+ its cleanup `job.cancel`, `resetReplica`, `releaseRole`) | exempt by plan |
| `OnOnline` (pending `mysql.quarantine` / `mysql.repoint`), `Resume` of running jobs, `ConfirmCutover` | continuation of work already started or a store mark; not in the spec's table |
| `Connect` / `RefreshInventory` (`mysql.connect`, `mysql.inventory`) | not gated; it is the fix for the account row |
| `SalvageDelete` (`mysql.salvageDelete`, deletes one salvage file) | not in the spec's gated table (salvage family); pre-existing |
| Monitor `monitor.configure` (role hint for an unpinned agent; now also the backup folder) | not gated; pre-existing, folder documented as residual risk |

Older data-dropping commands (`mysql.dropDatabases` ×3, CLONE/dump load, `mysql.resetReplica`,
`mysql.configure`) are only sent from steps of jobs whose start is in the first seven rows. No unintended
bypass found.

### 2. Gate under `startMu` with the port probe (R29) — not a defect

- Lock order: `startMu → Monitor.mu` (Health.View; already existed through `Alerts.JobFailed`),
  `startMu → hub.mu` (IsOnline, map only), `startMu → ports.mu` (map only). The Monitor never calls the
  Runner under `m.mu` (`Deps.CopyStarted` reads the store only); the hub calls listeners under `lifeMu`,
  never under `hub.mu`, and `Runner.OnOnline` only starts a goroutine. `startMu → lifeMu → m.mu` exists via
  `deleteServer`→`Hub.Disconnect`; nothing goes the other way. No cycle.
- 1-second bound: `dialPort` uses a 1 s context on `r.base` and `net.Dialer{Timeout: 1s}`; with
  `CGO_ENABLED=0` the resolver honours the context, so a name that does not resolve or a tarpit costs at
  most 1 s. A closed or timed-out port is a normal answer and is cached 10 minutes, so a slow or hostile
  `SourceHost` stalls starts by at most 1 s per 10 minutes per master, and only for planned promote and
  rebuild (the only gated actions with the row).
- Recover-into-closed: `probePort` recovers, the answer is not kept (`at` zero), and `done` is closed by a
  `defer` registered before the probe, so waiters under `startMu` are always released
  (`TestGatePortProbePanicReleasesWaiters`).

### 3. Rows a compromised agent can turn green — by design; document, not a defect

Each agent can forge only rows about its own server; no agent's report colours another server's row.

| Row | Source | Planned action that then passes | Worst outcome |
|---|---|---|---|
| Versi agent | probe at connect (`UpdateProbe`) | every gated action on that server | a step fails midway on an agent that lacks a command (K1's reason) |
| Pin master | master's health report | Buat replika, Resync, Tambah database, planned promote | none new: the pin is enforced by that agent itself |
| Jam | own report (`ClockUnixMS`, `TakenAgoMS`) | planned promote | lock deadlines and expiries on that server judged by a wrong clock |
| Ruang disk | own report | Buat replika, rebuild, Resync, Tambah database | the copy fills that server's disk; the agent's own preflight checks again |
| performance_schema | master's report | same four | the master-side reads that need it fail or see nothing |
| Cadangan | master's report | planned promote, rebuild | the most serious: a rebuild wipes the old master's full copy while the new master in fact has no backup, leaving one copy on a host that lies |
| Akun panel | own `ConnectResult.Account` | all | the panel keeps using `root` there unnoticed |
| TLS (replica part) | `tls_verified` follows the replica's own report (`syncTLSVerified`) | planned promote | an unverified link counted as verified |
| Persetujuan | own answer | rebuild of itself | none: that agent enforces it |

README "Keamanan" should list Versi agent and Akun panel too (deferred Task 14 item: fix before merge, one
line).

Green on missing/old data where it should be yellow: no row found that is green on *missing* data.
On *old* data: Pin master and Versi agent are green from the last stored report/probe even while the
agent is offline (plan text makes only "no report" red; a start then fails at its first master call, and
the agent enforces the pin); clock, disk and performance_schema are judged from the last report whenever
the agent is connected, which is bounded by the summary interval, with a short window between a
reconnect and the first live report; the backup row has C-3 and the refill window. None of these is
above Minor. The one rule that does act on old data is the alert (C-2), and C-1 is the opposite problem
(yellow that never becomes known).

### 4. Backup report through the Monitor — R22 sound; race is C-3 (Minor, fix cheap); `JudgeBackup` C-4

- R22 is right and correctly built (see Strengths). Its stated cost is accurate.
- Refill window: real but short and self-healing. The agent applies `Configure` first, then flushes its
  backlog, then answers; a report (or backlog batch) stamped with the old folder's state can be taken
  before that answer, and a tick in between can evaluate it: a spurious "pulih" + reopen pair and a green
  gate row for at most one check interval. The Seq check drops older reports that arrive after the
  answer. Can wait; the full fix is the generation echo.
- Two quick saves: see C-3.
- Unchecked integers: see C-4.

### 5. Emergency promote — not a defect

- Tick before any store write: promote.go:205, for `p.Mode == emergency` and for `p.Mode == ""` with an
  emergency view. Before it only `PromoteView` runs (CheckNow, `mysql.binlogTail`, `mysql.probeSource`:
  reads). Deferred minor "tick checked after PromoteView": can wait.
- Nothing else new refuses an emergency: the gate call is inside `if mode == planned`; the handler's
  `skippedRed` tolerates a gate error (audits `null`); `gateView.Off()` is never true for an emergency.
- Mode agreement: the handler posts `Mode = view.Mode` of its own `PromoteView` and requires the posted
  mode to equal it; `StartPromote` takes a fresh view and refuses when `p.Mode` differs. `PromoteMode`
  only decides *before the token* whether the form is stale; it uses the same function (`promoteMode`) on
  the same Monitor view, so it can differ from `PromoteView` only when a report arrived in between, and
  then the later comparison refuses. No start under a mode other than the one posted and ticked for.

### 6. Migration 0013 and upgrade — C-1; otherwise sound

Three `ADD COLUMN … NOT NULL DEFAULT`, all readers go through `serverColumns`/`GetInventory`; the test
opens a version-12 database with rows twice. Old servers: folder unset → yellow G8u with the tick on
planned promote/rebuild. Old health rows and old agents: no clock, `performance_schema` or backup →
yellow G10b, with Versi agent red; old agents ignore `backup_folder` (no strict decoding anywhere). Open
jobs: running ones resume ungated; waiting/failed ones are judged at Approve/Retry if they still drop
data, after the superseded check. Open alerts: untouched. Old inventories: account `''` → yellow (R30
holds) — and stays so: C-1.

### 7. `backup_stale` — C-2; the rest is correct

Role change and promote: an old master becomes `quarantined`, the rule is no longer judged and the alert
closes with "server menjadi master lama (dikarantina) setelah promote" (not as a recovery); the new
master is judged from its own row (unset → no alert). Server delete: alerts go by cascade, the Monitor
drops the state at the next tick; in between the rule finds no server row and does nothing. Restart with
an alert already open: it stays, unknown keeps it, the first report judges it. No storm: one alert per
master, text changes (hourly at most, R17/R18) do not notify. Telegram text: server name, an age in words,
the limit and a reason from a closed map — bounded, no agent text. Wrong: opening on extrapolated data
(C-2).

## Triage of deferred findings (one line each: fix before merge / can wait / not a defect — reason)

Only items that touch panel core are listed; the others belong to the agent, web or README reviewers.

- Task 2: doc comments of `lateMessage`/`lateCloseReason` describe the old rules — can wait — comments only.
- Task 2: `TestLateRuleDecisions` has no `topologyGone` dimension — can wait — the integration test covers the fact.
- Task 2: `deletableLocked` reads the replica list twice and shadows — can wait — clarity only.
- Task 2: `topologyGone` re-reads; two store reads per tick per open late alert — can wait — a handful of rows.
- Task 2: "master lama" busy label only when the job has no topology id — not a defect — the earlier refusal already names the topology.
- Task 3: wrapped agent error labelled whole in the audit, split in the flash (`agenttext.go`) — can wait — both are bounded and labelled.
- Task 4: three test comments still say "Perketat" — can wait — comments.
- Task 7 note (offline master reads `stale`): built for the gate (R25, tested); **not built for the alert → C-2, fix before merge.**
- Task 7: `JudgeBackup` unchecked integers — can wait, but three lines; do it with C-2 (C-4).
- Task 7: `backup_stale` closing on role change reported as recovery — not a defect — a master leaves through `quarantined`, where the close carries its own reason; no master→replica path exists.
- Task 7: `ruleCtx` reads the server row per master per evaluation under `m.mu` — can wait.
- Task 7: stored account cut at 255 bytes can lose "…" — can wait — the gate compares the user part only.
- Task 7: over-long comment line — can wait.
- Task 7: `BackupMessage` with limit 0 untested — can wait — `backupMaxAgeHours` falls back to 48.
- Task 8: R22 refill window — can wait — bounded by one check interval; full fix needs a generation echo (see item 4).
- Task 8: saving unchanged values forgets the report — can wait — the agent's answer refills it at once when online.
- Task 8: `backupMaxAge` repeated (web, monitor, and a third time in `backupRow` gate.go:573-576) — can wait — export one helper.
- Task 8: last block of `TestConfigureServerForgetsBackup` asserts nothing — can wait — it is one case of a test that asserts plenty; add a `View` check when next touched.
- Task 9: gate.go 632 lines — can wait.
- Task 9: Buat replika target without a health report reads G10b for disk — not a defect — unknown is yellow.
- Task 9 (carried to Task 10): all-offline loop asserts yellow on clock/disk/perf_schema/backup — built (gate_test.go:711-719).
- Task 9: a red multi-server row's Detail also carries yellow sentences — can wait.
- Task 10: role check in `StartCreateReplica` could move before the gate and `RemoveReplica` — can wait — order predates the plan.
- Task 10: `JobGateInput` "not gated" for unreadable params — not a defect — `confirmDrop` still needs the confirmation and `newRun` fails the job before any step.
- Task 10: e2e helper `connectRoot` name; five tests call `greenGate` themselves — can wait.
- Task 12: `skipped_red` read before `StartPromote`'s own view — can wait — a record, not a decision.
- Task 12: tick checked after `PromoteView` — can wait — only read probes are sent; the handler checks first.
- Task 12 (carried to Task 14): emergency with a gate error still starts; mode-moved test — built (4f45283, 78928fc).
- Task 14: README "Keamanan" omits Akun panel and Versi agent — fix before merge — both are agent-reported (item 3); one line (README reviewer).
- Task 14: e2e test 3 not mutation-checked; its folder left behind on a timeout panic — can wait.
- Task 14 (named): two quick saves, configure out of order — C-3: fix with this wave (cheap), not a blocker.

## Findings against rulings

- **R28 (G10b has no fix), with R30:** right in general, wrong for the account row. That row's unknown
  state is not waiting for a report; it waits for an operator click nobody is told about (C-1). The row
  needs its own fix sentence (or the panel should ask the agent itself).
- **R25:** correct, but incomplete: its reason ("an offline master would read `stale` from extrapolation
  alone") applies unchanged to the `backup_stale` rule, which was left judging that case (C-2).
- R5, R6, R7, R15–R18, R22, R24, R26, R27, R29: no finding; built as ruled.

## Declined to judge

- Emergency mode is offered for a healthy master in the seconds between a panel start and its agent's
  reconnect (`promoteMode` reads "not connected" as emergency) — rule of plans 4b/5a, unchanged here; the
  D5 tick is this plan's answer.
- K1 and K2 (candidate with another agent version; unfinished topology job) — owner's decisions.
- `cancelAgentCopy` runs before the gate and outside `startMu` — K9, handed to the audit / plan 5e.
- The agent clamps a future mtime to age 0, so a newest file dated in the future reads fresh until that
  date — agent side.
- A 0-byte newest file opens the alert at once (no hold time), so a scan that lands in the instant a dump
  file is created gives one open/close pair — spec says empty is red; window is a moment per day.
- `dropsData` classification (what counts as "still drops data" on Retry) — predates the plan.
- `Resume` of running jobs after a restart is not judged again — continuation, spec silent.
- `SalvageDelete` is not gated — not in the spec's table.
- Versi agent comes from one probe per connect; a failed probe leaves the stored version stale until the
  next connect — pre-existing source, plan names it "sudah ada".
- Web handler order, audit field shapes (SEC-44), guide wording — web reviewer.

## Assessment (Ready to merge? With fixes)

The gate itself is sound: no bypass, fail-closed, correctly ordered against store writes, locks and the
emergency tick, and well tested. Two gaps an operator will meet should be closed first: the account row
that stays "belum diketahui" on every upgraded server with no way shown to resolve it (C-1), and
`backup_stale` raised from extrapolated data after an agent or panel outage (C-2). Both fixes are a few
lines plus tests; C-3 and C-4 are cheap enough to ride in the same wave.
