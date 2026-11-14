# Design note G — the state after a failed rebuild of an old master

Read-only analysis of head 6799dc3 (`git show 6799dc3:<path>`). Nothing was run or edited. Line numbers are at 6799dc3.

Notation:
- X: the old master.
- T: its topology.
- N: the new master.
- "Row": X's row in `replicas`.
- "Q+row": `servers.role = quarantined` and a failed row together.
- "Legacy state": `servers.role = replica` + failed row + `topologies.old_master_server_id = X`. This is what every failed rebuild produces today.

## 0. Facts the design rests on

**F1. Steps of `create_replica`.** They are (`create_replica.go:57-60`, `createReplicaSteps` :299-313): Periksa master, Periksa server tujuan, Pasang MySQL, Siapkan replika, **Salin data**, Pasang sertifikat, **Sambungkan replikasi**, Verifikasi.
- The old master's data is first touched in **Salin data** (`replicaSnapshot` :701). Before the dump it sends `mysql.dropDatabases` for the WipeList (:742); then it sends `mysql.dumpFrom` or `mysql.cloneFrom`.
- Part C's measurement: `dumpFrom` resets the old master's binary logs.
- So from the start of "Salin data", what Periksa / Amankan data tertinggal read (binlog, GTIDs) may already be gone.

**F2. Where the quarantine ends.** The agent ends it only inside `mysql.configureReplication` (`settleRebuilt`, `quarantine.go:444-500`), as the call's last act.
- Every error return of that call leaves the pin `quarantined`: A6, C-1 listing, C-6 no record, "pin peran replica gagal ditulis".
- A success means one of two things: the agent settled, or it was not quarantined under that source at all.
- That call is NOT the end of a step. `replicaConnect` (`create_replica.go:906-968`) runs `mysql.status`, `replAccount`, `trustCA`, then **`configureReplication` (:941)**, then `startReplica` (:947) and `awaitVerifiedLink` (:952).
- So a failure "at Sambungkan replikasi" can come before or after the settle. The step index alone cannot tell them apart.

**F3. What failure and cancel do today.**
- `fail()` (`runner.go:1538-1572`) only sets the row to failed. The role stays `replica`, as `AddReplica` set it (`topologies.go:237-287`).
- `Resume` (`runner.go:1023-1075`) fails jobs cut off by a panel stop the same way.
- `cancelJob` calls `keepQuarantined` (`create_replica.go:227-236`). It removes the row and sets the role back to `quarantined` unconditionally for a rebuild, and skips `releaseRole`.

**F4. A new rebuild from Q+row works today.** `StartCreateReplica` (`create_replica.go:150-168`) removes the failed row (`RemoveReplica` makes the role unassigned) and then sets the role to `quarantined` when X is still the old master. `AddReplica` accepts role `quarantined`.

**F5. Retry refuses a role change.** `supersededBy` (`runner.go:935-990`) refuses a seeding job whose server role is not `replica` ("peran server ini sudah berubah"). The job page hides "Ulangi" when `Superseded` is non-empty. `Approve` calls the same check (`runner.go:~801`).

**F6. The monitor resolves the row first.** `resolve` (`monitor/state.go:269-308`) reads the topology by master, then **the replica row**, and only when there is no row the server role (quarantined → `resolveQuarantined`).
- So Q+row would still be monitored as a failed replica: "Replika belum siap dipantau".
- The old-master rules stay silent: `master_start_unguarded` (`alerts.go:355`) and the start-state reconciliation (`start_state.go:130`).

**F7. The TLS code already anticipates Q+row.** Tighten (`tls.go:527-531`, "A quarantined old master (its rebuild failed)") and `TestTightenTLSLeavesOutQuarantined` already handle role quarantined + failed row. `StartMySQLTLS` (:65), the daily renewal (:108) and `refuseQuarantinedTLS` (:797) refuse or skip role quarantined.

