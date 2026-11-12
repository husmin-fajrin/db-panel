# Plan 5a — security audit, agent side

- Date: 2026-11-12
- Code audited: base `0912c5f` → head `343898c` (later commits touch only the risk inventory document)
- Seat: agent side (`internal/agent`, `internal/proto`, `scripts`). The panel/web side and the general code review are other seats.
- Method: code reading only. Nothing was run (no `go test`, no mysqld, no root side effects), as instructed. Every finding below was traced in the source at HEAD; where a finding depends on behaviour outside this repository (the Go MySQL driver, MySQL itself, the kernel), that is said and the source checked is named.

## 1. Scope and method

### What was read

1. `final-overview.txt`, `global-constraints.md`, `final-ledger-triage.txt` (deferred minors and rulings), the open rows of `2026-11-10-inventaris-risiko.md` (R-001–R-108), spec 5a §3–§5.5, §5.8, §7, §8, and the method sections of the Plan 3 / 4a audits and the 4b final review (to know what was examined before).
2. `final-agent-code.diff` in full, in five passes (cli/configure/conn/connections; driver/dump/health/peer/promote/promote_files; promote_lock/quarantine/snapshot; startlock; startlock_guard/status/tls/monitor/proto/scripts).
3. Source at HEAD beyond the diff, to follow data flows: `pins.go` (trust checks, role table, `Allow`, `Release`, `claimRole`, `settleRebuilt`, `Bootstrap`, `writePin`), `realhost.go` (`WriteConfig`, `writeFileAtomic`, `ValidateConfig`, `Exec`), `layout.go` (`DiscoverLayout`), `peer.go` (`ReadPeer`, `validatePeer`), `promote_lock.go`, `promote_files.go`, `quarantine.go` (`quarantine`, `seenAddr`, `matchAddr`, `guardQuarantined`), `status.go` (`binlogDumpHosts`, `replicaPorts`), `replication.go` (`mysql.releaseRole`), `configure.go` (`mysql.configure`, `mysql.setReadOnly`), `databases.go` (`mysql.dropDatabases`), `command.go`, `agent/cli.go`, `agent/session.go`, `agent/runtime.go` (`Emit`), `agent/monitor/monitor.go`, `scripts/systemd/dbpanel-agent.service`, and `go-sql-driver/mysql v1.9.3` (`auth.go`, `packets.go`) for the authentication and packet-size behaviour that two findings rely on. On the panel side only what was needed to size agent-side findings: `orchestrator/runner.go` (`callTimeout`, cancel path), `orchestrator/promote.go` (`promoteLock`), `monitor/promote.go` (how start events are consumed), `store/topologies.go` (who holds the replication password).
4. `final-agent-tests.diff` was searched (not read end to end) to learn whether a property is pinned by a test.

Repository rule followed: `graphify query` before searches; raw files read only for lines the graph or the diff located.

### What was not covered

- Panel/web handling of the new events and fields (other seat).
- Runtime behaviour: nothing was executed, so claims about MySQL (`--print-defaults` output shapes, `init_file`, `~/.my.cnf`) rest on the spec's recorded experiments and on documented MySQL behaviour, not on a run of mine. Items that would need a run are in section 5.
- `testmysql`, e2e helpers and test-only export files, except to check coverage.
- The `rebuild_load.go` deferral logic and the salvage code were not re-audited (unchanged by 5a).

### How severity is used

The brief's rubric is applied by real exploitability. Two findings (SA-1, SA-2) match the *wording* of "Critical" (a panel-level attacker reaches two writable masters / data loss). I rate them High, not Critical, for three stated reasons: they need a fully compromised panel, the owner has already accepted equivalent panel-level harm as class B (R-019, R-021), and neither is introduced by Plan 5a (both are Plan 4b design). They are not in the risk inventory, and that is the point of reporting them. The owner may reasonably rate them differently.

## 2. Findings

| ID | Severity | OWASP 2021 | Title | Where |
|---|---|---|---|---|
| SA-1 | High (pre-existing, class B, not in inventory) | A01 / A04 | A compromised panel can un-quarantine an old master and open it for writes; the start lock goes with it | `pins.go:103-117`, `replication.go:168-195`, `pins.go:590-612`, `startlock_guard.go:153`, `configure.go:96-117` |
| SA-2 | High (pre-existing, class B, not in inventory; eased by 5a) | A04 / A01 | The replicas-seen gate does not stop a panel that holds the replication password: it can quarantine a healthy master under a server of its own and then drop its data | `quarantine.go:107-170`, `status.go:213-234`, `promote_files.go:603-615`, `pins.go:715-728`, `databases.go:15-38` |
| SA-3 | Medium | A02 / A07 | On every planned promotion the replication password is offered, over TLS without verification, to every host recorded in the last year at every recorded port | `promote_lock.go:210`, `promote_lock.go:279-304`, `peer.go:78-80`, `promote_files.go:322` |
| SA-4 | Medium | A04 / A05 | The recorded port set comes from the replica (`report_port`) and has no cap: `lockForPromote` fans out hosts × ports for up to 10 minutes, can lock the master long after the panel gave up, and can write a lock file it cannot read back | `status.go:238-271`, `promote_files.go:428-455`, `promote_lock.go:210-224`, `promote_lock.go:347-374`, `session.go:21,97`, `promote_files.go:62,310` |
| SA-5 | Medium | A09 | The one-shot events `monitor.quarantineStart` (late / bypassed) and `monitor.quarantineStartFailed` are lost when the panel session is not up at that instant, which is the normal state at boot and in a partition | `quarantine.go:315-318`, `startlock_guard.go:307-309,385`, `promote_lock.go:681-692`, `runtime.go:94-105`, `cli.go:152,167-171` |
| SA-6 | Medium | A01 / A05 (CWE-59, CWE-61, CWE-400) | The guard opens MySQL's error log as root with a plain `os.Open`: a FIFO there hangs the guard loop for good with `promoteMu` held; a link to a device node is opened as root | `startlock_guard.go:272-331` |
| SA-7 | Low | A01 (CWE-59, CWE-367) | Start lock file: `Chmod`/`Chown` follow symlinks and no code checks who owns the directory (wider than ledger item m11) | `startlock.go:392-422`, `configure.go:320-345` |
| SA-8 | Low | A04 (CWE-662) | The start lock's file and config line are renamed into place without syncing the directory; `ClearLock` does not sync either | `realhost.go:186-221`, `promote_files.go:266-279` |
| SA-9 | Low | A05 | `StartLocked` is computed from `mysqld --print-defaults` run in the agent's environment, which does not read the option file of the MySQL service account's home | `startlock.go:436-503`, `scripts/systemd/dbpanel-agent.service` |
| SA-10 | Low | A04 (CWE-400, CWE-770) | Nothing bounds what a peer sends back; a peer's error text goes unbounded into logs and into the `kept` event | `peer.go:53-96`, `promote_lock.go:559`, `promote_lock.go:347-374` |
| SA-11 | Low | A04 / A05 | The local unlock paths (`unlock-master`, `forget-replica`) say nothing about what they bypass, and an unreadable lock file tells the operator to run `unlock-master` | `cli.go:399-458`, `promote_files.go:218-221` |
| SA-12 | Info | A03 (CWE-116) | The start lock path is written into an option file without checking for characters the option-file syntax gives meaning to | `startlock.go:290-320`, `startlock.go:229-244` |

