# Final review, panel side (head 7ee4051) — findings, verbatim

Verdict: ready to merge with fixes. Critical: none.

## Important

P-I1. **A first old master whose quarantine is still pending is never quarantined after a second emergency promote** — orchestrator/promote.go:1423-1432 (`onOnline`), with spec 5.1 "Promote kedua".
- Path: master A dies; emergency promote A→B defers the quarantine ("dikirim saat agent tersambung lagi"). B dies before A returns; emergency promote B→C is allowed and `SwapMaster` moves the marker to B. A is powered on: `onOnline` finds `QuarantinePending` but `GetTopologyByOldMaster(A)` is not found, and returns.
- Nothing else sends it: the agent applies a quarantine only from `mysql.quarantine` (agent quarantine.go:73).
- Result: A runs writable with role pin `master`, while the panel shows role `quarantined` and a "Master lama (dikarantina sejak…)" box. T5 and T6 say only "Hapus" is left, not that the server was never locked. The T6 branch (server_detail.html:349-355) also drops the box's status block.
- The `onOnline` guard is older than this plan and unchanged by it, but this plan makes the state official and spec 5.1's table assumes the agent pin is `quarantined` there. On the brief's scale this sits at the edge of Critical; rated Important because it needs two failures in a row.
- Fix: in `onOnline`, when the marker is gone, still send `mysql.quarantine` with the current master of the promotion's topology. At the least, T5/T6 and the cutover warning must say the server is not locked and must not be started.

P-I2. **An agent can erase or forge the panel's sentences in the restart wait message** — prepare_master.go:300-317, runner.go:1809-1810, agenttext.go:196.
- `RestartReasons` is placed unlabelled in front of the panel's own sentences on one line, and `setStep` censors the whole line.
- Confirmed in a scratch run: a single `'` in one reason turns the banner into `MySQL di db1 perlu dimulai ulang: log_bin'…`. The downtime sentence, "Sudah saya restart" and the firewall sentence are gone. 20 reasons of 1024 characters do the same through the 4096 cut.
- An unlabelled reason can also carry any sentence that reads as the panel's.
- Limit of the harm: the dialog's `Impact` text is fixed panel text (handlers_jobs.go:219-225), so the operator still reads what a restart costs before approving.
- Fix: panel sentences first, then the reasons labelled (`agentWords`) and bounded in total.

P-I3. **A panel stop between `AddReplica` and `CreateJob` leaves an old master whose rebuild can never start** — create_replica.go:162-195.
- The server is left `quarantined` with a `creating` row and no job. `Resume` fails only jobs; the normalisation touches only role `replica`.
- A new rebuild removes only a `failed` row, so `AddReplica` hits `replicas.server_id UNIQUE` (migration 0003:26) and the operator reads a raw SQLite error. Resync and Lepas are refused. Only Hapus leads out, followed by `set-role` on the server.
- The window is two adjacent store writes, so it is very unlikely. The same state arises if `RemoveReplica` fails in `cancelJob` (runner.go:1344, logged only).
- Fix: for a quarantined target that passed `refuseBusy`, remove the row in any state.

P-I4. **An emergency promote is refused with no pointer while a job of the topology is unfinished** — promote.go:253-257; handlers_promote.go:478-492.
- Spec 5.1 says "Promote darurat tidak pernah ditolak"; plan note 17 records the refusal for the owner.
- The GET screen shows T5 and no blocker. After typing the name and password the operator reads "topologi ini masih punya pekerjaan yang belum selesai", with no job named and no link.
- It is not a dead end: the operator opens the old master's page (T22, "Lihat pekerjaan"), presses Batalkan on the job, and the emergency promote then starts.
- Smallest fix: set `Blocked`/`BlockedJob` on the screen from `ActiveJobForTopology` and add "batalkan pekerjaan itu dulu" to the text.

## Minor

P-M1. agenttext.go:231-234: the `nameText` comment says backticks protect a name from `redact.Quotes`. They do not; printable names are shown raw (`x', simrs, lab` → `x'…` in the banner and log).
P-M2. store/jobs.go:209: `ListJobsForServer` orders by `created_at DESC, id DESC` with one-second precision and random IDs. `latestRebuildSettled` and `cancelAgentCopy` take "the newest create_replica job" from it. The "older flagged, newer unflagged" case of `TestNormalizeLegacyFailedRebuilds` passes only because its IDs sort that way. Order by rowid, as `NewerJobExists` does.
P-M3. handlers_oldmaster.go:644-657: the delete dialog says the server "tetap dikarantina" even when the box shows T3/T3b (agent reports another role).
P-M4. controls.go:288-295: deleting the new master is refused with "lepas atau hapus semua replikanya dulu" when the only row is the quarantined old master's.
P-M5. monitor/alerts.go: `noTopologyCloseReason` and T25 say "sudah promote lagi" also when the topology was deleted.
P-M6. start_state.go: once a late alert is rewritten to T1b it keeps that text after the agent can verify again.
P-M7. monitor/state.go:514-531: `replicasDisconnected` still counts a quarantined server's row.

## Deferred-list triage (panel side)

| Line | Verified at head | Bin | Decision |
|---|---|---|---|
| Task 13 emergency promote refused while a topology job is unfinished | True (promote.go:253) | Owner decides | Keep refusing: the rebuild is never cut off silently. Auto-cancel on emergency: one step fewer in an outage, but a destructive side effect. Reviewer would keep the refusal and fix the text and link before merge (P-I4). |
| Task 13 busy loop iterates `ListReplicas`, label "replika" | True, but unreachable for a quarantined server today | Can wait | Decide with the line above in 5b-2b. |
| Task 14 `RestartReasons` unlabelled; `agentList` total length | True, and worse than recorded: one quote is enough | Fix before merge | P-I2. |
| Task 14 quote in a database name hides later names | True for the banner, step message and job log. Not for the approval: the dialog lists every name raw | Can wait | Correct the `nameText` comment now; exempt names from censoring in 5b-2b. |
| Task 14 `nameText` collisions, space collapse | True | Can wait | Same dialog list; such names need a deliberate operator. |
| Task 14 agent answers with code `offline` | True | Can wait | No new power: the agent can disconnect instead. |
| Task 12 `replicasDisconnected` | True | Can wait | Inflates a count in a detail text only. |
| Task 11 `cancelAgentCopy` outside `startMu`, before the checks | True | Can wait | Worst case a stale copy of a failed job is cancelled; Retry copies again. |

All other lines of Tasks 11 to 15: can wait, harmless (test gaps, comments, or items already assigned to the 5b-2b audit).

## Declined to judge

- Agent internals (answering `configureReplication` OK again, `rebuildApproval` under role `replica`): the other reviewer's area.
- Health-report fields on monitoring pages and the unlabelled audit outcome text: assigned to the 5b-2b audit.
- `dropReplUserHint` building a DROP USER hint from the agent's refusal: older than this diff.
- Alert-table growth when a hostile agent reopens `master_start_late` in the CopyStarted or no-topology state: Telegram bounded by the existing flap control; table growth as for any flapping alert.
- Session, CSRF and `requireConfirm` machinery: unchanged by this diff; checked only that changed handlers still go through it.
- Letter-for-letter comparison of T-texts with spec 5.8: not done (Task 15's reviewer did it).
- `web/static/app.css`, README and the plan's measurement sections: out of scope.
- Flakiness of `TestNotifierWiredToMonitor`: not run.