**F8. Deleting a server.** `replicas.server_id` has `ON DELETE CASCADE` (`0003_mysql_topologies_jobs.sql`), and the store opens SQLite with `foreign_keys(1)` (`store.go:47`). `deletableLocked` (`controls.go:245-296`) still refuses any server that has a row (`ErrServerIsReplica`), whatever its role.

**F9. Any new job supersedes the failed rebuild.** Periksa / Amankan data tertinggal create a new job on X (`oldmaster.go:121-160`). `NewerJobExists` then makes the failed rebuild job superseded: its "Ulangi" disappears. The rebuild form remains the way forward.

---

## 1. Design B — a failed rebuild of an old master leaves it quarantined

### 1.1 The rule

At the moment a rebuild job of an old master ends without success (fail, Resume after a panel stop):

- **(B-i) The agent's quarantine cannot have ended:** no successful `configureReplication` in this job. Role becomes `quarantined`, the failed row stays (for Retry).
- **(B-ii) It ended:** `configureReplication` answered OK. X is an ordinary failed replica. Its old-master marker is cleared at that moment (see 1.3).

Inside (B-i), one more fact is needed for the box and the form: whether the job reached "Salin data". Call these (B-i-a) data intact and (B-i-b) copy started.

### 1.2 How the panel knows the boundary (no guess)

- **New `jobState` field `QuarantineEnded bool`.** Set in `replicaConnect` immediately after `mysql.configureReplication` returns nil, when `st.RebuildOldMaster` is true, and saved with `jr.save()` before `startReplica`.
  - This is the only point where the agent tells the panel it settled (F2).
  - A panel crash between the agent's answer and `save()` leaves it false. That is the conservative direction: shown as quarantined. A Retry of the connect step calls `configureReplication` again; the agent answers OK, because `settleRebuilt` returns nil when it is no longer quarantined under that source; the flag is then set. The state heals itself.
- **The agent's snapshot pin (`HealthSnapshot.RolePin`) is not used to decide.** It can be up to one check interval stale, and it comes from the same agent. Use it at most as a display cross-check, for example a warning when the flag says ended but the last snapshot still says `quarantined`. The decision itself is the flag. Every error leaves it false, which is the safe side.
- **Copy started (B-i-b):** the failed job's `CurrentStep >= index of "Salin data"` in its stored steps, found by name like `connectIndex`, because stored steps may lack the certificate step. This is known from the job row alone.

### 1.3 Every place that must change (file:function)

**Orchestrator**

1. `runner.go: (jr *jobRun).fail` and `runner.go: (r *Runner).Resume`. Call one new helper, `endRebuildAttempt(job, st, failedStep)`:
   - for `KindCreateReplica` with `st.RebuildOldMaster` and `!st.QuarantineEnded`: `SetServerRole(X, quarantined)` after the row is set to failed. The row is kept, so Retry stays possible.
   - Both callers then call `replicaChanged(X)` so the monitor re-resolves (as it does for the state change).
2. `create_replica.go: (jr *jobRun).replicaConnect`, after `configureReplication` succeeds on a rebuild:
   - set `st.QuarantineEnded = true`, `jr.save()`;
   - `ClearOldMaster(X)` at that moment, not only in `replicaVerify`. This is what makes (B-ii) an ordinary failed replica: `AwaitingRebuild` is false, the rebuild form no longer accepts X, Resync and Lepas apply, and the box is gone.
   - log one line (text in §4).
   - `replicaVerify`'s own `ClearOldMaster` stays; it is idempotent.
3. `create_replica.go: (r *Runner).keepQuarantined` and `runner.go: cancelJob`. A cancel after `QuarantineEnded` must NOT set `quarantined`: the agent's pin is `replica`. Treat it as an ordinary `create_replica` cancel (reset replication, `RemoveReplica`, send `releaseRole`). Owner decision: today a cancel after the settle already sets `quarantined` while the agent is `replica`, so this case is wrong today too.
4. `runner.go: supersededBy`, seeding branch. Today it rejects `srv.Role != replica`. New rule: a rebuild job whose server is `quarantined`, with its own failed row in the same topology, is NOT superseded. It must be narrow:
   - job kind `create_replica`;
   - `st.RebuildOldMaster` and `!st.QuarantineEnded`;
   - row state failed;
   - `GetTopologyByOldMaster(X).ID == j.TopologyID`.

   The SEC-31 checks that follow (master changed, newer job) stay.
