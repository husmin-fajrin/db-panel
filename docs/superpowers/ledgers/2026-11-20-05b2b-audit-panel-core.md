# Audit notes — panel core (orchestrator, monitor, store, redact, hub) — Plan 5b-2 (head b950757)

Auditor: panel core. Read-only; the working tree, index and HEAD were not changed (checked with `git status` after the
one test run). Findings are numbered PC-1 … PC-13.

## Ground covered (files read in full; what was run)

Read in full at b950757:

- orchestrator: `gate.go`, `gate_port.go`, `runner.go`, `approval.go`, `create_replica.go`, `promote.go`,
  `promote_view.go`, `oldmaster.go`, `controls.go`, `tls.go`, `resync.go`, `change_databases.go`, `prepare_master.go`,
  `agentjob.go` (there is no `gate_*.go` besides `gate_port.go`).
- monitor: `monitor.go`, `state.go`, `alerts.go`, `promote.go`, `start_state.go`, `settings.go`; `status.go:96-300`.
  There is no `monitor/agenttext.go`: `AgentText` is alerts.go:472 (`agentText` :479).
- store: migrations `0009`, `0012`, `0013`; `migrate.go`, `backup.go`, `servers.go`, `topologies.go`, `audit.go`,
  `audit_chain.go`; `jobs.go:200-260` (active/newer job queries).
- `redact/redact.go`; hub `hub.go`, `conn_ws.go`.
- Only as far as a panel-core rule rests on it: `listener_impl.go:14-75,150-200`, `agentapi/ws.go`,
  `agentapi/pair.go` (where `servers.address` comes from), `notify/message.go:128-160`, `notify/notifier.go:300-545`,
  `notify/telegram.go:40-110`, web `audit.go`, `handlers_gate.go`, `handlers_promote.go:245-395`,
  `handlers_jobs.go` (`confirmedJobAction`, `jobAction`), `handlers_mysql.go:455-530` (`setMasterSource`),
  `backup_folder.go:120-200`, `handlers_settings.go:520-600`; agent `monitor/backup.go:340-440`,
  `monitor/monitor.go:245-295`, `drivers/mysql/promote.go:380-430`.
- Documents: audit-common.md, rulings.md (R1-R36; R37-R41 only as restated in final-fix-wave.md),
  final-review-panel-core.md, final-fix-wave.md, final-fix-report.md, the plan (Global Constraints, K1-K11, Tasks 9-12,
  "Audit keamanan", "Perubahan selama pelaksanaan"), spec 5.7-5.9 and sections 7-8, the 5b-2a spec sections 8 and 11,
  the 5b-1 audit entries SEC-204…SEC-214, the 5b-2a panel final review, README "Keamanan" lines 4252-4276.

Run: `graphify query` (five times, before the greps and reads they preceded). One throw-away test through
`go test -overlay` (the test file lives in the scratch directory
`(scratch directory)`,
nothing written to the repository; pure orchestrator package with fake agents, no MySQL, 0.18 s) to confirm PC-1.
No other test was run.

## Findings

### PC-1 — "Ulangi" of a failed promote job runs the promotion again with none of the start's checks — Sedang (upper edge)

- Attacker: an operator in a hurry (one click); also anyone at a logged-in session (no password is asked).
- Where: `orchestrator/runner.go:967-1028` (`Retry`), `:709-724` (`gateActionForJob`: no promote kind), `:653-668`
  (`dropsData` false for promote → `confirmDrop` asks nothing, `:779-783`); `promote.go:1161-1192`
  (`promoteSupersededBy` is the only check); web `handlers_jobs.go:483-531` (`confirmedJobAction`: gate, token and
  password only `if drops`). Test that pins the behaviour for the planned kind: `runner_test.go:229`
  `TestPromoteRetryStillTokenless`.
- What happens: a promote job that failed BEFORE the point of no return (nothing changed; e.g. the candidate's agent
  refused `mysql.promote` because the old master still answered, or the catch-up ran out of time) stays "gagal" and
  can be retried for as long as it is the topology's latest promotion and no newer job exists on the master. Retry
  then starts again at the failed step, for the emergency kind at step 1 "Jadikan master baru":
  - no dead-master tick (D5), no typed name, no password, no confirmation token;
  - the panel's mode is not read again: the job stays `promote_emergency` although the master is connected and
    "Sehat" (a new emergency start at that moment is refused with "master masih sehat; jalankan promote terencana");
  - `PromoteView` is not read again: candidate eligibility, "most current", branched, writable replicas, unreachable
    replicas are whatever they were when the job was created;
  - for the planned kind: no gate (K4), no backup tick;
  - no age limit (`confirmMaxAge` applies only to data-dropping jobs): days or weeks later is the same.
  What still stands: the candidate's own agent probes the old master three times, 10 s apart, and refuses when it
  answers or when its IO thread is still connected (agent `promote.go:387-420`); for the planned kind the master's
  agent takes the lock itself.
- Steps (emergency): 1. The panel loses the master's agent (network between panel and master), the operator starts an
  emergency promote with the tick; the candidate's agent refuses because the master answers; the job is "gagal", the
  `job_failed` alert stays open. 2. Any later time, a logged-in user clicks "Ulangi dari tahap ini" on that job.
  3. If at that moment the candidate cannot reach the master's MySQL for about 25 s and its receiver is not "Yes"
  (a replica that was stopped, or a network fault between the two servers) while applications still reach the master:
  the candidate opens writes, the topology is swapped, the old master is quarantined when its agent is reachable.
- Impact: the plan's rule "an emergency promote needs the dead-master tick" and the panel's own emergency/planned
  decision do not hold on this path; the agent's probe is the only check left. Outcome when the probe is fooled by a
  partition: two masters for a while, writes on the old master that are not on the new one. For the planned kind: the
  gate's promote rows (clock, TLS, pin, account, backup) and the backup tick are skipped on a retry; README "Keamanan"
  says of the gate "Tidak ada tombol untuk melewatinya", which this contradicts.
- Confirmed: by running. Output of the overlay test: `a new emergency start now: master masih sehat; jalankan promote
  terencana` … `job … kind promote_emergency ended succeeded; mysql.promote sent 2 times, last mode "emergency";
  topology master now 2 (old master 1, candidate 2)` after `Retry(id, JobAck{})` with the master attached and healthy.
  (The fake agent accepts; a real agent runs its probe.) The planned variant is confirmed by the repository's own
  `TestPromoteRetryStillTokenless` and `TestRetryGatedOnlyWhenDataIsDropped`.
- Smallest fix: in `Retry`, a promote job whose `mysql.promote` was never sent (`!st.PromoteSent && !st.PastPoint`) is
  refused with "mulai promote baru dari layar Promote" (a new start is always allowed after a failed promotion; it
  brings mode, tick, name, password and gate with it). Jobs past that point keep today's tokenless Retry, which they
  need. Alternative: keep Retry but require for that case the same as a start (mode re-read, tick, confirmation; gate
  for planned).
- Needs a decision changed: yes for the planned kind (K4 "Retry/Approve pekerjaan promote tidak pernah digerbang", and
  the older rule "a promote's Retry needs no confirmation"); the emergency part contradicts no written decision (K4 is
  about rows) but is new behaviour. Stop-and-ask item for the owner.