Counts: Critical 0, High 2, Medium 4, Low 5, Info 1.

## 3. Findings in detail

### SA-1 (High) — a compromised panel can un-quarantine an old master and open it for writes

**Attacker and position.** Someone who controls the panel process (can send any agent command over the authenticated channel). No access to the database host.

**Where.**
- `pins.go:103-117`: the refusal table for `quarantined` lists `mysql.lockForPromote`, `mysql.promote`, `mysql.repoint` and whatever a replica refuses (nothing). `mysql.releaseRole` is not refused.
- `replication.go:168-195`: `mysql.releaseRole` asks only that no replication source is configured and no replica reads the binary log (`refuseReplicating`, `replication.go:611-632`), then calls `Pins.Release`.
- `pins.go:515-527`: `Release` writes `unassigned` for any role but master.
- `pins.go:764-831` (`refusedByRole`): an unassigned server refuses neither `mysql.setReadOnly{on:false}` nor `mysql.configure{role:"master"}`.
- `pins.go:590-612` (`claimRole`): `mysql.configure{role:"master"}` on an unassigned server pins it master.
- `startlock_guard.go:153`: on role master the guard releases the start lock at once, with no read-only proof ("root decided"); on role unassigned it releases as soon as read-only is proved, which is the case right after a quarantine.
- `configure.go:96-117`, `configure.go:607-628`: `mysql.setReadOnly{on:false}` runs `SET PERSIST super_read_only = OFF`, `SET PERSIST read_only = OFF`.

**Path.** A genuinely quarantined old master after a promotion: MySQL up, no replication source configured, replicas already repointed (so no Binlog Dump thread).
1. Panel sends `mysql.releaseRole`. Allowed; role becomes `unassigned`.
2. Within one guard round (5 s) the guard sees role unassigned and read-only proved, and removes the `init_file` line and the lock file.
3. Panel sends `mysql.setReadOnly{on:false}`. Allowed on an unassigned server (`refuseOnSource` passes: nothing reads its binary log). Both flags are PERSISTed OFF.
   Variant: `mysql.configure{role:"master"}` first. The role is then pinned master, the guard releases the start lock at once whatever MySQL says, and `setReadOnly{off}` is the normal "open master".

**Harm.** The old master is writable again, also across restarts, while the new master is writable: two writable masters. Applications that still point at the old address write there, and those writes are not on the new master. The quarantine and the start lock, whose purpose is to hold without the panel, are both removed on the panel's word.

**Why not Critical, and what is new.** `releaseRole` on a quarantined server is a Plan 4 design decision (spec 4: "`releaseRole` pada `quarantined` → `unassigned`"), used by "Hapus server". Plan 5a did not add it. What 5a adds is that its new control follows the same rule: the start lock is released as soon as the role is no longer quarantined, and on role master without any proof, and a master role can be claimed through the panel (`claimRole`) or through `role_hint` (`Bootstrap`, `pins.go:548-583`, after root removed the role file). The inventory has no row for "a compromised panel opens a quarantined old master" (R-017/R-018/R-019/R-021 do not say it), and spec 5a §7 only says the compromised-panel class is unchanged.

**Fix, proportionate.**
1. Agent: refuse `mysql.releaseRole` while the role is `quarantined` (one row in `roleRefuses`). Leaving quarantine is then only a rebuild from the pinned source (`settleRebuilt`) or root's `set-role`. "Hapus server" then always leaves the old master quarantined and locked, which is what its dialog already says for the MySQL-down case (spec 5a §5.2).
2. Agent: release the start lock on role master only when the role was written by the local CLI or by a promotion, not by `claimRole`/`Bootstrap`; simplest form: while an agent `init_file` line is present, refuse `mysql.configure{role:"master"}` and `mysql.setReadOnly{on:false}` from the panel.
3. Whatever is decided, add an inventory row (class B, Tinggi).

Note that a longer panel-only path stays open by design after fix 1: rebuild the old master from the new one, stop and reset replication, release, open. That path first replaces the old master's data with the new master's, so it is the R-019 class; closing it needs the "mode ketat" local approval planned for 5b.

**Tests that would pin it.** (a) dispatch `mysql.releaseRole` on a rig pinned quarantined with a source pin: refused, role still quarantined, start lock line and file still present after a guard tick. (b) on an unassigned rig that still carries the agent's `init_file` line: `mysql.configure{role:"master"}` and `mysql.setReadOnly{on:false}` refused; after `set-role master` through the CLI both work.

