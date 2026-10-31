# Scoped re-review: final fix wave, Plan 2 (ebf03a2..9fa093b)

Reviewer read: final-fix-brief.md, final-fix-report.md, the full review diff, progress.md rulings, and the source lines the diff pointed to (runner.go `launch`/`execute`, agentjob.go `runAgentJob`, store `ActiveJobForServer`, `sourceHost`, `ensureTopology`). Nothing was edited or run apart from this file.

## Verdict

**Residuals: 6 Important, 5 Minor, 0 Critical.**

No Critical issue was found. The three dangerous flows (C1 reconnect, C2 superseded refusal, Q/R dump account and `refuseOnSource`) are implemented as the brief asks. The residuals are edge paths:
- a narrow race in Retry;
- dump-account orphan paths;
- a host-matching regression on the dump path;
- orphaned dump clients after an agent restart (a side effect of `KillMode=process`);
- an incomplete SEC-34 cap;
- the master address no longer being refreshed when Siapkan master runs again.

## Per item

| Item | Status | Evidence / what is missing |
|---|---|---|
| A. C1 | Addressed | `realhost.go` `ReadyCheck` (1045/1698 = up); `conn.go` `LoginRefused`, `notConnectedError`; `clone.go` `CloneFrom` returns `AdminReplaced`; `dump.go` `storedLoginRefused` + `flushTrailer`; `create_replica.go` `replicaSnapshot` → `verifyCopyAfterReconnect`, `reconnectMessage`; tests `TestCloneReplacesAdminAccount`, `TestDumpAllDatabasesReplacesAdminAccount`, `TestCloneAdminReplacedWaitsInSnapshot`, `TestCloneAdminReplacedIncompleteFails`, E2E CLONE case. |
| B. C2 / SEC-31 | Partly | `runner.go` `Approve`/`Retry` hold `startMu` and call `refuseSuperseded`/`supersededBy`; `store.NewerJobExists`; `handlers_jobs.go` hides the buttons; `superseded_test.go`. Missing: (1) the reopen of a failed job happens later in `execute`, outside `startMu` (finding 1); (2) the `"peran server ini sudah berubah"` and `"server ini sudah dihapus"` refusals have no test (brief: "tests for each refusal"). |
| C. C3 | Addressed | `preflight.go` `masterChecks` (server_id and binlog_format are now warn, with the open-connections note); `prepare_master.go` `masterConfigure` logs the note; `TestPreflightMasterReportsFixes`, `TestPrepareMasterMixedBinlogFormat`. |
| D. I1 | Addressed | `handlers_jobs.go` `approvalFor` (per-step labels, `Danger`, `Impact`, `Recheck`); `job_detail.html` daisyUI `<dialog>` + `app.js` `confirmApproval` (CSP-safe, `@click="open"`); `prepare_master.go` `masterRestart`/`pendingRestart`; `TestPrepareMasterRecheckAfterOwnRestart`, `TestJobApprovalLabelledPerStep`. |
| E. I2 | Addressed | `configure.go` `binlogSettings`/`renderConfig` (basename kept, expiry only when 0 or carried over from an earlier block); `TestConfigureMasterSetsGTIDAndWritesBlock` on `mysql-bin`/86400. |
| F. I3 / SEC-29 | Addressed | `status.go` `tlsAvailable`; `replication.go` `sourceStatement` (`SOURCE_SSL=1`, `GET_SOURCE_PUBLIC_KEY=0`); `users.go` `createAccount` `REQUIRE SSL|NONE`; `clone.go` `REQUIRE SSL`; `dump.go` `ssl-mode=REQUIRED`; `server_detail.html` PlainHTTP warning; tests listed in the report. |
| G. I4 / SEC-26 | Addressed | `dump.go` `loadArgs` adds `--binary-mode`; `TestLoadIgnoresClientCommands` (real client, `\!` refused, DELIMITER, routine and trigger loaded). |
| H. I5 | Addressed | `status.go` reads `lower_case_table_names`; `preflight.go` `replicaChecks` fails on a mismatch with instructions; `create_replica.go` passes `MasterLCTN`; `TestPreflightRules`. |
| I. I6 | Addressed | `configure.go` `installClonePlugin` uses `liftReadOnly`; `TestConfigureInstallsClonePluginUnderSuperReadOnly`. |
| J. I7 | Addressed | `create_replica.go` `waitCaughtUp`; `runner.go` `fail` skips `stopReplicaAfter` on `catchUpError`; `verify_test.go` (4 tests, including the thread-error failure path). |
| K. I8 | Addressed | `replica_new.html`, `replica_databases.html`; `TestReplicaFormsNotices`. |
| L. I9 | Addressed (minimal variant) | `handlers_mysql.go` `setMasterSource` + `validHost` + router `authedPost`; `master_detail.html` form; `TestMasterSourceAddressEdit`. See finding 6 for the `masterVerify` side effect. |
| M. I10 | Addressed | `create_replica.go` `refuseUnconnectedTarget`/`ErrTargetNotConnected`; `handlers_mysql.go` `replicaForm`/`createReplica` `NotConnected`; tests listed. |
| N. I11 | Addressed (doc) | README "Replikasi MySQL", agent policy, panel.key backup, §9.13 move, stale "Rencana 2" lines fixed. |
| O. Ledger | Addressed | `install.go` `realInstaller.Run` (`cmd.Cancel` SIGTERM, `WaitDelay` 30 s), `TestInstallerCancelSendsSIGTERM`; `create_replica.go` `replicaPreflight` + `foreignSource` (step-1 wait names host:port), `TestForeignSourceNamedBeforeReset`. |
| P. m3 / m4 | Addressed | `snapshot.go` `earlierAgentJob` (drop skipped while the earlier dump runs), `TestSnapshotRetryWaitsForEarlierDump`; `StartCreateReplica` refuses while prepare_master is active. The m3 guard does not cover a dump client orphaned by an agent restart (finding 4). |
| Q. SEC-27 | Partly | `users.go` `replGrants` = REPLICATION SLAVE, BACKUP_ADMIN; REVOKE ALL on an existing account; `mysql.createDumpUser`/`mysql.dropUser`; `snapshot.go` `copySnapshot`/`dropDumpAccount` + `cancelJob`; tests listed. Missing: "dropped afterwards on every path". An account created by a call whose answer was lost is never recorded, so it is never dropped (finding 2). |
| R. SEC-28 | Addressed | (a) `validateAccountName` prefix; (b) `refuseOnSource` comes before the first state change in `DropDatabases`, `DumpFrom` (DropExisting/ON, before `writeDefaultsFile`, drop and `resetBinaryLogs`), `CloneFrom` (before INSTALL PLUGIN and `clone_valid_donor_list`), `ConfigureReplication` (before STOP REPLICA), `ResetReplica`, `SetReadOnly(off)`; (c) `policy.go` + registry `Deny` + `cli.go`/`localagent.go`; (d) `logCommand`/`RedactParams`; tests listed. |
| S. SEC-30 | Addressed | `dbpanel-agent.service` and `agent-install.sh.tmpl` (no ProtectSystem, `KillMode=process`); `install_test.sh`. Side effect: finding 4. |
| T. SEC-33/34 | Partly | Host regex in `connect.go` `normalizeParams`, `preflight.go` `validatePreflight` and web `validHost`; caps in `handleEvents` (log 1 KiB), `setStep` (4 KiB), `agentJobEnded` (1 MiB), `inventoryCall` (5 000). Missing: the job.log line cap is applied only to agent log events, not in `jr.logf`; the job's `error` column is uncapped too (finding 5). |
| U. SEC-38 | Addressed | go.mod/go.sum edwards25519 v1.1.1. |
| V. Audit doc | Addressed | `## 9. Status perbaikan` with SEC-26..41. The SEC-31 and SEC-34 rows read "Diperbaiki" despite findings 1 and 5. |

