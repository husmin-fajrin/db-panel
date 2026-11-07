# Final fix wave — Plan 4b (after final review / security audit)

Commits (not pushed):
- 4d82517 fix(agent): emergency promote refuses a live old master; guard checks replicas before unlocking; salvage scope and caps
- 1b4a16a fix(panel): emergency refused while a lock is pending; cutover shows admin-account sessions; error text, lock margin, old_master_server_id, docs

## Per finding

### SEC-66 (a) agent, emergency mode
- `internal/agent/drivers/mysql/promote.go`: the emergency→planned switch is gone. `verifyOldSource` refuses an emergency promote whose old master answers any of the probes (nothing changed yet); the final probe in `openWrites` restores the replica (marker removed, START REPLICA) and refuses when the old master answers, read-only or writable. One message (`oldMasterAlive`): old master alive (terkunci atau tidak), not an emergency, use planned promote, or `sudo dbpanel agent unlock-master` on the master. Package comment updated.
- Tests (promote_test.go): `TestPromoteEmergencyRefusedWhenMasterReturns` now has subtests writable/locked (4 probes, replica restored, marker removed); `TestPromoteEmergencyRefusedWhenMasterAnswers` covers writable and locked (readPeer never called, no "dilanjutkan sebagai promote terencana").
- Also: `web/templates/promote.html` and README no longer advise "lock-master then emergency runs as planned" (that path no longer exists); test text updated.