5. `runner.go: Retry` (and `Approve`, see Tests). After `refuseSuperseded` and `confirmDrop`, under `startMu`: if this is a rebuild job and the role is `quarantined`, `SetServerRole(X, replica)` together with `ReopenJob`. On `launch` failure, put `quarantined` back next to the existing re-fail.
   - The approval gate (`rebuildApprovalForJob`) and `retryConnectionsClear` run before this, as today.
   - Retry of a superseded job (a newer rebuild, a Periksa job, a TLS job) is refused by `NewerJobExists` before any role change. The role stays `quarantined` and the operator uses the form.
6. `approval.go: AwaitingRebuild`.
   - New failures give role `quarantined`; this matches the first arm.
   - The second arm (role replica + failed + old master) is needed only for rows from before this change: the legacy state. Proposal in §1.5.
7. `approval.go: refuseAwaitingRebuild`. Today it returns nil for role `quarantined` (:119). With B, Q+row exists, and on it:
   - `StartResync` would start a resync: the row is found and state failed is accepted;
   - `DetachReplica` would `RemoveReplica` and leave the role `unassigned`.

   It must refuse whenever `AwaitingRebuild` is true, whatever the role. This also closes the reviewer's out-of-scope note.
8. `controls.go: deletableLocked`. With role `quarantined`, a failed row must not block the delete (`ErrServerIsReplica`). The row goes with the server (F8). The cutover and pending guards and "no `releaseRole` for quarantined" are unchanged. This closes N-B2.
9. New helper `approval.go` (or `oldmaster.go`): `RebuildCopyStarted(st, serverID) (bool, error)`.
   - Rule: the server's latest job is a rebuild `create_replica` that ended failed, partial or cancelled with `CurrentStep >= "Salin data"`.
   - It also applies to a cancelled rebuild after the copy (`keepQuarantined`). Today that case offers Periksa data tertinggal on a server whose binlogs a dump has reset, which could answer a false "Tidak ada data tertinggal". The hole predates this round.
   - Used by the web box, the rebuild form, and `StartLeftover`/`StartSalvage`, which refuse with the same sentence when it is true (§4).

**Monitor**

10. `monitor/state.go: resolve`. After the master check, read the server; if its role is `quarantined`, call `resolveQuarantined` whether or not a row exists. Otherwise keep today's order.
    - Without this, B leaves R-1 open (F6).
    - Effects in Q+row:
      - `master_start_unguarded` and the start-state reconciliation apply;
      - status is `DeriveQuarantined`;
      - replica rules close with `inactiveReason` ("server menjadi master lama (dikarantina) setelah promote");
      - the SP-2 arm (`monitor/promote.go:732`, role replica + not judged) is no longer reached for new failures and stays for the running rebuild.
    - Owner decision: addendum 3 said "monitor not changed in this wave". B without this change still closes N-B1 to N-B4 but not R-1.

**Web**

11. `handlers_oldmaster.go: oldMasterViews`. Today `RebuildFailed = role != quarantined`. Replace it with `CopyStarted = RebuildCopyStarted(X)`.
    - The template hides Periksa/Amankan only when `CopyStarted`, and shows one sentence (§4) instead.
    - Hapus is shown again: delete now works (item 8).
    - The "Server ini adalah replika." banner guard stays for the legacy state.
