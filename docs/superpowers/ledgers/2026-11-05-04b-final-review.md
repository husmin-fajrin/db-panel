# Final whole-branch review + security audit — Plan 4b (001efd7..1ec2494)

Reviewer: opus, read-only, one reviewer in passes (agent pins/lock/promote/quarantine/repoint/salvage/connections/binlog; orchestrator promote/oldmaster/controls/create_replica; web handlers, confirm, router/headers, templates, app.js; store, notify, README). Test suites not re-run (controller runs them).

## Summary verdict

The agent-side authority model holds: every new command is gated by the role pin (`Allow`/`refusedByRole`, `requireMaster`), cross-server reads use only the replication account and `@@global` variables, salvage paths are confined (`os.Root`, strict regex, `O_NOFOLLOW`, regular-file checks), confirmation tokens bind user+action+target and per-file for salvage, security headers wrap every route (incl. the streamed download and the fragment), templates only use auto-escaping, Telegram is plain text, and passwords never reach errors, job state or logs (`RedactParams`). Crash points in `mysql.promote` (marker, PERSIST order, resume, NotBefore record) and the panel's unknown-outcome → partial rules are sound. Two Important issues remain: one two-writable-masters path and one cutover screen that can tell the operator a falsehood.

## Security findings

**SEC-66 (Important) — stale planned lock lets an emergency promote run "as planned" and the old master reopen.**
`internal/panel/orchestrator/promote.go:829-874` (promoteUndo leaves the lock when the master agent is unreachable, promotion `failed`), `:131-260` (StartPromote allows a new promote after `failed`), agent `internal/agent/drivers/mysql/promote.go:361-407,318-331` (emergency → planned when the old master answers read-only) and `:441` (no margin check when `LockDeadline` is nil).
Scenario: planned promote to X fails before the point of no return because the master's agent dropped off the panel; unlock fails, lock stays. Master status = "Agent offline" → page offers emergency; user promotes Y≠X. Y's agent sees the old master read-only, converts to planned, opens writes. At the deadline the old master's guard reads X read-only twice and unlocks itself; quarantine is pending (agent offline to the panel). Old master (still where apps write) and Y are both writable until the agent reconnects.
Fix (both): agent — the guard, before unlocking, reads every replica in `binlogDumpHosts`/`replicas-seen` with the lock's credential and self-quarantines instead if any is writable holding this server's UUID; panel — StartPromote refuses emergency while the latest promotion has `LockTried`/`lock_at` with unlock unconfirmed and `now < deadline + grace + recheck` ("master mungkin masih terkunci… tekan Ulangi pada promote terencana atau tunggu sampai HH:MM"). Add a test for the sequence.

**SEC-67 (Important) — cutover screen hides every session of the agent's admin account.**
`internal/agent/drivers/mysql/connections.go:33-44` excludes `PROCESSLIST_USER = <USER() name>` from any host; README:253 tells operators to use `root`; `cutover.html:51` says only `dbpanel_*`, replicas and MySQL threads are hidden. Hospital apps commonly use root → "Tidak ada koneksi aplikasi tersisa", no warning in the rebuild confirmation (`handlers_oldmaster.go:570-576`), operator rebuilds, writes during the read-only-lifted load are lost (README:970-976 admits the window).
Fix: exclude only `CONNECTION_ID()` and that user's sessions from the local host (socket/localhost/127.0.0.1/::1); show others, labelled "akun admin panel"; make the page text name what is hidden.

**SEC-68 (Minor)** — `peer.go` `ReadPeer` without Verify uses `TLSConfig "preferred"`: an active MITM can strip TLS (replication channel requires `SOURCE_SSL=1`). Use `skip-verify` (TLS required).

**SEC-69 (Minor)** — `promote_lock.go` `GuardTick` blocks on `promoteMu` while a repoint holds it (3 probes ×(5+10 s) + 15 s receiver wait): agent health loop stalls (Task 6 I2 class). `TryLock` and skip the tick.

**SEC-70 (Minor)** — `salvage.go` accepts any subset of own `gtid_executed`, no total disk cap, concurrent runs: a compromised panel can extract arbitrary binlog history (row data) and download it, or fill `/var/lib` with N×2 GiB. Scope to `GTID_SUBTRACT(own, pinned source)` or cap the directory and one run at a time; else document.

**SEC-71 (Minor)** — download token in the URL query (`handlers_oldmaster.go:444`): single-use, 2 min, per file, HTTPS only, `Referrer-Policy: same-origin` — acceptable; a POST form would keep it out of history.

**SEC-72 (Minor, ledger T10)** — raw error text on page/audit: `handlers_oldmaster.go:622` (`err.Error()` of mysql.repoint), `handlers_promote.go:170` (`msgPromoteView+err.Error()` in audit). Route through `agentFailure`.

**SEC-73 (Minor)** — the 2-min lock margin compares the master agent's absolute deadline with the candidate's clock (`promote.go:441`); skew erodes it. Send seconds remaining or require NTP in README.

## Correctness / data-safety findings

**C-1 (Minor)** — `checkResume` (`promote.go:259-268`): mysqld restart between `SET PERSIST read_only=OFF` and `RESET REPLICA ALL` auto-starts replication; resume refused with "Kunci kembali replika ini". Following it after the old master self-quarantined leaves both read-only with no path (quarantined refuses lockForPromote). With a valid marker, `STOP REPLICA` and resume; fix the message.

**C-2 (Minor, ledger T7)** — `old_master_server_id` never cleared: `create_replica.go:112-118` re-marks a rebuilt (replica) old master `quarantined` after a later failed attempt. Clear it when the rebuild completes or the server is deleted.