- Why not Tinggi: the agent, which the trust model names as the safety boundary, still checks; what is missing is
  every panel-side confirmation. Why upper edge: those confirmations are what this plan added for exactly this case,
  and the remaining guard is one click away from being the only one.

### PC-2 — One agent that does not answer holds the panel-wide `repointMu` for up to 5 minutes; a pending quarantine of another server waits behind it and is not retried — Sedang

- Attacker: a compromised agent or database server (a replica in `needs_repoint`, or a quarantined server whose
  quarantine is pending); also no attacker: a slow or hung agent.
- Where: `orchestrator/promote.go:1470-1545` (`onOnline` takes `repointMu` at :1483 for EVERY server that connects,
  before it knows whether anything is to be sent; then `sendQuarantine` :1246 = `mysql.tlsTrust` (2 min) +
  `mysql.quarantine` (2 min), or `repointReplica` :1282 = `mysql.tlsTrust` + `mysql.repoint` (3 min), all under the
  lock, bounded only by `onlineHookTimeout` 5 min :47); the same lock in `promoteQuarantine` :813 and `promoteRepoint`
  :908 (job steps) and `RepointNow` :1395. The lock is a plain `sync.Mutex`: a waiter cannot give up with its context.
- Steps: 1. After a promotion, replica X stays `needs_repoint` (its agent refuses or was away: `repointReplica`
  :1324-1325 keeps the state on any error). 2. X's agent connects and never answers `mysql.repoint`/`mysql.tlsTrust`:
  the lock is held up to 5 minutes; X reconnects to do it again. 3. Meanwhile the old master M of an emergency promote
  comes back (writable, never quarantined) and its agent connects: its `onOnline` waits for the lock; so do the
  quarantine and repoint steps of any running promote job, of any topology.
- Impact: the quarantine of another server is delayed by up to 5 minutes per hold (repeatable; the Go mutex hands the
  lock to the longest waiter, so M is served at the next release, not starved for ever). During that time M takes
  writes beside the new master. Second effect, without any attacker: M's 5-minute context started when M connected, so
  what is left of it after the wait bounds its own `tlsTrust`+`quarantine`; when that fails (or any transient error),
  the quarantine stays pending and NOTHING sends it again until M's agent reconnects (`sendQuarantine` has two callers:
  the job step and `onOnline`). A promote job whose quarantine was skipped because the agent was offline has ended
  "succeeded", so there is no "Ulangi" either; the operator is left with guide 9 by hand.
- Confirmed: by reading only. A test with two fake agents (X holding `mysql.repoint`, M connecting) would confirm the
  wait; the 5a audit's FR-12 moved a 10-second call out of this lock for the same reason.
- Smallest fix: (a) in `onOnline` decide from the store first and take the lock only when a command will be sent;
  (b) make the lock per topology (or per target server), not panel-wide; (c) retry a pending quarantine from a timer
  (the Monitor's tick already knows `quarantinePending`) while the agent is connected, not only on connect.
- Needs a decision changed: no.

### PC-3 — `master_unpinned` puts the agent's role pin into the alert text raw and unbounded — Sedang

- Attacker: a compromised agent of a topology's master.
- Where: `monitor/alerts.go:569` (`unpinnedMessage`): `"… peran yang dipin di server ini " + snap.RolePin + ", bukan
  master."`. `RolePin` is the health report's field; nothing bounds or cleans it on the way (`HandleEvent`
  monitor.go:277-299 checks only 64 KiB for the whole payload; `redactSnapshot` state.go:222 censors three other
  fields). Every other agent string in an alert goes through `agentText` (alerts.go:505, 516, 530; promote.go:286, 391).
- Steps: the agent sends three live reports with `role_pin` = any text up to about 64 KiB, with line breaks, bidi
  controls, sentences that read as the panel's. The alert opens (`unpinnedReports > 2`, alerts.go:329) with that text.
- Impact: (1) the text is stored in `alerts.message`, shown on the alerts pages and sent by Telegram as plain text
  directly after the panel's words, unlabelled: an agent can write instructions in the panel's voice next to a real
  advice that names `sudo dbpanel agent set-role master`. (2) with more than about 4,000 characters Telegram refuses
  the message, the alert stays "gagal terkirim" and is retried each minute: the agent keeps its own Critical alert out
  of Telegram (it could also simply report `master`; the point is the broken rule). The trust model's rule "text
  reported by an agent is bounded (`monitor.AgentText`) and escaped before it is shown or stored" is not met here.
  Other alerts are not held up: the notifier sends each alert on its own (notifier.go:339-351).
- Confirmed: by reading only.
- Smallest fix: `agentText(snap.RolePin, 32)` in `unpinnedMessage`, with the "tidak dikenal (…)" form the server page
  already uses for an unknown pin (R9); a test with a 5,000-character pin.
- Needs a decision changed: no.

### PC-4 — The gate judges disk, performance_schema, clock and master pin from the last stored report whatever its age — Rendah

- Attacker: someone who can delay an agent's reports (network between agent and panel); a compromised agent that
  connects and stays silent; no attacker: the seconds after a reconnect or a panel restart.
- Where: `orchestrator/gate.go:430-441` (clock), `:493-509` (disk), `:559-569` (performance_schema): the only
  conditions are "agent connected" and "field present"; `:409-423` (master pin) not even "connected". `View.ReceivedAt`
  and `View.Status` (which turns "Agent offline" after `StaleAfter`) are not read. `Monitor.load` (state.go:127-169)
  restores the stored snapshot at start, and `OnOnline` (monitor.go:307) sets `online` before the first live report.
- Effect: a row is green on data that is minutes to days old: between a reconnect or a panel restart and the answer to
  `monitor.configure` (normally under a second, up to 30 s); for as long as a connected agent sends nothing; with
  "interval ringkasan" at its maximum (3600 s) a report up to an hour old is the normal case. `View.Backlog` is not
  stored (C-7 of the final review, still open), so after a restart a stored backlog snapshot is read as live by
  `ClockSkew`. The backup row is the exception (age extrapolated, offline → yellow, alert only on a current report).
- Impact: Buat replika, Resync, Tambah database start although the disk is now over the threshold (the agent's own
  preflight checks free space again), a planned promote starts on an old clock reading (StartPromote asks for fresh
  reports first, so there the window is small). No data loss found.
- Confirmed: by reading only.
- Smallest fix: in `gateServer`, treat a view as unknown (yellow G10b) when `now − ReceivedAt > StaleAfter` or the
  Monitor's status is `StatusAwaitingReport`/`StatusAgentOffline`; persist `Backlog` or treat a snapshot loaded at
  start as unknown for the clock until a live report arrives.
- Needs a decision changed: no (spec 5.7: unknown is yellow).

### PC-5 — The master-port probe dials an address any logged-in user can set, at once and without limit; the address change itself needs no password — Rendah

- Attacker: a logged-in user (or someone at an unlocked session). Not a compromised agent (see answer 5).
- Where: `orchestrator/gate_port.go:46-48` (address = `Topology.SourceHost:SourcePort`), `:61` (the 10-minute answer
  is kept per master AND address: a new address is probed immediately); web `handlers_mysql.go:494-530`
  (`POST /masters/{id}/source`: `ValidHost` + port 1-65535, no confirmation token, audited as `master_source`).