### SEC-66 (b) agent guard
- `promote_lock.go` `guardExpired`: after the candidate is read-only at both reads, `writableSeenReplica` reads every host in replicas-seen (except the candidate's lock address) with the lock's credential at the candidate's port; a writable one whose gtid_executed holds this server's UUID → `selfQuarantine(host:port)` (emits `monitor.quarantined`) instead of unlocking. A seen file that cannot be read keeps the lock (kept event, retried each minute). Unreadable replicas are passed over; no writable one → unlock as before.
- `lockGrace` now is `proto.PromoteLockGrace` (shared with the panel).
- Test `TestGuardChecksSeenReplicasBeforeUnlock` (real mysqld as the other replica; candidate faked): writable without the UUID → unlocked; relocked, other replica given the UUID in gtid_purged → quarantined under 127.0.0.1:<port>, PERSISTed read-only, lock removed, events lockExpired+quarantined.
- Limitation (documented in README): replicas are read at the candidate's port with the candidate's account; replicas on another port or refusing that account are skipped; the pin's source name is the processlist host form (IP or name), which may differ from how the panel names that server (rebuild then needs `set-source`).

### SEC-66 (c) panel
- `orchestrator/promote.go` `refuseWhileLockMayStand` (called in StartPromote for emergency mode): latest promotion of the topology failed, same old master, planned job, lock tried and unlock not confirmed → refused until deadline + `proto.PromoteLockGrace` (60 s, the guard's grace). Deadline: job's LockDeadline; else LockTriedAt + 1800 s + 5 min call slack; else promotion FinishedAt + 1800 s. Message gives the time ("mulai DD-MM-YYYY HH:MM:SS") and names `sudo dbpanel agent unlock-master` and Ulangi.
- jobState gained `lock_tried_at` and `lock_unlocked` (set when unlockPromote succeeds or answers "not locked"); no schema change.
- Tests: `TestPromoteEmergencyRefusedWhileLockMayStand` (review sequence: planned to X fails in catch-up, unlock fails offline, master offline → emergency to Y refused; still refused inside the grace; accepted and succeeds after deadline+grace), `TestPromoteEmergencyAllowedAfterConfirmedUnlock`.

### SEC-67 connections
- `connections.go`: hides CONNECTION_ID() and the agent account's sessions from '', localhost, 127.0.0.1, ::1, ::ffff:127.0.0.1; other sessions of that account are returned with `ConnectionRow.AdminAccount=true`.
- Panel: `_connections.html` badge "akun admin panel"; `cutover.html` states exactly what is hidden; rebuild warning counts and names admin-account sessions (`connectionsView.AdminTotal`). README "Hubungkan MySQL" recommends a dedicated admin account (example GRANT), no longer root, and says what the screen hides; server page hint updated the same way.
- Tests: `TestConnectionsHidesAgentAccount` (mysqld bound to 0.0.0.0 with skip-name-resolve; root via socket and via 127.0.0.1 hidden, root from the host's non-loopback IP shown with AdminAccount, ops shown; skips only if no non-loopback IPv4 — it ran here); web: cutover page/rebuild warning tests.

### SEC-68 — `ReadPeer` unverified uses `skip-verify`. `TestReadPeerRequiresTLS` (mysqld with tls-version= → unreachable with a TLS error).
### SEC-69 — `GuardTick` uses `promoteMu.TryLock`, Debug log, skip. `TestGuardTickSkipsWhileBusy`.
### SEC-70 — salvage.go: set must be a GTID_SUBSET of the last mysql.leftover set (memory + `<salvage dir>/leftover-gtid`, 0600, temp+rename, read back after restart; O_NOFOLLOW, regular file, parsed); one run at a time (`salvageRun.TryLock`, "salvage lain sedang berjalan…"); directory cap 8 GiB: refused before writing when full, writer bounded by remaining room (stopped + file removed, ParamError) so the cap is never passed. Tests: `TestSalvageBoundedByLeftover` (refused before leftover, superset refused, leftover set accepted, file 0600, new Driver reads it back), `TestSalvageOneAtATime`, `TestSalvageDirectoryCap` (sparse file fills dir). Existing salvage tests write the leftover file. `newPinRig` now uses temp SeenPath/SalvageDir.
  Note: "refuse before writing when the cap would be exceeded" is implemented as refuse-when-full plus a bounded writer, since the output size is unknown before mysqlbinlog runs.
### SEC-72 — `agentFailureAs(err, generic, offline)`; repoint page+audit and promote view page+audit use fixed sentences (ErrNeedsRebuild kept as is). Tests in web_oldmaster_test / web_promote_test (raw text absent from page and audit).
### SEC-73 — `PromoteParams.LockRemainingSec` (panel computes at send time); agent `promoteLockBy` = receive time (agent clock) + remaining, margin checked against agent clock; LockDeadline only logged (fallback if absent). Tests `TestPromoteLockMarginOnOwnClock` (agent), `TestPromoteSendsLockRemaining` (panel).
### C-1 — `checkResume` with a valid marker stops running replication and resumes; refusal messages reworded ("Bila tidak ada aplikasi yang menulis ke server ini, kunci kembali…"). `TestPromoteResumeStopsRestartedReplication`.
### C-2 — `Store.ClearOldMaster`; called in `replicaVerify` for RebuildOldMaster; `DeleteServer` clears it in its transaction. OnOnline's needs_repoint path now checks for a promotion instead of `OldMasterServerID != 0` (it would otherwise stop repointing after a rebuild). Tests `TestRebuiltOldMasterIsNoLongerOldMaster` (full rebuild via fakes, later failed attempt not re-quarantined), `TestDeleteServerClearsOldMaster`.
### C-3 — README Keamanan: the two §8 bullets, compromised panel wiping leftover data via rebuild/dropDatabases, salvage bounds, TLS-required reads, guard replica-check limitation.
### SEC-71 — unchanged as ruled.

## Commands and results
- go vet ./... — clean; gofmt -l — clean; bash scripts/tailwind.sh — ran (app.css unchanged; classes already present); graphify update . — done (graphify-out is gitignored).
- go test -race -timeout 40m ./internal/panel/... (includes all panel e2e):
  ok panel 711.957s, agentapi 40.9s, auth 20.8s, ca 1.2s, hub 1.3s, monitor 30.6s, notify 13.3s, orchestrator 57.0s, store 15.9s, web 64.0s.
- Promote e2e under -race separately: TestE2EPromoteEmergency ok (package 136.3s); TestE2EPromotePlanned PASS 130.14s.
- go test -timeout 40m ./internal/agent/... (mysql driver split by test name to stay under the tool limit):
  agent 12.9s, commands, config, testmysql 17.5s, jobs, monitor, probe ok; drivers/mysql ^Test[A-D] 505.4s, ^Test[E-O] 288.7s, ^TestP 466.7s, ^Test[Q-Z] 682.7s — all ok.

## Not done / notes
- Spec text (§5.1 step 2/4, §5.2 promote screen) still describes emergency→planned; left unchanged (design doc), README and UI updated.
- The panel refusal ignores clock skew between panel and master (ruling: deadline + grace).