12. `handlers_oldmaster.go: rebuildTarget` warnings (:954, :957). When `CopyStarted`, replace "Data tertinggal belum diperiksa … Jalankan Periksa …" and the start-late sentence's "Jalankan Periksa data tertinggal (dan Amankan bila ada) … dulu" with the copy-started sentence (§4). Otherwise unchanged; in (B-i-a) they are true again. This closes N-B3.
13. `handlers_mysql.go: replicaForm` target list (:592-601). It lists `unassigned` and failed `replica` rows. X in Q+row has role `quarantined`, so it is already skipped. For the legacy state, also skip `AwaitingRebuild`. This closes N-B4.
14. Templates: `server_detail.html` (box flag), plus the README (§4).

### 1.4 Every place that reads the role or the row and sees Q+row

| Place (6799dc3) | What it does with Q+row today | Verdict under B |
|---|---|---|
| `store.AddReplica` (topologies.go:254) | refuses only master/replica roles; Q is fine | right (a new rebuild re-adds) |
| `store.RemoveReplica` (:342) | deletes the row, sets **unassigned** | right only inside `StartCreateReplica` (which then sets Q); wrong for detach, hence item 7 |
| `store.SwapMaster` (:405-435) | a second promote overwrites `old_master_server_id` | **needs decision** (R-3, §3) |
| `store.ListReplicas` users: dashboard `topologyPanels` (handlers_servers.go:341), replication page (handlers_replication.go:50), master page (handlers_mysql.go:453) | X listed as a replica card of T; status from `statusOf(X, "quarantined")` | after item 10 the card reads "Master lama setelah promote; dikarantina …" inside T's replica list. Acceptable (true) but **needs decision**: keep, or skip Q rows in replica cards and the lag chart. Today the card says "Replika belum siap dipantau" |
| `monitor.resolve` (state.go:269) | resolved as a failed replica (F6) | **wrong**: item 10 |
| `monitor` `master_start_unguarded` (alerts.go:355), `start_state.go:130`, `quarantinedFromStore` (promote.go:301), promote notice reasons (promote.go:499), `ruleApplies` (alerts.go:529) | not reached (`v.Role` = replica) | right after item 10 |
| `monitor.configure` hint (monitor.go:372) | reads the server role: Q gives hint "" | right |
| `monitor.syncTLSVerified` (monitor.go:543) | row not ready: nothing | right |
| `monitor/weekly.go:87` | after item 10, X has role Q: left out of the weekly report | right (old masters are left out today) |
| `orchestrator.topologyOf` (runner.go:358) | row gives T | right |
| `refuseBusy` | jobs only | right |
| `promote.go:1350` (pending-quarantine resend on reconnect) | role Q, promotion not pending: returns | right |
| `PromoteView` candidates (promote_view.go:120-300) | X a candidate row with "Replika gagal" (and start lock present) | right |
| promote repoint step (promote.go:782-806) | skips failed rows with a log line | right |
| `StartPromote` itself (promote.go:142) | allowed while X is Q+row; then `SwapMaster` drops X's old-master record | **needs decision** (R-3) |
| `RepointNow` (promote.go:1257) | needs `needs_repoint`: refused | right |
| `StartResync` (resync.go:51) | **allowed today for role Q** (state failed accepted) | **wrong**: item 7 refuses |
| `DetachReplica` / `detachable` (controls.go:111-157) | role Q passes `refuseAwaitingRebuild` (nil for Q); `RemoveReplica` then makes it **unassigned** | **wrong**: item 7 refuses (B needs no detach: Hapus and rebuild are the exits) |
| `controllableReplica` (stop/start/relock), `changeableReplica` (add/remove databases) | state failed: refused | right |
| `deletableLocked` (controls.go:245) | Q passes the cutover/pending checks, then **`ErrServerIsReplica`** | **wrong**: item 8 |
| `StartCreateReplica`, rebuild (create_replica.go:150-185) | failed row removed, Q set, `AddReplica` | right (F4) |
| `StartCreateReplica`, not rebuild | D-10 refusal only for role replica; for Q the row is removed first, then "pakai Bangun ulang" | the row removal would kill the Retry: item 7 (refuse on `AwaitingRebuild`, any role) |
| `oldMasterOf` / `StartLeftover` / `StartSalvage` (oldmaster.go:96-160) | role Q accepted | right in (B-i-a); **wrong in (B-i-b)** (reset binlogs): item 9 refusal. Note F9: it supersedes the failed job |
| `StartPrepareMaster` (prepare_master.go:43-50) | Q refused | right |
| `StartMySQLTLS` (tls.go:65), `RenewMySQLTLS` (tls.go:108), `refuseQuarantinedTLS` (:797) | Q refused / skipped | right; closes R-5 for (B-i) |
| `StartTightenTLS` (tls.go:494-531), `tightenSkip` (:560) | busy first, then Q left out; designed for Q+row (F7) | right |
| `Retry` / `Approve` / `supersededBy` (runner.go:845-990) | role not replica: superseded | **wrong**: items 4 and 5 |
| `cancelJob` / `keepQuarantined` | a running job never has Q (Retry sets replica) | right; item 3 for after the settle |
| `Resume` (runner.go:1023) | role untouched | item 1 |
| web `rebuildTarget`, `oldMasterViews`, `connectionsOf`, `rebuildURL` (D-9) | `AwaitingRebuild` true for Q | right; items 11 and 12 for the copy-started wording |
| web `oldMasterFromPath` (handlers_oldmaster.go:580) | role Q accepted | right (item 9 refuses in the orchestrator in (B-i-b)) |
| web server page `page-actions` (server_detail.html:19-23) | role Q: no "Buka halaman replika"; delete via the box | right |
| web `showError` (handlers_controls.go:356) | role Q: server page | right |
| web replica page `/replicas/X` (`replicaDetail`, still reachable through the row) | `RebuildURL` line shown, Resync and Lepas hidden (D-9) | right; it stays |
| web job page `CanRetry`/`Superseded`/`RebuildURL` (handlers_jobs.go:300-345) | superseded today (role not replica): no Ulangi | right after item 4 |
| web `prepareMaster` guard (handlers_mysql.go:238), `CanPrepare` (handlers_servers.go:723) | Q refused / not offered | right |
| web `replicaForm` target list (handlers_mysql.go:592) | lists only unassigned and replica roles: Q skipped | right (item 13 is for legacy) |
| web delete handler (handlers_servers.go:843-860) | Q: `unquarantined` option, warnings D1/D1b | right after item 8 |
| web confirm `ActRemoveDatabases` (handlers_confirm.go:267) | row-based page rendering only | right |
| audit | the job/replica audit entries do not read the role; the Retry role flips are internal (no audit entry). Owner may want a log line | right |