### SA-2 (High) — a panel that holds the replication password can quarantine a healthy master under its own server, then drop its data

**Attacker and position.** Someone who controls the panel process. The panel stores the replication password (`store/topologies.go:16`, sealed with the panel's own key), so this attacker has it. The attacker needs one host that can reach the master's MySQL port (the panel host itself will do).

**Where.**
- `status.go:213-234` (`binlogDumpHosts`): a host is "a replica" when it has a session whose command is `Binlog Dump` or `Binlog Dump GTID`. Any account with REPLICATION SLAVE, from any host, produces that.
- `promote_files.go:532-564`, `promote_files.go:603-615`: the guard loop records such hosts every 5 s, and `mysql.recordReplicas` records them at once on the panel's request.
- `quarantine.go:107-121`: on a master that is not locked for that candidate, `mysql.quarantine` asks only that the named host is in replicas-seen (`seenAddr`).
- `quarantine.go:129-146`: the "new master" must answer writable and its `gtid_executed` must hold a transaction of this server's UUID. It is read with the account the panel sends and, unless the panel sets `verify`, over TLS without verification (`peer.go:78-80`).
- `quarantine.go:148-155` → `promote_lock.go:646-676` (`selfQuarantine`): PERSIST read-only, pin quarantined under the named source, write the start lock.
- `pins.go:715-728` (`Allow`): a quarantined server with a source pin accepts `mysql.dropDatabases` and accepts `mysql.dumpFrom`/`mysql.cloneFrom` from the pinned source. `databases.go:15-38`, `databases.go:71-111`: `dropDatabases` drops what it is told, once no replica reads the binary log.

**Path.**
1. From host H the attacker logs in to the master as `dbpanel_repl` and starts a binary log dump (a real `mysqld` on H configured as a replica does it; it then really holds the master's transactions).
2. Within 5 s, or at once with `mysql.recordReplicas`, H is in replicas-seen.
3. The attacker makes the server on H writable and sends `mysql.quarantine{new_master: H:port, user/password of an account on H, verify:false}`. Every check passes: H is "seen", it answers, it is writable, it holds the master's UUID.
4. The production master PERSISTs read-only, is pinned quarantined under H and gets the start lock. Only root on the host can undo it (`set-role master`).
5. The attacker stops the real replicas through their agents and closes its own dump session, then sends `mysql.dropDatabases` (or `mysql.cloneFrom` from H). The pins allow it, because H is the pinned source.

**Harm.** At step 4: production writes stop until root intervenes (availability). At step 5: the production master's data is dropped or replaced by the attacker's copy. In a topology with in-sync replicas the data survives there; with no replica, or after the same attacker resyncs the replicas, it does not.

**Why not Critical, and what is new.** The quarantine checks are Plan 4b. The 4b review considered the guard's self-quarantine ("adds no capability" beyond an unwanted planned promote) but I found no record that examined this command path with a server the panel attaches itself. Plan 5a relies on the gate explicitly: spec 5a §4 rejects taking replica hosts from the panel because "panel yang dibobol bisa mengarantina master sehat. Itu serangan yang desain karantina larang", and the global constraint says the agent never accepts replica hosts from the panel. In effect it does: the panel cannot name a host, but it can make any host of its choosing recorded, and 5a made that faster (5 s loop) and on demand (`mysql.recordReplicas`). Before 5a the same was possible within one check interval, which the panel sets (minimum 10 s), so feasibility is unchanged. The header of `pins.go:22-27` ("a compromised panel can therefore never … drop or replace its data") does not hold for this path. Not in the inventory.

**Fix, proportionate.** There is no cheap check that tells a rogue replica from a real promoted one while the panel holds the replication password; that is R-038's root cause (per-replica accounts the panel never sees, planned 5c). What can be done now:
1. Contain the damage: after a quarantine that was not preceded by this agent's own promote lock for that candidate (the emergency path), refuse `mysql.dropDatabases`, `mysql.dumpFrom` and `mysql.cloneFrom` until root confirms on the host (a marker file written by a new `dbpanel agent allow-rebuild`, removed by the rebuild). The forged path then ends at an outage, not at data loss. This is the 5b "mode ketat" idea applied to one command group.
2. Correct the comment in `pins.go:22-27` and add an inventory row (class B, Tinggi) that names both the outage and the data-loss step.

**Tests that would pin fix 1.** Rig pinned master with a second mysqld recorded as seen, writable and holding the UUID: `mysql.quarantine` succeeds, then `mysql.dropDatabases` and `mysql.cloneFrom` are refused with the CLI hint; after the marker is written they are accepted. A quarantine that follows `lockForPromote` for the same candidate keeps today's behaviour.

### SA-3 (Medium) — the replication password is offered to every host recorded in the last year, at every recorded port, without certificate verification

**Attacker and position.** Either (a) a device on the database network that takes the IP address of a server that was a replica within the last year and is no longer there, or (b) an unprivileged local user on a host that is in replicas-seen where one of the recorded ports is not in use (a former replica whose MySQL was removed; or a topology whose replicas use more than one port). No MySQL account, no MITM position.

**Where.**
- `promote_lock.go:210`: `lockForPromote` calls `readableAtLock` with every host of replicas-seen and every port of replica-ports-seen plus the candidate's port.
- `promote_lock.go:279-304`: each combination is read with the candidate's account (`c := cred; c.Host, c.Port = h, port`).
- `promote_files.go:322` (`seenCutoff`): hosts and ports stay recorded for a year. `forget-replica` is the only removal and nothing runs it when a replica is detached.
- `peer.go:78-80`: without `Verify` the read uses TLS with `skip-verify`.
- Driver behaviour this depends on, checked in `go-sql-driver/mysql@v1.9.3/auth.go:400-406`: when the server answers "perform full authentication" for `caching_sha2_password` and the connection is TLS (verified or not), the client sends the password in clear inside that TLS session.
- The same reads happen again in the guard at lock expiry (`promote_lock.go:584`, `readListed`), but only for entries that answered at lock time.

**Path.** The attacker listens on the stale address (or the unused port) with any program that speaks the MySQL handshake, offers TLS with any certificate and asks for full authentication. At the next planned promotion the master's agent connects and sends `dbpanel_repl`'s password.

**Harm.** `dbpanel_repl@'%'` holds REPLICATION SLAVE and BACKUP_ADMIN (R-038): with its password the attacker can replicate or CLONE the whole patient database from the master. The same password is what SA-2 and SA-4 start from.

**What is new.** Before 5a the agent read seen hosts with this account only in the guard, after a planned promotion had failed and its lock expired, and only at the candidate's port. 5a does it at the start of every planned promotion and at every recorded port. Topologies with "Perketat" (certificate verification) are not affected: the TLS handshake fails before any credential is sent. R-039 records the unverified-topology class for an on-path attacker; this variant needs no on-path position, so it is worse than recorded.

**Fix, proportionate.**
1. Short term, small: record with each host the SHA-256 of the TLS public key it served when it was first observed as a replica (the agent can probe `host:port` at record time without logging in), and in `ReadPeer` without `Verify` refuse, before authenticating, a server whose key differs (`tls.Config.VerifyPeerCertificate` with `InsecureSkipVerify`). A squatter then gets a TLS alert, not a password, and the R-033 coverage is unchanged.
2. Durable: the per-replica read account planned for 5c (R-108, R-038), so that what is sent has no privilege worth stealing.
3. Operational meanwhile: document that `forget-replica` must be run on the master when a replica is retired, and have the panel refuse or warn on a planned promotion in an unverified topology (R-039's proposed fence).

**Test that would pin fix 1.** A fake listener that accepts TLS with a fresh key and records what it receives: after `lockForPromote` with a recorded host pointing at it and `Verify` false, the listener has received no authentication packet and the entry is not in `Readable`.

### SA-4 (Medium) — the recorded port set is replica-supplied and uncapped; `lockForPromote` can run for 10 minutes, lock late, and write a lock it cannot read

**Attacker and position.** Anyone holding an account with REPLICATION SLAVE on the master: a compromised replica host, or a retired replica that still knows the shared `dbpanel_repl` password (R-038 says it stays valid). Not the panel.

**Where.**
- `status.go:238-271` (`replicaPorts`): ports are the `Port` column of `SHOW REPLICAS`, which is the `report_port` a client sends when it registers as a replica. Any value 1–65535 is taken.
- `promote_files.go:428-455` (`RecordReplicaPortsSeen`): merged into the file with no cap on the number of ports; kept a year. `promote_files.go:457-459`: the file is read back with a 1 MiB limit.
- `promote_lock.go:279-304`, `347-374`: at lock time every host × every port is read, 16 at a time, 5 s each.
- `promote_lock.go:211-215`: if the request context ended, the lock is refused.
- `session.go:21,97`: the agent's request context is 10 minutes. `orchestrator/runner.go:88`: the panel waits 2 minutes and sends no cancel.
- `promote_lock.go:222`: the `Readable` list is written with no length limit; `promote_files.go:62,310`: the lock is read back with a 64 KiB limit.

**Paths.**
1. *Planned promotion blocked.* The attacker registers many replica sessions with different `report_port` values over a few minutes (each guard round records the ports registered at that moment). On a host it controls, those ports drop packets. At the next planned promotion the fan-out cannot finish in 10 minutes and the lock is refused; the panel has timed out 8 minutes earlier. `promoteMu` is held throughout, so guard ticks are skipped and `mysql.unlockPromote`/`mysql.quarantine` wait. This lasts until someone edits `/var/lib/dbpanel/replica-ports-seen` by hand: there is no CLI for ports.
2. *Late lock.* With a port count tuned so the fan-out ends between 2 and 10 minutes, the agent writes the lock and sets `super_read_only` after the panel has reported the step as failed. The master is read-only for the lock time (1800 s) plus grace although no promotion is running, and the panel's "lock may still stand" window (computed from when it asked) ends before the real deadline.
3. *Lock file the agent cannot read.* If the attacker's host answers on the ports (one `mysqld` plus a port redirect is enough), about 3,300 entries make the JSON exceed 64 KiB. `WriteLock` succeeds, `super_read_only` is set, and from then on `ReadLock` fails: the guard returns an error every round, `mysql.unlockPromote`, `mysql.setReadOnly{off}` and `mysql.quarantine` are refused ("kunci promosi tidak bisa dibaca"). The master stays read-only (fail closed), but after a promotion that did complete the old master cannot be quarantined, and the only command that clears the state is `unlock-master`, which opens it (see SA-11).
4. *Record stops.* Once either seen file passes 1 MiB (about 38,000 ports; or many source addresses for hosts), every later read fails, so recording stops for good with only a once-a-minute local warning. For the hosts file that means new replicas are never recorded and a later quarantine under them is refused.

**Harm.** Availability of planned failover and of the master's writes; in path 3, a stuck state whose documented exit opens the old master. No direct data loss.

**Fix, proportionate.**
1. Cap the port set (a small number, for example 8 distinct ports); beyond it, do not record, and say so in a health field the panel can alert on.
2. Give the fan-out its own budget well under the panel's call timeout (for example 60 s) and a maximum number of reads; over either, refuse the lock at once with a message that names the files.
3. Refuse the lock when the encoded lock would exceed `maxRecordSize`.
4. Never write a seen file larger than what is read back (drop oldest entries first, and report it).
5. Extend `forget-replica` (or add a sibling) to clear ports.

**Tests.** `RecordReplicaPortsSeen` with 1,000 ports keeps at most the cap. `lockForPromote` with a `readPeer` stub that blocks returns within the budget, writes no lock file and leaves `super_read_only` off. `lockForPromote` whose readable list would exceed the size limit is refused and writes nothing. A seen file at the size limit is still readable after the next record.

### SA-5 (Medium) — one-shot start events are lost when the panel session is not up

**Attacker.** None needed; this is a monitoring failure in the path the feature exists for.

**Where.**
- `cli.go:152`: the driver's emitter is the agent itself. `runtime.go:94-105`: `Emit` returns `ErrOffline` when there is no session; nothing is queued.
- `promote_lock.go:681-692` (`emit`): a failed send is logged ("event not sent") and dropped.
- `quarantine.go:315-318`: `qStartPending` is cleared before `reportQuarantineStart` sends. `startlock_guard.go:385`: one `emit`, no retry.
- `startlock_guard.go:307-309`: `bootFailAt` is advanced before the send, so the same log line is never reported again.
- `cli.go:167-171`: the monitor (and with it the guard loop) starts at the same time as the connection loop, so the first guard tick normally runs before the WebSocket session exists.
- Panel side (`monitor/promote.go:569-612`): the `master_start_late`, bypass and start-failed alerts are opened only from these events; the health snapshot carries no "last start" result.

**Path.** (a) The old master's host reboots: mysqld and the agent start together, the guard's first tick finds MySQL up, PERSISTs read-only, computes `late` or `bypassed`, and emits while the agent is still dialing the panel. (b) The old master is cut off from the panel (the usual situation around an emergency promotion) and its MySQL restarts.

**Harm.** The panel never learns that the quarantined old master was writable for a while (`late`) or started without the start lock (`bypassed`). Those two results are the stated mitigation for R-097 (class A, Tinggi) and the trigger for "jalankan Periksa data tertinggal". Without the alert an operator may rebuild the old master without salvaging what was written in that window. It needs the first line (the start lock) to have failed as well, hence Medium.

**Fix, proportionate.** Keep the computed event until `Emit` succeeds (clear `qStartPending` / advance `bootFailAt` only then, retry each round), or carry the last start result (`result`, `init_file`, time) in `HealthSnapshot.StartLock`, which already travels with every report and its backlog. The second form also survives an agent restart if the result is written beside the pins.

**Test.** An emitter that returns `ErrOffline` for the first two ticks and then succeeds: the panel-side fake receives exactly one `monitor.quarantineStart` with result `late`.

### SA-6 (Medium) — the guard opens the MySQL error log as root with a plain open

**Attacker and position.** The `mysql` OS user on a quarantined old master (for example after an exploit of mysqld, or a job left behind), while MySQL is down.

**Where.** `startlock_guard.go:272-311` (`watchBootFailure`) takes the path from `log_error` in `--print-defaults`; a relative value is joined to the data directory (`startlock_guard.go:288-294`). `startlock_guard.go:313-331` (`readTail`) calls `os.Open(path)` and reads up to 64 KiB. There is no `O_NOFOLLOW`, no `O_NONBLOCK`, no check that the opened file is regular, and no owner check. On Debian and Ubuntu the log is `/var/log/mysql/error.log` in a directory the `mysql` user owns; with a relative `log_error` it is in the data directory, which that user also owns. The call runs inside `GuardTick` with `promoteMu` held (`promote_lock.go:477-484`).

**Path.**
1. The attacker replaces the error log with a FIFO. On the next guard round `open(2)` blocks until a writer appears, which never happens. The context cannot interrupt it.
2. The guard goroutine never returns: no further guard round runs (start lock upkeep, the quarantine guard, the promote lock guard, the dump-lock breaker and replica recording all share that loop, `monitor.go:318-330`), and `promoteMu` is never released, so `mysql.quarantine`, `mysql.lockForPromote`, `mysql.unlockPromote`, `mysql.repoint` and `mysql.promote` block until their request times out. The health loop keeps reporting, so the agent looks alive and the last start lock state stays "locked".
3. Variant: a symlink to a device node. Root opens it; for some devices opening and closing has effects (a watchdog device arms on open and resets the machine when closed without the magic byte).

**Harm.** A lower-privileged local account switches off the agent's safety loop without any alert. On its own this does not open the master (the start lock still applies at the next start), but it removes the second line: a MySQL started outside the service's options by that same account is never re-locked or reported. No content of the opened file leaves the agent (only a timestamp is ever sent), so arbitrary-file read is not the issue.

**Fix, proportionate.** Open with `O_RDONLY|O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, `fstat` the descriptor and continue only for a regular file; treat anything else as "log not readable" with a rate-limited warning. Optionally resolve one level of symlink only when the link and its target are root-owned.

**Test.** `watchBootFailure` with the log path pointing at a FIFO returns within the tick and emits nothing; with a symlink to another file it reads nothing.

### SA-7 (Low) — start lock file: operations that follow symlinks, and no check on the directory

**Where.** `startlock.go:392-422` (`writeStartLockFile`): `Lstat` says regular, then `os.ReadFile(path)`, `os.Chmod(path, 0o644)` (line 402) and `os.Chown(path, 0, 0)` (line 409) all follow a link put there in between. `configure.go:320-345` (`writeConfigBlock`) calls the same function for any absolute path outside the data directory that an existing `init_file` line of the managed config names. Nowhere is the directory (include dir, or the main option file's directory) checked for owner and mode.

**Attacker and position.** A local user who can write the directory that holds the lock file. With stock packages that is root only, so this is not reachable; it becomes reachable on layouts where the option file's directory belongs to the `mysql` user (tarball and some panel-managed installs, the same layouts R-056 is about).

**Harm there.** Root can be made to `chmod 0644` or `chown root:root` a file of the attacker's choice (for example make a root-only file world-readable), and the lock file itself can be deleted or replaced between guard rounds.

**Relation to the ledger.** Item m11 records the `Lstat`→`Chmod` race and dismisses it because the directory is root's. That assumption is not checked by code, and the `Chown` and the config-named path are not in m11.

**Fix.** Before placing the lock, require the directory and its parents to be root-owned and not group- or other-writable; otherwise `StartLocked=false` with that reason. Operate on a descriptor opened with `O_NOFOLLOW` (`Fchmod`, `Fchown`) instead of on the path.

**Test.** A lock path whose directory is owned by another uid: state not locked with the reason, and no chmod is applied to a link target planted there.

### SA-8 (Low) — the start lock is not on disk when the command answers

**Where.** `realhost.go:186-221` (`writeFileAtomic`) syncs the file and renames it but does not sync the directory; `writePin` does (`pins.go:920-923`). The start lock file (`startlock.go:413`) and the managed config (`realhost.go:110,127`) both go through `writeFileAtomic`. `promote_files.go:266-279` (`ClearLock`) removes the promote lock without `syncDir`, unlike `ClearPromoting` beside it.

**Path and harm.** `mysql.quarantine` on a server whose MySQL is down answers `StartLocked=true`; the pin is durable, the lock file and the line may not be for a few seconds. A power loss in that window (the kind of event these failovers happen around) brings the host back quarantined but without a start lock, and MySQL starts as its persisted values say until the agent's first guard round. Both renames are in one directory, so journaling keeps their order: a line without its file is not a possible outcome. For `ClearLock` the direction is safe: a lock that reappears after a crash keeps the master read-only until someone unlocks it.

**Fix.** `syncDir` after the rename in `writeFileAtomic` (or in the two start lock call sites), and after the removal in `ClearLock` and `releaseStartLock`.

**Test.** Unit test with a hook counting directory syncs for the write and release paths.

### SA-9 (Low) — `StartLocked` is computed in the agent's environment, not the service's

**Where.** `startlock.go:436-441`: with the usual several default option files the arguments are just `--print-defaults`. `startlock.go:459`: it runs through `Host.Exec`, which inherits the agent's environment (`command.go:16-20`). The agent unit runs as root with `ProtectHome=yes` (`scripts/systemd/dbpanel-agent.service`). mysqld's default list ends with `~/.my.cnf`, resolved from `HOME`.

**Path.** The MySQL service runs as the `mysql` account, whose home on RHEL-family packages is the data directory. A `.my.cnf` there with a `[mysqld]` group is read last by the service and can set another `init_file` (or switch it off); the agent's `--print-defaults` reads root's (empty, protected) home instead and reports the agent's line as last. Who can write that file: the `mysql` OS user, or a MySQL account with FILE where `secure_file_priv` is empty.

**Harm.** `StartLocked=true` while the next start does not use the lock. The guard reports `bypassed`/`late` once MySQL is up and the agent is connected (subject to SA-5). This is the R-097 class reached through a file that is not root's; the code comment at `startlock.go:445-449` argues the check is conservative, which is not true for this case.

**Fix.** Run `--print-defaults` with `HOME` set to the home of the account in `--user` (and ideally with that account's uid), or read `<home>/.my.cnf` explicitly and report not locked when it holds an `init_file`. See also the unconfirmed item on `loose-`/`skip-` forms in section 5.

**Test.** `readEffectiveOptions` with a fake mysqld that prints its `HOME`: it is the service account's home.

### SA-10 (Low) — nothing bounds what a peer sends back

**Where.** `peer.go:53-96`: the read has a 5 s timeout but no limit on bytes. Checked in the driver (`packets.go:40-110`): `readPacket` has no maximum size for what it receives (the `maxAllowedPacket` check is on writes only). `peer.go:57`: the driver's error text, which includes the server's error message, becomes `PeerState.Err`. `promote_lock.go:559`: that text goes whole into the log and into the `kept` event's reason. `promote_lock.go:347-374`: sixteen reads run at once.

**Path and harm.** A hostile endpoint at a recorded address (SA-3's squatter, or SA-4's attacker) answers with very large packets: memory use in the root agent on the master's host is bounded only by bandwidth × 5 s × 16. A multi-megabyte error message makes the `kept` event exceed the 1 MiB frame limit, so it is dropped and, because `keptAt` was already set, not tried again for an hour.

**Fix.** Wrap the connection (the driver's dial hook) in a reader that fails after, say, 64 KiB; cut `PeerState.Err` to a few hundred bytes.

**Test.** A fake server that streams 10 MiB: `ReadPeer` fails early and `Err` is short.

### SA-11 (Low) — the local unlock paths do not say what they bypass

**Where.** `cli.go:399-430` (`unlock-master`): on a pinned master it opens writes and removes the lock whatever the lock says (candidate, deadline, listed replicas). `cli.go:436-458` (`forget-replica`): removes the host and prints a count. `promote_files.go:218-221`: a lock that cannot be parsed tells the operator "hapus dengan: sudo dbpanel agent unlock-master".

**Harm.** Both commands are the documented way out of a kept lock, and a kept lock means "a replica that may have been promoted cannot be read". An operator who runs `unlock-master` while that replica is in fact a writable master creates two masters; `forget-replica` for that host lets the guard do the same a minute later. The commands are root's decision by design, but they give no information at the moment of the decision, and the parse-error hint points at the command that opens writes even when the right action is to remove the file and quarantine.

**Fix.** `unlock-master` prints the lock it is about to remove (candidate, deadline, listed replicas) and, for a lock with a candidate, one sentence of warning; ask for `--yes` in that case. `forget-replica` prints that the guard will no longer wait for that host. Reword the parse-error hint so it does not present opening as the repair.

**Test.** CLI output tests for the three texts; `unlock-master` without `--yes` on a candidate lock changes nothing.

### SA-12 (Info) — the lock path is written into an option file unescaped

`startlock.go:290-320` writes `init_file=<path>`; `startlock.go:229-244` parses a value by cutting at `#` unless quoted. A directory name containing `#`, a quote or a backslash would be cut by MySQL (the server then refuses to start on a file that does not exist) and by the agent's own parser (it then never recognises its line and appends one more on every retry). The path comes from root-owned option files only (`!includedir` or the option file's own directory), so there is no attacker; a refusal to place the lock in such a directory would be cleaner than the failure mode.

## 4. What was checked and found sound

Injection (A03)
- No new SQL is built from a value that comes from outside the agent. `connections.go:68-93`: the two connection queries are constants; the tag and the excluded instrument names are bound parameters (`connections.go:117-146`). `startlock_guard.go:198-217`, `quarantine.go:284,306`, `status.go:238`: constants. `dropDatabases` keeps its name validation (`databases.go:43-66`).
- The only option-file line the new code writes is `init_file=<path>` with the path from the discovered layout (`startlock.go:146-170`); a relative directory and anything inside the data directory are refused. No panel parameter, query result or CLI argument reaches the managed config. Lines kept by `writeConfigBlock` are re-read from the root-owned managed config itself.
- `mysql.recordReplicas` rejects every parameter (`promote_files.go:603-615`); pinned by `TestRecordReplicasCommandTakesNoData`.
- Hosts read from the processlist are stored one per line only when they contain no space or control character (`promote_files.go:412-414`), so a host cannot add a line to replicas-seen; ports are integers 1–65535 (`promote_files.go:484`). Entries of the lock's `Readable` list are validated again when read back and a bad entry counts as unreadable, which keeps the lock (`promote_lock.go:312-340`).
- Child processes: `mysqld` is run with exactly `--print-defaults`, optionally preceded by `--defaults-file=<ConfigFile>` (`startlock.go:436-441`); no outside value is in the arguments.
- `--print-defaults` parsing (`startlock.go:462-494`): a value that contains ` --init-file=…` can only add an entry, which makes the lock look not held; it cannot hide a real later `init_file` written in the plain form.
- A forged "bootstrap file" line in the error log needs the line to end exactly with the lock path and start with a timestamp newer than the lock file (`startlock_guard.go:339-359`); the result is at most a false alarm.

Root file operations (A01/A05)
- Managed config writers: only `configure` (`configure.go:320-345`), `writeStartLock` (`startlock.go:360-385`) and `releaseStartLock` (`startlock_guard.go:54-97`) write it, all under `lockManagedConfig`; `configure` keeps every agent `init_file` line and rewrites its file first. Pinned by `TestConfigureKeepsPresentStartLockLine` and `TestConfigureKeepsStaleStartLockLine`.
- Order: file then line on write, line then file on release, with a re-read before the file is removed (`startlock_guard.go:78-86`). Pinned by `TestStartLockFileBeforeLine` and `TestStartLockReleaseInterruptedNeverLeavesLineWithoutFile`.
- The final write of the lock file is a temp file plus rename, so a symlink at the final name is replaced, not followed (`realhost.go:186-221`), and the owner is set with `Lchown` afterwards (`startlock.go:416-420`).
- Flock files are opened with `O_NOFOLLOW`, created 0600 and close-on-exec, so children do not inherit the lock and a non-root user cannot open them to hold it (`startlock.go:97-123`). The config lock is never placed in the data directory or the include directory (`startlock.go:63-84`).
- Pins, the promote lock and the seen files are read through `readTrustedFile` (regular file, root-owned, not group/other-writable, size-bounded: `pins.go:288-318`) and written with `writePin` (temp, fsync, rename, directory sync: `pins.go:894-925`). `/var/lib/dbpanel` is root's, 0711 (`scripts/install.sh:35,216`), so the panel's user cannot plant the seen files.
- Error-log read is bounded to 64 KiB (`startlock_guard.go:28-32,313-331`); its content is never sent, only a timestamp.

Fail-open review (A04)
- Start lock release needs MySQL to answer: with MySQL down, `set-role replica`/`unassigned` keeps the lock (`startlock_guard.go:153-157,179-193`; `TestSetRoleReplicaWhileDownKeepsStartLock`). The proof is dropped once either flag is off (`TestReleaseProofLostOnceWritesOpen`). `persistedReadOnlyOn` treats a missing row or a read error as not proved (`startlock_guard.go:198-217`).
- Untrusted pins (`Role()==""`): the start lock guard changes nothing (`startlock_guard.go:118-121`).
- `mysql.promote` runs the pin check before `releaseForPromote` and PERSISTs read-only before releasing (`promote.go:119-131`, `startlock_guard.go:392-418`), so a quarantined server cannot use that route.
- `lockForPromote` refuses when the fan-out was cut short by the context (`promote_lock.go:211-215`), so a truncated list is never stored. The guard treats "not read in time" as unreadable and keeps the lock (`promote_lock.go:347-374`, `591-595`). The unlock runs on its own context (`promote_lock.go:599-603`; `TestUnlockUsesOwnContext`).
- `ForgetLockHost` re-reads under the flock and never writes back a lock removed or replaced meanwhile (`promote_files.go:235-262`; `TestForgetLockHostKeepsLockReplacedMeanwhile`).
- Time: the retry and check timers compare times that carry Go's monotonic reading, so a wall-clock step does not suspend start lock retries; `checkStartLock` also handles a clock that went back (`startlock_guard.go:251-257`).
- `mysql.dumpFrom` refuses `drop_existing` before anything runs (`snapshot.go:132-135`; `TestDumpFromRefusesDropExisting`); `mysql.dropDatabases` is the only drop path and is subject to the pins.

Credentials (A02/A07)
- No password appears in argv, in an error string or in an event: `validatePeer` and `ReadPeer` build errors from host, port and the driver's error only (`peer.go:56-58,145-170`); the candidate's credential lives in memory only (`promote_lock.go:67-71`); the lock file holds addresses, not credentials.
- With `Verify`, the chain and the recorded host name are checked before authentication (`peer.go:64-77`).

Information exposure (A09/A01)
- `mysql.connections` selects thread name, host, user, default database and two booleans; statement text (`INFO`) is not read (`connections.go:68-93`). Hidden by name are exactly `dbpanel_repl` and `^dbpanel_dump_[0-9a-f]{8}$` (`connections.go:217-219`); `dbpanel_admin` is shown.
- New events carry paths, a result word, a time and host:port lists; no log excerpt, no SQL.

Resources and concurrency (A04)
- The guard loop pauses after a round finishes, so rounds cannot overlap (`monitor.go:318-330`); each guard call has its own timeout; `readPeers` waits for every read it started.
- Lock order is consistent: `promoteMu` → `managedConfigMu` → config flock; the promote-lock flock is taken alone; `Pins.mu`, `seenMu`, `startLockMu` are leaves. I found no inversion between guard, command handlers and CLI.
- Guard warnings from the monitor are limited to one per minute per text (`monitor.go:332-362`).

CLI and scripts (A05/A08)
- `unlock-master` refuses on a replica or quarantined server and when the pins cannot be read (`cli.go:404-418`). `set-role master` changes nothing when the start lock cannot be released (`cli.go:258-263`; `TestSetRoleMasterRefusedWhileConfigLocked`). Run without root, the CLI fails on reading the agent directory before it changes anything.
- `scripts/`: the diff adds shellcheck directives and `${VAR:?}` guards on two `rm -rf` lines in the test script; no behaviour change in `install.sh`.

Protocol compatibility (spec 5.8)
- `check_every_sec` above 300 from an older panel is accepted and applied as 300 (`monitor.go:234-256`).
- An older panel that sends `drop_existing` gets a refusal, not a load on top of existing data.
- A lock written by an older agent has no `Readable`; after the agent restart that an upgrade implies, the guard has no credential and keeps the lock (`promote_lock.go:544-548`), which is the safe side.
- New result fields and events are additive; an older panel ignores them. Whether the new panel treats their absence as "not guaranteed" is the other seat's.

## 5. Unconfirmed suspicions

1. **`loose-init-file` / `skip-init-file`.** `readEffectiveOptions` recognises only `init_file`/`init-file` (`startlock.go:477-486`). MySQL accepts a `loose-` prefix on any option and, I believe, a `skip-` prefix that resets a string option. If an operator's file read after the agent's line uses one of those forms, the agent would report the lock as held while mysqld uses another file or none. Checked: the parser. Not checked: what `mysqld --print-defaults` prints for those forms and how mysqld treats `skip-init-file` (needs a run of mysqld).
2. **`startLockProved` survives a role change made through the CLI.** It is reset only by a quarantine written while MySQL is down (`quarantine.go:162`). A sequence in one agent run of quarantine → rebuilt → promoted → `set-role quarantined` by CLI → `set-role replica` by CLI could let the guard accept an old proof if both flags happen to be on while the persisted values are off. I could not build a realistic sequence in which the flags are on without being persisted, so this stays a suspicion; clearing the flag whenever the guard sees the role enter `quarantined` would remove the question.
3. **`set-role master` racing the guard.** The CLI releases the start lock and then writes the role. A guard round in between still sees `quarantined` and writes the lock again; the next round (role master) releases it. If MySQL restarts inside that window (at most about 5 s) the init file runs and PERSISTs read-only on a server root has just pinned master. Read from the code (`cli.go:258-268`, `startlock_guard.go:223-247`); not demonstrated.
4. **Guard starvation by a panel.** `GuardTick` skips its round while `promoteMu` is held (`promote_lock.go:477-482`). On a quarantined server the only panel command that takes it is `mysql.quarantine`, which itself re-PERSISTs read-only and rewrites the start lock, so I see no harm; on a master, `lockForPromote` can hold it for up to 10 minutes (SA-4). I did not measure how long a tight loop of such commands can keep the guard from running.
5. **Wall-clock deadline.** The promote lock's deadline is compared with the wall clock (`promote_lock.go:512`). A forward clock step on the master during a planned promotion would make the guard start its expiry reads early. The candidate is read twice a minute apart and the candidate's own agent checks the old master before opening, so I do not think this opens two masters, but I did not trace the candidate side. Pre-existing (4b).

## 6. Residual risks that are inherent or accepted

Already in the inventory and unchanged by what I found:
- R-097 (service command-line `--init-file` beats the lock; reported after the start). Worse than recorded in two ways: SA-5 (the report can be lost) and SA-9 (a non-root file can have the same effect on RHEL-family layouts).
- R-098 (a master stays read-only while a listed replica is unreadable). Worse than recorded: the party that can cause it is not only a compromised panel; any holder of the replication password can get a host listed and then stop answering (SA-4).
- R-102 (more listed replicas than fit one guard round keep the lock). Same root cause as SA-4; SA-4 shows the list length is attacker-controlled.
- R-108 (a replica unreadable at lock time is never read by the guard), R-095 (forged `program_name`), R-096 (AppArmor/SELinux untested), R-099 (agent and CLI may discover different layouts), R-100/R-101 (connection list edges), R-103 (flocks are advisory and need both sides upgraded), R-038 and R-039 (shared replication account; optional verification). SA-3 makes R-039 worse: no on-path position is needed.
- Ledger items m6 (owner/mode of an existing flock file not checked) and m11 (see SA-7) stand as recorded, with SA-7's additions.

Not in the inventory and should be:
- SA-1 and SA-2 (class B, Tinggi): what a compromised panel can do to a quarantined old master and to a healthy master. Spec 5a §7 says the compromised-panel class is unchanged, which is accurate, but neither path has a row, and SA-2 contradicts the stated reason for the "no hosts from the panel" rule.
- SA-3 (class C, Tinggi by the inventory's own definition, since a credential leaks).
- SA-5 and SA-6 (class C, Sedang).

Inherent and acceptable as designed:
- The start lock is a defence against a restart without the agent, not against root or the `mysql` OS user on the host; the latter can start a mysqld of its own on the data directory. The guard's 5 s re-lock is the only answer to that, which is why SA-6 matters.
- Events to the panel are best effort by design; state that must not be lost has to travel in the health snapshot (SA-5's fix).