- Steps: POST source `10.0.0.5:22` (or `127.0.0.1:…`, or a name), GET the promote page or the rebuild form (or a job
  page of a rebuild, `handlers_jobs.go` F1): the yellow row "Port MySQL master … bisa dijangkau dari host panel"
  appears or not. Repeat with the next port.
- Impact: a TCP-connect oracle from the panel host's network position (one SYN, nothing sent, closed at once; result:
  one bit, shown only to logged-in users), one step per POST+GET, each POST audited. While the address is changed, new
  replicas, "Wajibkan pemeriksaan sertifikat TLS" and repoints would be pointed at it (pinned replicas refuse without
  `set-source`; a topology with the master's certificate refuses the name).
- Confirmed: by reading only.
- Smallest fix: keep the answer per master for 10 minutes whatever the address (or probe only an address that has been
  stored for N minutes); put the source-address change behind the confirmation dialog like the other actions that
  change where replication points. The second half is the web auditor's route table; flagged to them.
- Needs a decision changed: no.

### PC-6 — Ways to make a red row not count that are not all written down; one of them skips the backup tick — Rendah

- Attacker: an operator in a hurry.
- Facts (each audited, none asks for the password):
  1. Backup, folder CHANGED to another path (not cleared): the panel forgets the report (R22, state.go:663), the agent
     bumps its folder generation and answers `pending` (agent backup.go:435), the row is yellow "belum diketahui: agent
     … belum melaporkannya" (gate.go:599) and asks for NO tick, until the agent's next check loop scanned the new
     folder: up to "interval pemeriksaan" (15 s default, 300 s maximum) plus the 10 s scan bound. Clearing the folder
     asks for the tick (gate.go:579-583); this path does not. Re-saving the SAME folder does not open the window (the
     agent keeps its generation and answers the old verdict at once; checked in agent monitor.go:281-284).
  2. Backup, folder pointed at any directory holding a fresh non-empty file (the MySQL data directory, a log
     directory): green for good. Limit raised to 8760 hours: green for a stale file.
  3. Disk: "ambang kritis" in Pengaturan may be set up to 100 (settings.go:142, 214): the row is then red only at 100 %.
  4. TLS (promote): "Lepas replika" of the unverified replica takes it out of the count (`ReplicaUnverified` counts
     ready/attention rows only).
  5. Any promote row: "Ulangi" of a failed planned promote (PC-1).
- README "Keamanan" (4261-4265) documents 1-2 as a class ("kuning atau hijau … tercatat di Audit") but not that the
  changed-folder window needs no tick; 3 and 4 are not mentioned; "Tidak ada tombol untuk melewatinya" (4274-4275,
  and "Tidak ada tombol untuk melewati baris merah" at 3430) is not true for 5.
- Impact: the gate "menahan salah langkah"; these are the steps an impatient operator finds. No boundary of the trust
  model is crossed.
- Confirmed: by reading only (agent and panel code for item 1).
- Smallest fix: in `backupRow`, `unknown`/`pending` with a folder set asks for the tick too on planned promote and
  rebuild (`NeedsAck`), so every not-red backup state that is not "ok" is acknowledged; one README sentence for 3 and 4.
- Needs a decision changed: no (spec 5.7 asks the tick "bila cadangan belum diatur"; extending it to "not known" is a
  reading, to be listed for the owner).

### PC-7 — The inventory read on connect (fix wave F2c) has no limit on repetition and is not audited — Rendah

- Attacker: a compromised agent.
- Where: `orchestrator/promote.go:1446-1468` (`refreshUnknownAccount`), `runner.go:268-291` (`inventoryCallCut`).
- Facts: the only condition is "stored inventory connected and account empty". An agent that answers
  `mysql.inventory` with an empty `account` keeps the condition true, so every connect makes the panel call it again
  and store its answer: up to 5,000 databases, any number of `missing_grants`, version and series strings, bounded only
  by the 1 MiB message limit (conn_ws.go:15). There is no connection rate limit (agentapi/ws.go), and the store is
  one connection, so a reconnect loop turns into a queue of writes of up to 1 MiB each (before F2c a connect cost
  about 128 KiB: probe + health row). The stored values are the ones "Segarkan" stores, but at a moment the agent
  chooses and with no audit entry (`mysql_refresh` is audited only from the web).
- Impact: slows the panel for everyone while it lasts; lets the agent rewrite its own inventory (database list, sizes,
  missing grants) between an operator's look at a form and the start that reads it. It gains nothing it could not
  answer on the operator's next "Segarkan" or inside a job. No effect on other servers' rows.
- Confirmed: by reading only.
- Smallest fix: one automatic read per server per panel run (or per 10 minutes), remembered in the Runner; a size cap
  on the stored inventory fields; an info line is enough for the audit question, or record it as `mysql_refresh` by
  "panel".
- Needs a decision changed: no (R40 asked for "once").

### PC-8 — An emergency promote is still refused by things a single other replica reports — Rendah (older rule; named because question 4 asks)

- Attacker: a compromised replica agent other than the candidate; also a misconfigured replica.
- Where: `orchestrator/promote.go:233-238` (any replica whose last report shows `super_read_only` off refuses the
  promotion in BOTH modes), and the other refusals of both modes listed in answer 4.
- Impact: during an outage one replica's report holds the emergency promote of the whole topology until the operator
  understands the message and detaches that replica ("Lepas replika", which needs no agent). The refusal protects
  against an unknown second master, so it is a deliberate rule of plan 4b; the plan's sentence "nothing but the
  dead-master tick can refuse it" is true only of the rows.
- Confirmed: by reading only.
- Smallest fix: say on the emergency screen, before the name is typed, which replica blocks and that detaching it is
  the way on (as was done for the unfinished job, 5b-2a P-I4). Owner's call whether an emergency should accept it with
  a tick.

### PC-9 — A rebuild start on a server that is not quarantined removes its failed replica row before refusing — Rendah

- Attacker: a logged-in user who posts the rebuild flag for an ordinary failed replica.
- Where: `orchestrator/create_replica.go:201-203` (`RemoveReplica` for a failed row), then `:214-215` refuses
  ("server tujuan bukan master lama yang dikarantina"). Before that the gate ran with the rebuild's rows and
  `requireRebuildApproval` passed because an ordinary replica's agent answers "not required".
- Impact: a store write with no job: the failed replica is no longer registered and its role is `unassigned`, without
  the checks of "Lepas replika"; its agent stays pinned as a replica. Known as a deferred minor of Task 10; kept here
  because it is a write after a refusal the caller reads as "nothing happened".
- Confirmed: by reading only.
- Smallest fix: move the role switch (`:212-218`) above `RemoveReplica`.

### PC-10 — Audit entries of the new paths: three gaps in what they say — Info

1. `server_backup` stores `backup_dir` cut at 128 runes (`maxAuditString`, web/audit.go:42, 213), while a folder may
   be 1024 bytes: for the attacker "a compromised panel (or session) chooses the backup folder" the audit does not
   hold the whole path once it is changed back. Fix: add `backup_dir` to `auditFullKeys` (it is bounded at 1024).
2. `master_last_report` is `View.ReceivedAt`, which is the panel's clock MINUS the age the agent stated
   (monitor.go:554, `reportAge` bounds it only at the events' retention): the plan's note 26 and the code comment
   (handlers_promote.go:335-336) say "the panel's own clock, not the agent's". A master's agent can make its last report
   look up to that retention older; never newer.
3. A promote run by "Ulangi" (PC-1) is audited as `job_retry` with job id and kind only: no `old_master_dead`, no
   `master_last_report`, no `skipped_red`. `skipped_red` of a start is read by a separate `Gate` call before
   `StartPromote` and outside `startMu` (handlers_promote.go:349): a record, not a decision.

### PC-11 — Growth a compromised agent can cause in the panel's tables — Info (older; the 5b-2a "Declined to judge" item)

- `alerts` and `status_events`: one open alert per kind and server, but every report may open or close one; the hub
  lets one server send a report every 2 s (hub.go:58-59), so a flapping agent adds on the order of tens of thousands
  of rows a day per kind until the retention prunes them (90/180 days). Only the three start alerts have an open
  limit (start_state.go:237-240). Telegram is bounded by the notifier's flap control.
- `job_log`: while a job waits for an agent job, that agent's `job.log` events are not rate-limited (hub.go:263-269
  limits four other types) and are queued without bound (`liveJob.events`, runner.go:463-472) and written one row
  each (agentjob.go:160-165).
- Every connect writes a status event and a probe row and starts three background calls; no connection rate limit.
- Worst outcome: panel.db and memory grow, the single store connection is busy; no data of another server changes.
- Fix (next plan): a per-server, per-kind open limit like `startOpens` for every rule alert; a cap on queued job
  events and job-log lines per job; a minimum time between two accepted connects of one server.

### PC-12 — `monitor.configure` is sent outside any job and ungated, and now carries a path — Info

`monitor.configure` (monitor.go:435-443) is sent on every connect, every settings save and every backup save, outside
any job and ungated, with the role hint and now the backup folder. The agent treats the folder as untrusted (its own
rule, a refused folder is reported unreadable, the configure never fails): agent auditor's part. Panel side: the folder
is validated by the web form only (`SetServerBackup` stores "as given", store/backup.go:8-11); anything that writes
`servers.backup_dir` otherwise (the CLI, a compromised panel) reaches the agent unchecked, which is why the agent's
rule is the one that counts.

### PC-13 — Small leftovers — Info

- `Monitor.configMu` and `ageClamped` keep an entry for every server ever seen; the tick deletes only `servers` and
  `lastCheck` (monitor.go:680-685). A few bytes per deleted server.
- README "Keamanan" lists the rows a compromised agent can turn green (4270-4271) without the replica half of
  "Pemeriksaan sertifikat TLS" (`tls_verified` follows the replica's own report, monitor.go:605-639) and without
  "Persetujuan di server" (the old master's own answer).
- `Resume` does nothing when `RunningJobs` or `ListJobs` fails (runner.go:1222-1231): one unreadable job row leaves
  every cut-off job "running", which blocks its server (fail closed, needs a database repair).
- The audit hash joins fields with "\n" (audit_chain.go:160-170); the writer strips line breaks from outcome and
  username (web/audit.go:87) and `detail_json` is JSON, so no stored field can hold one today. A second writer would
  have to do the same.

## Answers to the questions

### 1. Does `Gate` have a side door?

No unintended bypass of the rule "a gated start is judged under `startMu`, last before the first store write, and a
gate that cannot judge refuses". One path that the written decisions allow and that I judge wrong for its first half
(PC-1), and one write-before-refusal (PC-9).

| Path | What it does | Gate |
|---|---|---|
| `StartPromote` planned (promote.go:297-306) | role swap, replication repointed | gated, then backup tick |
| `StartPromote` emergency (promote.go:205) | the same | exempt: Global Constraints, spec 5.9; needs the D5 tick |
| `StartCreateReplica` plain / rebuild (create_replica.go:184-194) | drops and overwrites the target | gated (`create_replica` / `rebuild_old_master` with the approval read before), then backup tick; `RemoveReplica` comes after the gate. PC-9 |
| before that, on a rebuild: `requireRebuildApproval` (agent makes a request code), `cancelAgentCopy` (`job.cancel`) at :113-119 | touches the agent before the gate and outside `startMu` | K9 item, re-judged in answer 10 |
| `StartResync` (resync.go:84) | drops and reloads the replica | gated, before `SetReplicaState` |
| `StartAddDatabases` (change_databases.go:114) | loads; may drop existing | gated |
| `StartRemoveDatabases` (:151-156) | drops when asked | gated only with `dropData`; without: exempt (plan Task 10) |
| `StartPrepareMaster` (prepare_master.go:65) | role master, topology | gated |
| `Retry`, `Approve` of create/resync/add/remove jobs (runner.go:927, 994) | run the rest of the job | gated when `dropsData` (spec 5.7), before `confirmDrop`; a rest that drops nothing: exempt by spec |
| `Retry`, `Approve` of promote jobs | runs the promotion again or on | exempt by K4 and spec 5.7; **PC-1**: for a job that never sent `mysql.promote` this is a start without mode, tick, confirmation or gate |
| `StartMySQLTLS`, `RenewMySQLTLS`, `StartTightenTLS` (tls.go) | certificates; `CHANGE REPLICATION SOURCE` with verification to the stored address | exempt: Global Constraints (they are the fix of the TLS row) |
| `StartLeftover`, `StartSalvage` (oldmaster.go:226, 247) | read the old master; write a salvage file | exempt: spec 5.7 (rescue is never held) |
| `SalvageDelete` (oldmaster.go:401) | deletes one salvage file (it may be the only copy of the leftover writes once the old master is rebuilt) | not in the spec's table; not gated; name checked against a strict pattern, refused while the server has a job. Web auditor: is it behind the confirmation? |
| `StopReplica`, `StartReplica`, `Relock` (controls.go:23-61) | stop/start replication, read-only ON | exempt: Global Constraints |
| `UnlockMaster` (controls.go:64-79) | `mysql.setReadOnly{off}` on the topology's CURRENT master | exempt: Global Constraints ("buka kunci master"); a documented unlock path; refused for a server that is no topology's master (so for a quarantined old master after the swap) and while it has a job; the agent refuses it under a quarantine pin |
| `DetachReplica` (controls.go:115) | stop, reset, release role; row removed | exempt: Global Constraints; refused for an old master in quarantine |
| `DeleteServer`, `DeleteUnquarantined` (controls.go:175-220) | `job.cancel`, `releaseRole` (not for a quarantined server), row deleted | exempt: Global Constraints; refusals F8 etc. read and found in place |
| `Cancel` → `cancelJob` (runner.go:1174, 1431) | `job.cancel`, `resetReplica`, `releaseRole`, `unlockPromote` | exempt: Global Constraints; a promote past `PromoteSent` cannot be canceled |
| `RepointNow`, `OnOnline` → `mysql.repoint`, `mysql.quarantine`, `mysql.recordReplicas` (promote.go:1366, 1432) | continuation of a promotion | exempt: Global Constraints ("arahkan ulang"); the direction of quarantine is the safe one. PC-2 |
| `ConfirmCutover` (promote.go:1552) | store mark | not in the table |
| `Connect`, `RefreshInventory`, `refreshUnknownAccount` | account handed to the agent / inventory read | not gated: it is the fix of the account row. PC-7 |
| Monitor `monitor.configure` | settings, role hint for an unpinned agent, backup folder | not gated; older; PC-12 |
| `Resume` (runner.go:1200) | fails cut-off jobs, normalises old rows | no agent command except restarting a replica a cut-off TLS redirect left stopped |
| web `POST /masters/{id}/source` → `SetTopologySource` | changes where every later copy, tighten and repoint points | not gated, no confirmation, audited; older than this plan. PC-5 |

The older data-dropping commands (`mysql.dropDatabases` in create_replica.go:770, change_databases.go:409 and :580;
`cloneFrom`/`dumpFrom`; `mysql.resetReplica`; `mysql.configure`) are sent only from steps of jobs whose start, Retry
and Approve are in the first rows. The copy step runs the preflight again in every new run before it drops
(`preflightBeforeDrop` create_replica.go:843) and add_databases reads the replica again (`dropAddExisting` :553).

What a logged-in operator can do to make a red row not count without fixing the cause: see PC-6 (five ways; all
audited; the backup ways and the clear-folder tick are by design and written in README; the changed-folder window
without the tick, the disk threshold and the detach are not written; the promote Retry is PC-1). Rows with no such way:
Versi agent, Pin master, Jam (30 s is a constant), Akun panel (only another account fixes it; the Buat replika target
without a connected inventory is skipped by design), performance_schema, Persetujuan di server. The two marks
(panel.key, audit forwarding) are yellow-only and set by one click, audited with who and when. The emergency path
cannot be used to walk around a red planned gate on a live master: the mode needs the panel to see the master gone,
and the candidate's agent refuses while the master's MySQL answers.

### 2. Can a compromised agent or database server turn a row green?

Yes for every row read from an agent, each only for its own server; this is the documented residual (README 4266-4271).

| Row | Where the value comes from | Panel's own check | Planned action that then passes | Worst outcome |
|---|---|---|---|---|
| Versi agent | `ProbeResult.AgentVersion` of the probe at connect (listener_impl.go:187), stored | none | every gated action on that server | a step fails midway on an agent that lacks a command; a failed probe leaves the old stored version |
| Pin master | master's last report `RolePin` (gate.go:409) | none | Buat replika, Resync, Tambah database, planned promote | none new: the pin is enforced by that agent |
| Jam | own report: `ClockUnixMS` against `ReceivedAt` = panel now − the agent's `TakenAgoMS` (monitor.go:94, 554) | the panel's clock is one side, but the agent states both its clock and the report's age | planned promote | lock deadlines and approval lifetimes on that server judged with a wrong clock (the promote passes the lock's remaining seconds, not the deadline: SEC-73) |
| TLS, master part | `server_tls` row, written when the agent reported installed the serial the panel issued (tls.go:258) | the serial must match the issued certificate; the panel does not look at the port itself | planned promote, rebuild | a master without the certificate counted as having it; replicas that verify would then fail to connect |
| TLS, replica part | `replicas.tls_verified`, from the replica's own job answer or report (monitor.go:605-639) | none | planned promote | an unverified link counted as verified |
| Ruang disk | own report | none | Buat replika, rebuild, Resync, Tambah database | that server's disk fills; the agent's preflight checks again |
| Akun panel | own `ConnectResult.Account` (runner.go:284) | none | every gated action | the agent keeps logging in as root unnoticed; an empty answer keeps the row yellow (PC-7) |
| performance_schema | master's report | none | Buat replika, rebuild, Resync, Tambah database | reads that need it fail or see nothing on that master |
| Cadangan | master's report (status, age, size); also anyone who can write a fresh non-empty file into the folder | none | planned promote, rebuild | the gravest: a rebuild wipes the old master's full copy while the new master has no backup |
| Persetujuan | the old master's own answer (approval.go:113-131) | answer must be well-formed | rebuild of that server | none: that agent enforces it |
| Port MySQL master | the panel's own TCP dial | independent | yellow only | none |

Server A's agent on a row about server B: no for every per-server row (reports and call answers are keyed by the
server id taken from the client certificate, agentapi/ws.go:27-39, hub.go:231). Two topology-wide effects exist: any
replica's agent can make the promote's TLS row red (below), and any replica reporting `super_read_only` off refuses
every promotion (PC-8). A's agent cannot make them green for B.

Wrongly red on purpose: an agent can turn its own rows red (version, pin, clock, disk, performance_schema, backup
"unreadable") and hold every planned action that judges its server; a replica that reports
`SSLVerifyServerCert` false turns the TLS row of every planned promote of its topology red until it is detached. That
is a nuisance: the row names the server, emergency promote is not held by rows, and detaching the server removes it
from the judgement. PC-8 is the one case that reaches the emergency promote.

### 3. Stale and missing data

- Missing data is never green: clock, disk, performance_schema and backup are yellow without a report, without the
  field or without a connection; version and pin are red without a value. Checked row by row in gate.go.
- Old data can be green: PC-4. After a panel restart every agent is "not connected" until it reconnects, so the four
  report rows are yellow G10a, version and pin are judged from the store; from the reconnect until the first live
  report they are judged from the snapshot stored before the restart. The same after any reconnect.
- Backlog: a backlog snapshot is not used for the clock (monitor.go:91) but is used for disk, performance_schema and
  pin while the batch is being flushed (up to 200 reports, followed by a live one). After a restart the backlog flag
  is lost (C-7).
- Clock jump on the panel host. Forward: ages grow, the backup row can read "stale" from extrapolation until the next
  report (wrong red, safe side), `backup_stale` is not opened from it (alerts.go:420), statuses go "Agent offline"
  until reports arrive, the port answer expires, 24-hour confirmations expire (safe). Backward: `JudgeBackup` clamps
  the negative time (alerts.go:1132), the port answer is probed again (gate_port.go:64), "Periksa sekarang" is refused
  for the length of the jump (monitor.go:388), a silent agent looks fresh for that long; the 24-hour window of a
  data-dropping Retry stretches by the jump. Changing the panel host's clock needs root there or control of its time
  source; nothing turns green that should be red except by PC-4's mechanism.
- Delaying or replaying reports: replay is not possible inside the mTLS WebSocket, and `Seq` drops older reports per
  connection (state.go:177). Delay: a delayed report is taken as "now minus the agent's stated age", so a delay of D
  shifts the measured clock difference by D (it can push a server over 30 s: wrong red; or, timed to the second, hide
  a real difference of the opposite sign) and keeps the other rows green on older data (PC-4). No action that should
  be held by something other than those rows passes.

### 4. The emergency promote

- The tick: checked at promote.go:205, after the mode is decided and before `AddPromotion` (:308), for a posted mode
  and for `Mode: ""` with an emergency view; the web checks it before the token (`checkPromotePosted`,
  handlers_promote.go:304). Sound for a START.
- Mode: the web never sends an empty mode; it requires the posted mode to equal the panel's (`PromoteMode` before the
  token, the fresh `PromoteView` after), and `StartPromote` refuses a posted mode that differs from its own fresh view
  (promote.go:193-202). A planned form cannot become an emergency one.