**C-3 (Minor, docs)** — README security limits lack spec §8 bullets 1–2 (compromised panel can lock a master via a connected replica and force a planned promote; strict `createReplUser` on replicas) and "a compromised panel can wipe leftover data on a quarantined server via the rebuild path/dropDatabases".

## Deferred-minor triage

Must fix before merge: SEC-66, SEC-67, SEC-72 (T10 raw error), C-2 (T7 old_master_server_id).
May stay (record in 4c): T1 minors; T2 messages/asymmetry/CLI LoadOrCreate; T3 second admin conn, serial DNS, NAT note (README has it); T4 binlogTail socket-vs-TCP probe, res.Mode assertion, stall-path test; T5 quarantinePending in memory, source-addr binding; T6 salvage disk/scope (SEC-70) unless fixed, name collision, thread-pool/X clients; T8 store reads under mu, second master_quarantined; T10 fragment any id, SalvageList per view; T11 stale allowSalvage comment, subset-dump loaded mark, agentapi race flake.

## Residual risks to present to the owner

1. Compromised panel: lock a master through a connected replica (writes stop until cutover/unlock-master); point a DR replica at an attacker MySQL when the source is locked/dead; wipe leftover data on a quarantined server; extract binlog history via salvage (SEC-70).
2. Network split: an emergency promote proves unreachability, not death; the other side keeps writing (found by "Periksa").
3. Emergency quarantine window (seconds) and a rebuild load during which the old master is writable.
4. Unverified topologies (no "Perketat"): DNS/MITM on pinned names and the replication password.
5. Salvage files hold real data 7 days on the old master (0600 root).
§8 is accurate; add SEC-66 until fixed.

## Declined to judge

Per-test fidelity of every fake (sampled only); UI wording beyond safety statements; install.sh/HTTPS (Plan 4a).

Final verdict: mergeable after fixes

## Re-review of the fix wave (1ec2494..1b4a16a)

- **SEC-66(a) ADDRESSED.** `verifyOldSource` returns `oldMasterAlive` on any answered probe; the pre-open probe in `openWrites` restores and refuses; the emergency→planned branch is gone. Remaining paths that open writes in emergency: all probes fail and the IO thread is not running (MySQL unreachable *from this replica*: the documented network-split residual), or a resume, which needs a marker written only after verification. None with the old master answering.
- **SEC-66(b) ADDRESSED.** The guard runs `writableSeenReplica` only after the candidate is read-only twice. An unreadable seen file keeps the lock. Replicas that refuse the credential, are down, or listen on another port are passed over, so the guard unlocks as before (documented).
  - Abuse check: a compromised panel that took the lock can already demote the master with a real planned promote (§8 bullet 1), and can already hold the lock forever by making the candidate unreachable. Quarantining under a writable replica that holds the master's UUID adds no capability, so it is not worse than today.
  - New Minor (non-malicious): a seen replica that is briefly writable at the deadline (a resync/dump load, or the `createReplUser` lift) quarantines a healthy master. Precondition: a failed planned promote whose unlock was not confirmed. Result: an outage, no data loss; recovery is a root CLI command. Suggest refusing resync/create on the topology while `refuseWhileLockMayStand` holds.
- **SEC-66(c) ADDRESSED.** `refuseWhileLockMayStand` covers LockDeadline, LockTriedAt+slack, FinishedAt and StartedAt fallbacks. `LockUnlocked` is set only on a confirmed unlock or a "not locked" answer. It checks only the latest promotion, which is acceptable now that (a) is the agent-side gate.
- **SEC-67 ADDRESSED.** Only the agent's own connection and the admin account's loopback/socket sessions are hidden; the rest are badged "akun admin panel". The page text now states exactly what is hidden, and the rebuild warning counts admin sessions. Residual: an application on the same host using that account over localhost stays hidden. It is disclosed on the page, and the README now recommends a dedicated account.
- **SEC-68 ADDRESSED.** `skip-verify` (TLS required), with a test.
- **SEC-69 ADDRESSED.** `TryLock` skip. The quarantined guard still detects a restart through Uptime on the next tick, and PERSIST covers the gap.
- **SEC-70 ADDRESSED.** The salvage set must be a subset of the last `mysql.leftover` set. That set is computed against the pinned source at its verified address (`pinnedNewMaster`), so the panel cannot widen it. There is one run at a time and an 8 GiB directory cap with a bounded writer.
- **SEC-72 ADDRESSED.** `agentFailureAs` covers the repoint and promote-view page and audit, with tests.
- **SEC-73 ADDRESSED.** The panel sends `LockRemainingSec`; the agent measures the margin on its own clock from the time it received the request. An expired lock sends 0, so the promote is refused.
- **C-1 ADDRESSED.** With a valid marker, `STOP REPLICA` and resume; the messages are reworded.
- **C-2 ADDRESSED.** `ClearOldMaster` runs on a completed rebuild (`replicaVerify`) and inside the DeleteServer transaction. OnOnline's needs_repoint branch is now keyed on the topology having any promotion. A rebuilt old master is `replica`/ready, so it is neither repointed nor re-quarantined (that branch requires role `quarantined` and a pending quarantine). Every needs_repoint replica belongs to a topology with a promotion, so none is skipped.
- **C-3 ADDRESSED.** The README security section has the two §8 bullets, the leftover-wipe risk, the salvage bounds and the guard limitation.
- Note: spec §5.1 and §5.2 still describe emergency→planned. Amend the design doc so it matches the code.

No new Critical or Important breakage in this diff.

Final verdict: mergeable