### 1.5 Rows from before the change (legacy state)

They exist on any panel where a rebuild of an old master has failed: the rebuild feature predates this branch. Proposal:
- **One-time conversion in `Resume`**, which already runs before any job goroutine. For each server with role `replica`, row failed, and old master of a topology, whose latest job is a failed rebuild:
  - `CurrentStep < "Sambungkan replikasi"`: set role `quarantined`. This is certain (F2).
  - `CurrentStep > "Sambungkan replikasi"` (Verifikasi): `ClearOldMaster`, so it becomes an ordinary failed replica. Verify only runs after connect succeeded.
  - `CurrentStep == "Sambungkan replikasi"`: ambiguous. If the job log has "Replikasi berjalan:" (logged only after `startReplica` succeeded, hence after the settle), treat it as ended. Otherwise leave it in the legacy state.
- **Keep the second arm of `AwaitingRebuild`** for that remaining ambiguous case, with all D-9/D-10 behaviour as now, plus the target-list skip (item 13).
- Alternative: no conversion, keep the arm for all legacy rows. Simpler, but the reviewer's N-B2 and N-B3 holes then stay for exactly those rows.

### 1.6 Lifecycle of the failed row under B

- **New rebuild starts:** removed and re-added (F4). The old job is superseded (`NewerJobExists`); its page hides Ulangi and the D-9 line (`CanRetry` false).
- **Retry:** the same row, state back to creating (`run`, runner.go:1494), role back to replica (item 5).
- **Server deleted:** it goes with the server (cascade, item 8). The old-master marker goes as today (TestRebuiltOldMasterIsNoLongerOldMaster covers delete).
- **Operator detaches:** refused (item 7). Lepas is not needed as an exit in B.
- **Periksa / Amankan / TLS job on X:** the row stays failed, and the failed job becomes superseded (F9). A new rebuild is the way forward; the README must say so (§4).
- **Second promote of T** (R-3): the row stays in T; `old_master_server_id` points to the new old master; X keeps role `quarantined` and leaves `AwaitingRebuild` (no topology) and the rebuild form. Pre-existing; needs a decision.