## Findings

### 1. Important: a superseded failed job can still be reopened after a newer job is created (Retry race)
`internal/panel/orchestrator/runner.go:416-433` (`Retry`) together with `runner.go:375-383` (`execute` → `ReopenJob`).

`Retry` checks `refuseSuperseded` under `startMu`, then `launch` starts a goroutine and `startMu` is released. The failed job stays `failed` in the store until `execute` calls `ReopenJob`. `ActiveJobForServer` counts only running, waiting and queued jobs.

Failure path:
1. Retry on old failed job J1 passes its checks and releases `startMu`.
2. Before J1's goroutine reaches `ReopenJob`, a concurrent `StartCreateReplica`/`StartAddDatabases`/`StartRemoveDatabases` on the same server takes `startMu`, sees no active job, and creates J2.
3. J1's `execute` then reopens J1 without re-checking.
4. J1 and J2 both run against the same agent. J1 runs its stale params, which is the SEC-31 outcome.

The window is the goroutine start, so it is narrow: a double submit from two tabs or two operators. Approve is not affected, because a waiting job counts as active and blocks new jobs.

Smallest fix: in `Retry`, reopen the failed job (status running/queued) under `startMu` before `launch`. Alternatively, have `execute` take `startMu` and run `refuseSuperseded` again before `ReopenJob`.