- "Nothing but the tick can refuse it" is true of the gate rows only. Refusals of both modes that are older than this
  plan: cutover pending; quarantined candidate; candidate not eligible (agent offline, another agent version (K1),
  start lock present, replica not ready, stopped, no fresh report, MySQL not answering, transactions outside the
  master); branched without the acceptance; any writable replica (PC-8); a previous promotion running or partial; a
  failed planned promotion whose lock may still stand (SEC-66, up to about 40 minutes); a partial candidate or
  unreachable replicas without the acceptance; an unfinished job in the topology (K2).
- Pushed through the emergency path by something other than the operator's explicit choice: **yes, through "Ulangi" of
  a failed emergency promote job (PC-1)**: no tick, no name, no password, mode not read again, confirmed by running.
- Who can make the master look offline, so that the emergency form is offered: the master's own agent (stopped by root
  there, crashed, or compromised: it disconnects, stays silent longer than `StaleAfter`, or reports MySQL down);
  anyone who can cut or filter the connection from the master to the panel's agent port (network, firewall, and DNS
  when agents reach the panel by name); a panel restart (every agent is "not connected" for seconds, then "Menunggu
  laporan pertama" until the first live report, promote_view.go:482-492).
- What then protects against two masters: (1) the operator's tick, typed name and password; (2) the candidate's own
  agent: three probes of the old master 10 s apart and its IO thread must not be connected, else refused (agent
  promote.go:387-420). This is the strong one, and it fails only when the candidate itself cannot reach the master;
  (3) quarantine: sent in the job when the old master's agent is connected, else pending and sent when it connects
  (PC-2 for how that can be delayed and not retried); (4) the old master's own agent does nothing by itself in an
  emergency (no promote lock was taken); (5) the start lock protects only after a quarantine was applied once. So:
  with the master cut off from both panel and candidate but not from the applications, two masters exist until the
  old master's agent is back and its quarantine lands. Inherent to an emergency failover; the tick names it.
- K1 (a candidate whose agent is another version is refused in an emergency too): keep. The value is the agent's own
  word (a compromised agent can claim any version, which gains it nothing), the refusal fails closed, and the
  alternative risks a promotion that stops after writes were opened. Cost: with every replica on another version the
  emergency waits for one agent update. The emergency screen should name that step.
- K2 (refused while the topology has an unfinished job): keep, with the pointer of 5b-2a. The operator is one
  "Batalkan" away (cancel works with agents offline, `cancelJob` bounds its calls at 15 s); an automatic cancel would
  be a destructive side effect in the middle of an outage. A job cut off by a restart is failed by `Resume` and does
  not block.

### 5. The master-port probe (SSRF)

- Address: `Topology.SourceHost` + `SourcePort` (gate_port.go:46). Set by: "Siapkan master" (the IP the agent paired
  from, taken by the panel from the TCP connection, agentapi/pair.go:95-151, or `PublicHost` for the panel's own
  host; the port is what the master's agent reports, unchecked for range); a promote (the candidate's pairing IP and
  the port its agent reports); the operator on the master page (any `ValidHost` name or IP, port 1-65535, no
  password, audited).
- Pointing it elsewhere: only the operator route can name another host or the panel host itself (`127.0.0.1`,
  internal addresses, another server's port). A compromised agent controls only the PORT on its own pairing address
  and never sees the answer.
- Disclosure: one bit per probe (TCP connect succeeded within 1 s), shown as the yellow row G11a, which names the
  master's server name, not the address; to logged-in users on the promote page, the rebuild form and a rebuild's job
  page; it is yellow-only, so it is in no audit list. The address is logged only if the probe panics.
- How often: one dial per master per address per 10 minutes, shared by concurrent gates; a changed address is dialled
  at once (PC-5). Triggers: every GET of those pages, every gated POST, the `skipped_red` read of an emergency.
- Port scanner: for a logged-in user yes, slowly and audited (PC-5, Rendah); for a compromised agent no.
- Not probed at all when the panel host is the master, the action's target or a replica of the topology, or when the
  topology has no address.

### 6. The inventory read on connect

`refreshUnknownAccount` (promote.go:1458): reads the stored inventory, and only when it is connected with an empty
account calls `mysql.inventory` (2-minute bound, `r.base` context, counted in the Runner's wait group so `Close`
waits). Locks: none but `invMu` around the read-modify-write of the row, never across the agent call; not `startMu`,
not `repointMu`, not the Monitor's. `OnOnline` returns at once.

What a compromised agent can make the panel store: its own inventory row (database names and sizes cut at 5,000,
missing grants, version, series, account cleaned and cut at 255 bytes by `storedAccount`), nothing about another
server; `MasterReady` and the preflight are kept. What it can make the panel do: one call and one write per connect,
for ever if it answers an empty account (PC-7). It runs during jobs too (a failure is only logged) and on a
quarantined server (read-only command). The row then goes red for `root`, which is the purpose.

### 7. The backup settings path

- Who sets the folder: any logged-in user, no password (R2), audited as `server_backup` with folder (cut at 128 runes,
  PC-10) and limit; a refused form is audited with lengths only. The panel's folder rule is stricter than the agent's
  (R23); the store keeps what the form gives.
- What a compromised panel learns through the report: for any path outside the refused prefixes, the status and reason
  code (exists or not, folder or not, readable, hidden from the service, more than 10,000 entries), whether a regular
  file exists within three levels, and the age and size of the newest one. A new folder starts a new scan generation
  on the agent, so each `monitor.configure` with a new path is scanned at the next check loop (the 5-minute limit is
  per generation). Written in README 4253-4260; accepted (R14). Agent auditor judges the scan itself.
- The per-server lock (monitor.go:410-467): held from reading the server row to taking the answer, at most 30 s per
  call (`configureTimeout`) and ended by `Close`. A hostile or slow agent holds only its own server's lock; other
  servers have their own; `m.mu` is never held while waiting for it or across the call. Saves are not behind it: the
  handler writes the store, audits, calls `ConfigureServer`, which forgets the report under `m.mu` and returns; the
  send is a background goroutine. Each save, connect and settings save queues one goroutine per server, so with an
  agent that never answers they run 30 s apart; each reads the newest row when its turn comes. No deadlock found:
  order is configMu → `m.mu`, and `report` takes only `m.mu`.
- R22 and the refill window: `forgetBackup` replaces the snapshot by a copy without the backup state and rewrites the
  stored row (state.go:663-679); sound. A report that left the agent before the new folder arrived (or, after a
  reconnect, before the configure was sent) still carries the old folder's state and is judged against the new folder
  until the configure's answer replaces it: a short wrong green/"pulih" at most one report long. With the lock the
  two-saves race of the final review is closed. The changed-folder "pending" window is PC-6.

### 8. Alerts and notifications

- Unbounded or unescaped agent values: one place, PC-3 (`master_unpinned`). Every other alert text is the panel's own
  with numbers, server names (store rule `[A-Za-z0-9 ._-]{1,64}`), a stored address, or agent text through
  `agentText` with a bound (reason 300, file 300, host 120, ten hosts). `backup_stale` carries a server name, an age
  in words, the limit and a reason from a closed map; unknown reason codes read "galat lain". Telegram is sent as
  plain text (no parse mode), one message per alert.
- Alert storm: at most one open alert per kind and server; a reopen within the flap window sends "berkedip" once
  (notifier.go:417). The table growth is PC-11.
- Suppressing `backup_stale`: by the master's own agent (report "ok", or keep the report not current: unknown neither
  opens nor closes, which is the rule of R41); by an agent of the previous version (never reports the folder; the
  version row is red for gated actions); by an operator (clear the folder or raise the limit: closes it as resolved,
  audited; snooze mutes sending). Not by another server's agent.
- Old-master alerts: `master_start_unguarded` follows the old master's own report and is not judged before a first
  report; the late/bypassed/blocked alerts follow the start state its agent reports, and an agent of the older kind
  can close "blocked" and "bypassed" with an event (monitor/promote.go:671-680). A compromised old-master agent can
  therefore hide its own start alerts (inventory R-026, accepted). A quarantined server has no `agent_offline` alert
  (`ruleApplies` alerts.go:601): when its agent is away, nothing says so except the dashboard.
- Closing: an agent's reports close the alerts they open; `job_failed`, the promote alerts and `tls_expiring` are the
  store's and no agent closes them; `monitor.lockExpired{unlocked}` from a master's agent closes that master's
  `master_lock_kept`.

### 9. The audit chain and the store

- Migration 0013 is three `ADD COLUMN … NOT NULL DEFAULT` on `servers` and `mysql_inventory`; it has no hook and does
  not touch `audit_log` or its triggers: the chain is intact. 0012 adds `start_marks` (ids compared for equality only;
  an id longer than 64 bytes counts as none and is never stored, start_state.go:27).
- The new entries go through the same `auditAs` as the old ones (chain row + journald copy with the row's hash; a
  failed write is logged and the action goes on, as before): gate refusals from the handler before the token and from
  `Start*` (outcome G0: row labels only, no agent text); `backup_ack: true` when the tick was used;
  `old_master_dead`, `master_last_report`, `skipped_red` (keys; `null` when the gate could not judge) on an emergency
  start; `server_backup`, `panel_key_marked`, `audit_forward_marked`. On a refused or failed action strings and lists
  are replaced by their lengths and numbers and booleans kept (web/audit.go:233-260), so a refused emergency shows
  `skipped_red_count`, not the keys. Redacted: any key containing password/token/secret; an agent's failure text is
  censored (`redact.Quotes`), labelled and cut. Gaps: PC-10.
- Tamper evidence is what it was: append-only triggers, a hash chain, a second copy in journald. Whoever can write
  panel.db can drop the triggers and rebuild the chain; the journald copy on the same host is the anchor, and only a
  forwarded journal survives a compromised panel host.
- K6 (audit forwarding is a mark set by hand): accept. The row is yellow-only and never refuses; a detector that could
  not be measured would be a false green in the other direction. What the mark is must stay clear on the screen and in
  README ("ditandai", who and when); it is the only thing standing between the audit chain and a compromised panel
  host, and one click by any logged-in user sets it, audited.

### 10. Re-judgements

- SEC-204, SEC-212, SEC-213, SEC-214 are all about what the old master's AGENT proves and compares when a quarantine
  ends; nothing in the panel core decides them and plan 5b-2b changed nothing there. Panel core's share:
  - SEC-204 (race between the listing proof and the list): the panel settles on the agent's OK answer to
    `mysql.configureReplication` (create_replica.go:985-1004) and has no check of its own. Unchanged: still open as
    narrowed in the 5b-2a spec, section 8. A panel-side cross-check (compare the old master's inventory with the new
    master's after the settle and warn about extra databases) would be cheap defence in depth; not built.
  - SEC-212 (writes into copied databases during the load): the panel's controls are in place and read: no rebuild
    while the cutover is pending (create_replica.go:174), the old master's connections read fresh from its agent at
    start, at Retry and at Approve, and required clear or acknowledged (:100-110; runner.go:816-817, 850-879). The
    gate's account row (root) is a help, not a control. Still open as written.
  - SEC-213 (system schemas not compared), SEC-214 (privileges through roles not counted; fails closed): by design, no
    panel part. The agent's refusal text reaches the operator labelled as the agent's words.
- 5b-2a panel final review, "Declined to judge":
  1. Agent internals (`configureReplication` answering OK again, `rebuildApproval` under role replica): agent
     auditor. Panel side read: an answer "not required" lets a rebuild start (approval.go:125) — the agent's own
     gate, by design; a malformed answer is unusable and refuses.
  2. Health-report fields on monitoring pages and the audit outcome text: pages are the web auditor's; in the alerts
     one field is still raw (PC-3); the outcome text is labelled now (`outcomeText`).
  3. `dropReplUserHint` (prepare_master.go:159-177): builds `DROP USER 'dbpanel_repl'@'<host>'` from the refusal of
     the master being prepared; the user is fixed, the host cannot hold a quote or backslash, the whole message is
     then censored and cut. An agent can only name hosts of its own replication account. Not a defect.
  4. Alert-table growth by a hostile agent: PC-11.
  5. Session, CSRF, `requireConfirm`: web auditor.
  6-8. Text comparison, CSS/README, a flaky test: not security items.
- 5b-2a spec section 8 and 11, panel items: the pending quarantine after a second promote is now sent under the
  topology's current master (promote.go:1486-1520), and stays pending when that agent refuses, as section 11 says;
  the restart reasons come after the panel's sentences, labelled and bounded (prepare_master.go:308-328); an orphan
  row of a quarantined server is removed by a new rebuild (create_replica.go:201); "the settle's answer lost" leaves
  the server judged as an old master until "Ulangi" — unchanged, accepted. New for that list: PC-2 (a pending
  quarantine is tried once per connect and waits behind a panel-wide lock).
- K9, panel-core items:
  - `cancelAgentCopy` outside `startMu` and before the gate (controls.go:330, create_replica.go:119): the worst is as
    written — the agent job stored on the newest FINISHED create job of that server is canceled; when a Retry of that
    same job re-attached to the copy in the same instant, its copy is canceled, the job fails and is retried. A gate
    refusal after it has still canceled a stale copy of a job the panel already failed. No data beyond what that
    rebuild was replacing. Accept; move it under the lock when the function is next touched.
  - Database names holding a quote in censored lines: the wait message, step message and job log lose the names after
    such a name (`setStep` runner.go:1920), while the approve dialog reads `WaitList` whole and the approval is bound
    to the hash of the whole list (create_replica.go:495-502), and an older job whose only list was cut is refused
    (`refuseCutWipeList`). Accept for this merge; exempt names from the censor in plan 5e.
  - The other K9 items are the agent's.

### 11. Locks and denial of service

- `startMu` across `Gate`: inside are store reads, `Monitor.View` (`m.mu`), `Hub.IsOnline` (`h.mu`, map only) and at
  most one port probe. The probe is bounded at 1 s (context and dialer; the build is `CGO_ENABLED=0`, and with cgo the
  dial would still return with the context), its answer or failure is kept 10 minutes, waiters are released by a
  deferred close also on a panic. A slow DNS answer costs at most 1 s per master per 10 minutes. No agent is called
  under `startMu` anywhere (checked every `Lock` site: the calls come before it or run in the job goroutine); R29
  holds.
- Lock order found: `startMu → lifeMu → m.mu` (delete: `Hub.Disconnect` is called under `startMu`, controls.go:216),
  `startMu → m.mu`, `startMu → ports.mu`, `configMu → m.mu`, `lifeMu → m.mu`, `lifeMu → r.mu`; nothing takes them the
  other way; `m.mu` and `r.mu` are never held across a hub call.
- A compromised agent cannot hold `startMu`, `m.mu`, `h.mu`, `lifeMu` or another server's `configMu`. It CAN hold the
  panel-wide `repointMu` for up to 5 minutes (PC-2), its own `configMu` for 30 s per call, and its own connection's
  write lock for 10 s per request when it stops reading (conn_ws.go:104-107; that wait ignores the caller's context,
  but only calls to that same agent queue there).
- `m.mu` is held across SQLite reads and writes and across a full alert evaluation on every live report; the hub
  limits one server to a report per 2 s, so a compromised agent can force an evaluation that often: milliseconds each
  with tens of servers.
- Many concurrent requests: gates of one master share one probe; `PromoteView` asks agents on every GET of the promote
  page (the fresh-report call is limited to one per 10 s per server, the other two calls are not) — web auditor's
  rate question. Growth and reconnect loops: PC-7, PC-11.

### 12. Confirmation-token and job-state handling on Retry/Approve

- A job created under one master cannot be approved or retried under another: `supersededBy` refuses when the
  topology's master is not the job's `MasterServerID` (runner.go:1128-1134), when the replica row moved to another
  topology, when the server's role or the replica's state or databases changed, when a newer job exists; a promote
  job by `promoteSupersededBy`. The gate of a Retry is judged for the job's own master (`JobGateInput` :742).
- Parameters between gate and execution: the job's params are written only by its own goroutine (which has ended
  when the job is failed or waiting), by `confirmDrop` under `startMu` (the new `ConfirmedAt`, and on Retry the
  re-acknowledged `WipeOnly`, which must be a subset of the stored `WipeList`) and by the settle. No web route edits
  them. `execute` re-reads the job and runs only when status and step are those the action was checked against
  (runner.go:553-567). What can change in between is outside the job: the topology's source address (operator, PC-5)
  and the master's inventory (its agent); the copy step reads both fresh and runs its preflight again before it drops.
- A data-dropping Retry needs a fresh token (`ack.Confirmed`), a last confirmation at most 24 hours old, and for a
  rebuild the approval and the connections read again; Approve of an add needs the typed names; the gate comes before
  the token so a refused post does not use it. Sound.
- An unreadable job: `jobDropsData` counts it as dropping (needs the token), `JobGateInput` does not gate it,
  `supersededBy` returns the read error for replica and promote jobs (refused), `rebuildApprovalForJob` refuses a
  create job whose params cannot be read, and a job of another kind that gets through is failed by `newRun` before any
  step (`failUnrunnable`, runner.go:568-575). The web answers 500 before any of that (`DropsData` error). No step runs
  on params that could not be read.
- Not covered by any of this: a promote job (PC-1).

## Checked and found sound

- Every gated `Start*` calls the gate under `startMu` after its own checks and before its first store write; a gate
  error refuses; the backup tick is checked after the rows.
- Emergency start: tick before any promotion write; mode agreement in three places; no row refuses.
- `supersededBy`/`promoteSupersededBy`; `confirmDrop` stores nothing on a refusal; `launchRefused` before the store
  write; a reopened job is put back when the launch fails.
- Rebuild: approval read before every start, Retry and Approve; connections read again; settle in one transaction;
  the role of a quarantined server is changed only by the settle; `RemoveReplica` keeps the quarantine role.
- `UnlockMaster` reaches only the topology's current master without a job; `DeleteServer` does not ask a quarantined
  server to release its pin; a pending quarantine blocks the plain delete.
- Port probe: bounded, shared, panic-safe, not cached on failure, clock-jump safe.
- `JudgeBackup`: clamps, saturates, size ≤ 0 is empty; `backup_stale` opens and closes only on a current report of a
  connected agent, except "unset" which closes.
- Hub: server id from the client certificate with the latest serial; 1 MiB read limit; report and event rate limits;
  an agent's error is told apart from the hub's own (`AgentError`).
- Monitor: report age clamped before use; older `Seq` dropped; over-long start ids ignored; start alerts rate-limited; snapshot
  error texts censored at ingest.
- Store: 0013 is additive; the audit chain is written in `BEGIN IMMEDIATE`; 0009's backfill fails closed when a
  trigger is missing.
- `redact.Quotes` as read: kept literals are the panel's own account names and phrases; MySQL's templates win.

## Residual risks (inherent or accepted; worst outcome each)

- A compromised agent turns its own rows green (answer 2). Worst: a rebuild wipes the old master's copy while the new
  master has no backup; a planned promote onto a server whose state is misreported.
- A compromised panel sends agents commands without the gate (README 4266-4269). Worst: whatever the agents allow.
- Emergency promote with the master cut off from panel and candidate but not from the applications: two masters until
  the old master's quarantine lands. The tick is the control.
- The backup row proves only "a recent non-empty file exists"; anyone who can write into the folder makes it green.
- A wrongly red row holds planned work until fixed; no override (except the ways of PC-6 and PC-1).
- A compromised old-master agent hides its own start alerts (R-026).
- The audit chain against a compromised panel host rests on a journal forwarded elsewhere, which the panel only
  takes the operator's word for (K6).
- SEC-204, SEC-212 open as narrowed; SEC-213, SEC-214 by design.

## Not done (what I could not examine, and why)

- No test that starts MySQL, no e2e test, no race run (rules). PC-2 to PC-9 are confirmed by reading only; PC-1 by one
  overlay test with fake agents.
- The agent side of every rule named here (the emergency probe, the scan, the approval, `mysql.quarantine`,
  `monitor.configure`) was read only where a panel statement rests on it; judging it is the agent auditor's.
- Web handlers were read only for the audit fields, the gate order, `setMasterSource` and the job actions. Left to the
  web auditor, with pointers: whether `POST /masters/{id}/source`, `salvage delete`, settings save and the two marks
  should need the confirmation; `Status.Detail` of "Tidak terjangkau" is the agent's `MySQLError`, censored but not
  bounded in the monitor (status.go:167-170, 259-262) — whether the pages bound it; the rate of `PromoteView` on GET.
- `monitor/samples.go`, `weekly.go`, `explain.go`, `gtid.go`, `orchestrator/snapshot.go`, `agenttext.go`,
  `record_replicas.go`, `gtid.go`, store files not named in the dispatch (`alerts.go`, `health.go`, `inventory.go`,
  `promotions.go`, `confirmations.go`, `tokens.go`, `users.go`), `hub/conn_local.go`: not read in full (outside the
  dispatch's list; used only through the calls seen).
- Rulings R37-R41 were read only as restated in final-fix-wave.md and final-fix-report.md (rulings.md ends at R36).
- No second pass over the findings by another reader; severities are mine alone.