### 1.7 D-9 lines and D-10 refusals under B

- **D-9 predicate:** stays (one definition). For new failures only the first arm matters; the second arm is for legacy rows.
- **D-9 box:** `RebuildFailed` becomes `CopyStarted`. Hapus shown again; Periksa/Amankan hidden only after the copy started.
- **D-9 replica-page line and job-page line:** stay, unchanged. They are true in (B-i), and in (B-ii) `AwaitingRebuild` is false so they do not show.
- **D-10 refusals and `ErrAwaitingRebuild` text:** stay. The check widens to `AwaitingRebuild` for any role (item 7). "masih dikarantina" is true in every state where it can now appear: (B-i) and legacy. This closes N-B1 together with item 2.

### 1.8 Existing tests whose expectation changes under B

- `orchestrator/approval_test.go`:
  - `TestRebuildAgainAfterFailedRebuild`: role after the failed rebuild is `quarantined`, not `replica`.
  - `TestFailedRebuildRefusesResyncAndDetach`: `unchanged()` expects `quarantined`. Its ordinary-replica half stays.
  - `TestRebuildRetryAndApproveRecheckApproval`: the failed job leaves role Q. "Approved again: the retry runs" now needs items 4 and 5. The hand-made waiting job (`ReopenJob` + `UpdateJob` waiting) has role Q, so `Approve` must also accept it (item 4 rule) and set role replica (item 5 applied to Approve), or the test must set the role.
  - `TestRebuildApprovalUnusableAnswers`: the retry part (refused before any role change) is unchanged.
- `orchestrator/runner_test.go` / `create_replica_test.go`: any test asserting `RoleReplica` after a failed `create_replica` rebuild, or the Resume path for a rebuild job (role after a panel restart).
- `orchestrator/promote_test.go`: `TestRebuiltOldMasterIsNoLongerOldMaster` is unchanged. A test that a cancelled rebuild after the settle is not set to Q is new (item 3).
- `web/web_failed_rebuild_test.go`:
  - `failedRebuildEnv` builds the legacy state. Keep it for the legacy arm, and add a Q+row env.
  - `TestFailedRebuildOffersRebuildAgain` (no Periksa/Hapus) holds only for legacy and (B-i-b); in (B-i-a) Periksa, Amankan and Hapus are offered.
- `web/web_oldmaster_test.go`: box tests that assume `role quarantined` implies no row (likely none). Delete tests with a failed row (`ErrServerIsReplica` gone for Q).
- `monitor` tests around SP-2 (start_late kept while a rebuild is creating/failed) and any test that resolves a server with a row as a replica regardless of role.
- **New tests, roughly 14-18:**
  - fail before copy, at copy, and at connect before vs after `configureReplication` (role, row, flag, old-master marker);
  - Resume of each;
  - Retry from Q (role flip, launch failure restores Q);
  - superseded by a new rebuild, by a Periksa job, and by a TLS job;
  - delete of Q+row;
  - resync/detach refusals for Q+row;
  - `StartLeftover` refused when copy started;
  - monitor resolve of Q+row (alerts raised);
  - web box in (B-i-a), (B-i-b) and (B-ii);
  - rebuild form warnings in (B-i-b);
  - legacy conversion in Resume (three cases).