### 2. Important: a dump account can be left on the master unrecorded
`internal/panel/orchestrator/snapshot.go:72-77` (`copySnapshot`).

`jr.st.DumpUser` is set and saved only after `mysql.createDumpUser` returns success. In each of these cases the account can exist on the master while `DumpUser` is empty, so neither the defer, a retry, nor Cancel ever drops it:
- `jr.call` times out (`callTimeout` 2 min, for example CREATE USER waiting on a metadata lock) and the agent completes the call afterwards;
- the job ctx is cancelled by Cancel during the call;
- the panel stops between the reply and `jr.save()`;
- the agent's `createAccount` fails after CREATE USER, at ALTER/REVOKE/GRANT.

The result is a leftover `dbpanel_dump_<hex>@'<replica>'` holding SELECT/RELOAD ON *.*, with a random password nobody holds. This breaks "dropped on every path"; the security impact is low.

Smallest fix: set `jr.st.DumpUser, jr.st.DumpHost = user, host` and `jr.save()` before calling `mysql.createDumpUser`. `dropUser` is `DROP USER IF EXISTS`, so dropping an account that was never created is harmless.

### 3. Important: dump account host = panel-observed address breaks the dump path on NAT, multi-homed or name-only setups
`internal/panel/orchestrator/snapshot.go:59` (`host := jr.r.sourceHost(srv)`) and `runner.go:917-922` (`srv.Address`, or `PublicHost` for the panel's own server).

`srv.Address` is the address the panel saw when the agent paired (`MarkPaired`). `PublicHost` may be a DNS name. The master matches the account against the address the replica connects from.

These cases fail:
- the replica reaches the master over a private network or VPN while it reached the panel over another address;
- the target is the panel host and `PublicHost` is a name, while the master runs with `skip_name_resolve` (common on aaPanel).

In both, `checkClients`' probe gets 1045 and every dump-based Buat replika and Tambah database fails. At ebf03a2 the dump used `dbpanel_repl@'%'` and worked. CLONE still works, but only when every database is selected and the target is empty. Nothing in the UI lets the user correct the replica address. This follows the ruling's `@'<replica address>'`, but the cost was not stated there.

⚠️ Whether this hits a given deployment depends on its network and cannot be verified from the diff.

Smallest fix: have the replica agent report the local source IP it uses toward `topo.SourceHost:Port` (a UDP `net.Dial` and its `LocalAddr`, from `mysql.preflight` with the result stored in jobState). Use that IP as the account host, falling back to `srv.Address`. At minimum, word the 1045 probe failure so it names the host used.

### 4. Important: `KillMode=process` leaves an in-flight `mysqldump | mysql` running after an agent restart; a retry then loads concurrently
`scripts/systemd/dbpanel-agent.service:16` and `internal/agent/drivers/mysql/dump.go:468,477` (`pipeDump` → `h.Exec`). No `SysProcAttr`/`Pdeathsig`/`Setpgid` exists anywhere in `internal/agent`.

With `KillMode=process`, systemd kills only the agent binary on restart or upgrade. Its child processes survive, where before the whole cgroup was killed.

Failure path:
1. The agent restarts mid-dump.
2. The agent's job table is gone, so `job.status` reports the job unknown and `earlierAgentJob`/`runAgentJob` do not reattach.
3. The panel step fails after `offlineGrace`. On Retry, `replicaSnapshot` drops the WipeList and starts a second dump.
4. The orphaned `mysql` client keeps running `DROP TABLE IF EXISTS` / `CREATE` / `INSERT` into the same databases.

The two loads interleave and the replica can end with missing or duplicated tables. The verify step cannot detect this, because it compares GTIDs, not data. An orphaned apt/dpkg from `mysql.install` survives the same way.

⚠️ Behaviour under real systemd cannot be verified from the diff; the report notes there is no systemd in this environment.

Smallest fix: start the client processes (`h.Exec` for mysqldump/mysql, and the installer) with `SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}`. Keep the mysqld start path without it, so mysqld survives as SEC-30 intends.

### 5. Important (low): SEC-34 job.log cap applies only to agent log events
`internal/panel/orchestrator/runner.go:871` (`logf`, no truncate), `runner.go:820-828` (`fail` → `logf("error", msg)`), `runner.go:849` (`setJob` errMsg uncapped), `runner.go:925` (`logChecks`, agent-provided `c.Detail`).

Agent-controlled text reaches job.log and `jobs.error` without the 1 KiB cap. This text includes the `JobDone.Error` / Hub error strings, the preflight check details, and the `%v` of agent errors in `verifyCopyAfterReconnect`/`replicaConnect`. A hostile agent can still write lines of about 1 MiB (bounded by the deferred `readLimit`) on every failure or preflight. `setStep` is capped, so step rows are fine.

Smallest fix: apply `truncate(msg, maxLogLine)` inside `logf`, and `truncate(errMsg, maxStepMessage)` in `setJob`.

### 6. Important (low): `masterVerify` keeps every existing source host, not only an edited one
`internal/panel/orchestrator/prepare_master.go:285-288`.

`ensureTopology` always creates the topology with `r.sourceHost(srv)`, so `topo.SourceHost` is never empty, and running Siapkan master again no longer refreshes it. Deviation 4 says only an edited host is kept, but this keeps every existing host.

Failure path: the master is re-paired from a new IP, or the panel's `PublicHost` changes, and Siapkan master runs again. The replicas' address stays the old one, where at ebf03a2 it was updated. New replicas fail preflight reachability until the user finds the new edit form.

Smallest fix: record manual edits (for example a `source_host_manual` flag set by `setMasterSource`) and keep the host only when that flag is set.

### 7. Minor: test gaps
- `superseded_test.go` has no test for the role-changed or server-deleted refusals. Its comment says "the server was made a master meanwhile", but the code does not change the role.
- The `refuseOnSource` check on `dumpFrom` in GTID ON mode without DropExisting (deviation 2) is untested in `TestDestructiveCommandsRefusedOnSource`.
- `TestLoadIgnoresClientCommands` calls `t.Skip` when the mysql client is missing (the SEC-26 guard then goes untested silently on such a host).

### 8. Minor: the policy loader checks the file but not its directory
`internal/agent/policy.go:42-58`. The file gets `Lstat` (mode and owner checks) and is then read with `ReadFile`, which follows symlinks. The parent directory's ownership and mode are not checked. If `/etc/dbpanel` were writable by a non-root user, the file could be swapped between the two calls or replaced outright. This does not apply to the documented root-owned layout.

Fix: open with `O_NOFOLLOW`, `Fstat` the open descriptor, and check the directory's mode and owner.

### 9. Minor: the wipe dialog omits the foreign-source reset
`internal/panel/web/handlers_jobs.go:144`. When the wait is both a wipe and a foreign source, `WaitReason` is `wipe`, so the dialog's Impact names only the databases. The wait message above the dialog does name the source.

### 10. Minor: cosmetic edges
- `RedactParams` cuts at a byte (`registry.go:169`) and can split a UTF-8 rune in the log.
- `PlainHTTP` uses `r.TLS == nil` (`handlers_servers.go:157`), so the warning also shows behind a TLS-terminating proxy.

### 11. Minor: weak GTID check after a CLONE reconnect
In `verifyCopyAfterReconnect`, for CLONE the GTID check is only "non-empty". It could also require that the set contains `jr.st.MasterUUID`; the clone_status check already covers the main failure.

## Flow traces

**(a) C1, CLONE AdminReplaced.**
1. `copySnapshot` → `runAgentJob(cloneFrom)` returns `AdminReplaced`.
2. `replicaSnapshot` saves `AdminReplaced`, `WaitReason=reconnect` and the snapshot GTID (empty for CLONE), then waits at step 4.

Whether the user presses Approve or Retry, `replicaSnapshot` first runs `verifyCopyAfterReconnect`, so there is no second copy:
- **Approve without reconnecting:** `mysql.status` is refused with "login MySQL gagal: Error 1045", `loginRefused` matches, and the step waits again.
- **clone_status not Completed, or a clone error:** the step fails with "CLONE tidak selesai … ulangi Buat replika". `AdminReplaced` stays true, so later Retries keep failing the same way, and a new Buat replika supersedes the job.
- **Completed:** the snapshot GTID is set from `gtid_executed`, `setReadOnly(true)` runs, the inventory is refreshed, and the step is done. Step 5 keeps its own reconnect wait.

The E2E exercises the real path end to end.

**(b) C2, superseded refusal.**
- Approve: safe. A waiting job is active and blocks every Start* on that server.
- Retry of a failed job: residual window (finding 1).
- Other entries: `launch`'s pending path and `execute`'s step check add no new hole.
- `Superseded()` for the page runs without the lock; it only affects display.

**(c) Q/R, dump account.**

Where the password goes:
- It is never stored in jobState or the job log.
- The agent command log redacts the `password` key at any depth.
- `createAccount` error text replaces the password.
- The defaults file is 0600 and removed afterwards.
- `JobStatus` carries no params.

The account itself can leak only through finding 2. `refuseOnSource` comes before the first destructive statement in all six commands (see R above).