---

## 2. Alternative A — keep the state, give it a designed exit

The reviewer's option (a): for an `AwaitingRebuild` server with role `replica`, "Lepas replika" (keep) removes the row and sets the role back to `quarantined` in one store transaction, with no agent call.

**Places**
- `store`: a new transaction, e.g. `ReturnToQuarantine(serverID)`. It deletes the row only if it is failed and the server is the old master of the row's topology, and sets role `quarantined`.
- `controls.go: DetachReplica`:
  - for `AwaitingRebuild` with role replica, call that function instead of `RemoveReplica`, with no agent calls;
  - stop mode: refuse as now, or treat as keep, since stopping replication on a quarantined agent needs an approval;
  - role Q + row (from `createJob` undo failure): same.
- Web:
  - the box offers its own action (a button with a token, §4 text) instead of hiding everything;
  - the replica page keeps hiding Resync and offers the same action instead of "Lepas replika", or the same button with the new meaning;
  - target list skip (item 13 of B).
- N-B3 until the operator acts: reword the two rebuild-form warnings in that state.
- N-B1: either neutral wording ("master lama yang bangun ulangnya gagal") or B's `QuarantineEnded` flag + `ClearOldMaster` at settle. Only the flag makes the panel state true.
- Monitor: unchanged. R-1 stays until the operator clicks.
- After the click, X is a plain quarantined old master: Periksa, Amankan, Hapus, rebuild, monitor and TLS refusals all apply. The failed job is superseded ("server ini tidak lagi terdaftar sebagai replika"): no Ulangi, so the A6 and C-1 cheap path is lost after the click.

**Places seeing Q+row:** none new. The click removes the row. The legacy state stays the default after every failure.

**Tests changing:**
- `TestFailedRebuildRefusesResyncAndDetach`: the keep variant now succeeds as "return to quarantine".
- `TestFailedRebuildReplicaPage` / box tests: a button instead of nothing.

**New tests, about 6-8:** store transaction, detach-keep in that state, no agent call, refusal of stop, web button, form warnings, target list.

---

## 3. Recommendation

**Recommend B**, with items 1-14, the monitor change (item 10) put to the owner explicitly, and the legacy conversion of §1.5.

Reasons:
- It matches the orchestrator's own stated intent ("A failed rebuild of an old master leaves it quarantined", `create_replica.go:152`), what cancel already does, and what the TLS code already expects (F7). The panel's role then says what the agent's pin is.
- The safe state is the default after every failure, with no operator click. Under A the broken state stays the default, and its holes (R-1 silent monitor, R-5 TLS, N-B3) are open until someone clicks.
- It keeps "Ulangi dari tahap ini" (the cheap path the README gives for A6 and C-1) and still gives Hapus, Periksa and Amankan.
- It makes the panel true about the settle (N-B1) instead of rewording around it.

Size:
- **B:** about 10 files (runner.go, create_replica.go, approval.go, controls.go, resync.go, oldmaster.go, monitor/state.go, handlers_oldmaster.go, handlers_mysql.go, server_detail.html) plus README. About 14-18 new tests, about 6-8 changed.
- **A:** about 6 files (store, controls.go, handlers_oldmaster.go / handlers_controls.go, server_detail.html, replica_detail.html, handlers_mysql.go) plus README. About 6-8 new tests, about 2-3 changed.

Risks:
- **B:**
  - role flips in fail, Resume and Retry: a missed path leaves Q on a running job or replica on a failed one;
  - `supersededBy` is relaxed, a SEC-31 guard; keep the four conditions of item 4 exact and test the superseded cases;
  - the monitor change touches alert reconciliation for old masters;
  - the legacy conversion runs once on real data;
  - a cancel after the settle changes behaviour (item 3).
- **A:**
  - one more operator action, with new wording to learn;
  - the state the reviewer called a patchwork stays the default;
  - the monitor stays blind (R-1) and TLS stays possible (R-5) until the click;
  - "Ulangi" is lost after the click.

What each closes:

| | N-B1 | N-B2 | N-B3 | N-B4 | R-1 monitor | R-3 second promote | R-5 TLS / renewal hides Ulangi | cancel-after-copy offers Periksa (pre-existing) |
|---|---|---|---|---|---|---|---|---|
| B | closed (flag + `ClearOldMaster` at settle) | closed (delete of Q+row) | closed (Periksa works before copy; true sentence after) | closed (Q not listed; legacy skipped) | closed **only with item 10** | open (decision: refusing a promote would block emergencies; at most a warning on the promote screen) | closed for (B-i) (Q refused/skipped) | closed (item 9) |
| A | only with the flag, else reworded | closed by the new action | closed only after the click; reworded before | closed (skip) | open until the click | open | open until the click | open unless item 9 is added |

---

## 4. Texts and README under B

**New or changed operator texts (proposed):**

1. Box, data-tertinggal part, when `CopyStarted` (replaces the Periksa/Amankan buttons; also the refusal of `StartLeftover`/`StartSalvage` in that state):
   `Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah dihapus atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel.`
2. Rebuild form warning when `CopyStarted` (replaces "Data tertinggal belum diperiksa …" and the "Jalankan Periksa … dulu" tail of the start-late warning):
   `Bangun ulang sebelumnya sudah mulai menyalin data ke server ini; transaksi yang tidak sampai ke master baru tidak bisa lagi diperiksa atau diamankan lewat panel.`
3. Job log line at the settle (info):
   `Karantina master lama berakhir: agent di <server> kini menyimpan peran replika.`
4. Optional job log line at fail time, for (B-i):
   `Server <server> kembali dikarantina (quarantined); mulai lagi Bangun ulang sebagai replika, atau tekan Ulangi dari tahap ini bila salinan data tidak perlu diulang.`

Unchanged:
- `ErrAwaitingRebuild` ("server ini master lama yang masih dikarantina; pakai Bangun ulang sebagai replika");
- the D-9 replica-page and job-page lines;
- the agent texts (A6, C-6, C-1).

**README statements that become false at 6799dc3 + B:**
- "Mulai bangun ulang baru" step 1 (near line 1861): "Kotak **Master lama** tetap tampil (di panel server itu kini tercatat sebagai replika yang gagal)". Under B the server is quarantined again in the panel. Also "dan di halaman replika server itu" is now reachable only by URL: the server page no longer links the replica page for role quarantined.
- D-2 section step 3 and the A6 paragraph: "tekan **Ulangi dari tahap ini** pada pekerjaan itu". This needs the caveat that Ulangi disappears once any other job ran on the server (Periksa / Amankan data tertinggal, a certificate job): then start a new rebuild.
- "Master lama" bullets:
  - "**Hapus server:** … baru tersedia setelah **Sudah saya alihkan**" stays true. A sentence should be added that a failed rebuild returns the server to quarantine, with Hapus, Periksa and Amankan available again; Periksa and Amankan only if the copy had not started.
  - "Pin peran agent baru menjadi `replica` bila salinan … berhasil": add that the panel ends the old master's record at that moment (`ClearOldMaster` at the settle), not only at the end of the job.
- Monitor section (near line 1536): "**Bangun ulang sebagai replika** yang belum selesai menyalin (atau dibatalkan) tidak menutupnya" (start_late) stays true. With item 10, the start-lock and bad-start alerts are raised for a failed-rebuild old master: this is new, and should be said where the README lists when those alerts appear.
- C-6 paragraph: it says Ulangi fails again and to start a new rebuild. Still true.
- If item 3 (cancel after the settle) is adopted: wherever the README says cancelling a rebuild leaves the server quarantined, add "unless the copy was already connected as a replica (the agent ended its quarantine); then it becomes an unassigned server".
