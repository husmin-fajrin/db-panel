# Final fix wave, part D — report

BASE 0b1c760. Status: BLOCKED (D-8 stop condition met; D-2 depends on the same fact). Done and committed (not pushed):
- 9d643ee docs(plan): record what keeps MySQL read-only when started without the agent  (D-1 evidence)
- d024025 docs: cut the applications off before starting a quarantined MySQL by hand  (D-1)
- 549cd1c fix(panel): take the approval's source host by the master address rule  (D-4)
Not done: D-2, D-8 (blocked, below), D-3, D-5, D-6, D-7 (ordered after D-8; I stopped at D-8's "stop and report").
Not pushed: the push needs all gates; only gate 1 was run (see Gates).

## Blocking finding (D-8 (b); also breaks D-2 and A6's advice)

After any failed step of a rebuild job ("Bangun ulang sebagai replika" = create_replica with RebuildOldMaster), the panel cannot start a new "Bangun ulang sebagai replika" for that server:
- StartCreateReplica -> store.AddReplica sets servers.role = replica (topologies.go AddReplica `UPDATE servers SET role = ?` RoleReplica). fail() (runner.go) only sets the replica row to ReplicaFailed; nothing puts the role back to quarantined (the only setters: create_replica.go:154 inside a NEW StartCreateReplica, keepQuarantined on cancel/createJob error; cancelJob/Cancel return early for a failed job, and a failed job has no Batalkan: handlers_jobs.go sets only CanRetry for JobFailed).
- rebuildTarget (web/handlers_oldmaster.go) returns nil when srv.Role != quarantined, so `/masters/<m>/replicas/new?bangun-ulang=<id>` answers 400 with msgRebuildTarget, and a POST with rebuild=1 fails the same way; oldMasterViews returns before building the "Master lama" box (role != quarantined), so the server page has no "Bangun ulang sebagai replika" button.
- Verified with a throwaway web test (promotedEnv; AddReplica(topo, 1) + SetReplicaState(1, failed); deleted afterwards, not committed): GET /masters/2/replicas/new?bangun-ulang=1 -> 400, no rebuild link; GET /servers/1 -> 200, zero occurrences of "Bangun ulang sebagai replika"; GET /replicas/1 -> offers "Resync ulang" and "Lepas replika"; GET /masters/2/replicas/new lists db-master as a (retry) target.
- Only accidental way back, from reading the code (not run): "Buat replika" (not rebuild) from the new master with the failed old master as target -> StartCreateReplica removes the failed row and SETS the role to quarantined (create_replica.go:150-158), then refuses "server tujuan adalah master lama yang dikarantina; pakai Bangun ulang sebagai replika". After that the Master lama box and the form are back. I did not document this: it is a side effect, not a designed path.

Consequences:
- D-8: the addendum's condition "If (b) shows a new rebuild cannot be started from the panel in this state, stop and report" holds. errNoLoadRecord left unchanged.
- C-6 state (agent restarted between copy and configure): "Ulangi dari tahap ini" fails again forever (no load record) and no new rebuild can be started from the form. No designed way out exists in the panel.
- A6 (errNotReplaced) says "Ulangi Bangun ulang sebagai replika dari panel. Bila panel lalu menolak karena database itu tidak ada di master baru, lihat README …": neither half happens — the form cannot be opened, so the preflight refusal "server ini masih berisi database lain (…)" is never reached from this state. What does work from the A6 state (code reading, not run): drop the foreign database on the server, then "Ulangi dari tahap ini" (Runner.Retry relaunches at j.CurrentStep = the configure step; settleRebuilt still has the load record if the agent did not restart; notCopied(present) is then empty -> settled). D-2's required step 3 ("run Bangun ulang sebagai replika again") would therefore be a README step that does not work; I did not write D-2.
- README lines at my head that are already false for the same reason (from part B/C, not changed by me): "Bila teks itu muncul, jalankan lagi Bangun ulang sebagai replika dari panel …" (A6 paragraph) and "… jadi mulai lagi Bangun ulang sebagai replika dari panel." (C-6 paragraph).

Decision needed (owner/controller): either (1) a panel change so a failed rebuild of an old master can be started again from the form (e.g. rebuildTarget/oldMasterViews accept role replica + replica failed + the server is the topology's old master, or fail() of a rebuild job puts the role back to quarantined as cancel does), after which A6, C-6 and D-2 as briefed become true; or (2) README/text that sends the operator to "Ulangi dari tahap ini" for A6 (true while the agent was not restarted) and gives the C-6 state some exit. Option (1) also needs a decision on what the monitor's SP-2 logic (monitor/promote.go:733 "a cancel makes it an old master again") should do for a failed job.

### D-8 (a), (b), (c)
- (a) Retry button on a failed job: "Ulangi dari tahap ini" (web/templates/job_detail.html:85 and :87). A rebuild job is DropsData, so it is the /retry/prepare form; its dialog's button is "Ya, ulangi" (job_detail.html:124).
- (b) See above: not from the form; no designed path.
- (c) The agent keeps the approval on the C-6 failure (part C). A new rebuild start and every Retry ask the agent (requireRebuildApproval); an approval is valid 24 h, after which the form shows a new code and root runs `approve` again (README "Persetujuan di server untuk bangun ulang"). Moot while (b) blocks.
- D-8.4 (C-1 text, "Ulangi dari tahap ini" after the GRANT): true at both places. At the copy step (dump refused before any local change) Retry relaunches the dump step; at the configure step Retry relaunches configureReplication and the settle reads the listing again with the load record still in memory. Caveat (not in README, not changed): if the agent restarted in between, the configure-step retry gives the C-6 text instead; and Retry of a job whose last confirmation is over 24 h old is refused (README "Keamanan").

## D-1 — measurements (scratch mysqld 8.0.46-0ubuntu0.24.04.4, datadir under a mktemp dir in /tmp, run as root with user=nobody, --defaults-file only; stopped and removed afterwards; also in the plan, new subsection "Bukti baca-saja saat MySQL dinyalakan tanpa agent (temuan tinjauan ulang C-1)")

| Case | Persisted before stop | Option file [mysqld] | After start (read_only, super_read_only) |
|---|---|---|---|
| fresh | none | none | 0 0 |
| (a) | none | super_read_only=ON | 1 1, nothing persisted |
| (b) | read_only=OFF, super_read_only=OFF | super_read_only=ON | 0 0 (persisted OFF wins) |
| (b2) | super_read_only=OFF only | super_read_only=ON | 1 0 |
| (b3) | read_only=OFF only | super_read_only=ON | 0 1 |
| (b4) | both OFF | super_read_only=ON + init_file=<lock file with the 3 lock lines> | 1 1, persisted back ON ON |

(c) quarantined state 1 1 / persisted ON ON; ordinary account apl (ALL ON app.*, lain.*; no SUPER/CONNECTION_ADMIN):
| Step | Result |
|---|---|
| apl INSERT at 1 1 | ERROR 1290 |
| root `mysql -e 'SET GLOBAL super_read_only=OFF; DROP DATABASE lain; SET GLOBAL super_read_only=ON;'` | exit 0; dropped; 1 1; persisted ON ON |
| root SET GLOBAL super_read_only=OFF | 1 0 |
| apl INSERT / CREATE TABLE / DROP DATABASE at 1 0 | all ERROR 1290 |
| root CREATE + DROP DATABASE at 1 0 | ok |
| same one-invocation with a failing DROP | exit 1 (ERROR 1008); later statements not run: stays 1 0 until SET GLOBAL super_read_only=ON |
| restart | 1 1 |
| backticked `data-lama` inside single shell quotes | ok |
| `mysqldump --single-transaction --routines --triggers --events --set-gtid-purged=OFF --databases data-lama > file` at 1 1 | exit 0; file has CREATE DATABASE + INSERT; reloaded fine |

Finding against the brief's assumption: the agent's guard does NOT put super_read_only back within seconds while MySQL stays up. guardQuarantined (quarantine.go) acts only on a transition (MySQL back up, Uptime fell, agent start, after a rebuild load): `if d.guard.up && sameRun { return nil }`. So a future D-2 section must not say the guard restores it; the one-invocation command must set it ON itself, and on a failure the operator runs SET GLOBAL super_read_only=ON. Recorded in the plan evidence.

Outcome: (b) shows the persisted OFF wins, so the README follows the brief's fallback: the `super_read_only=ON` line is still added (it helps when nothing is persisted, case (a), e.g. a quarantine written while MySQL was down) but is said plainly not to be a guarantee; the protection is cutting the applications off; check and SET PERSIST right after the start.

### D-1 — README rewrite ("Melepas kunci start dengan tangan")
New order: intro with the two-master caution and the panel's own alert quoted -> 1 stop agent -> 2 cut every application off (stop apps or close the MySQL port in the firewall), reason "Tulisan yang masuk ke server ini tidak pernah sampai ke master baru." -> 3 find the option file and lock line (moved before any start) -> 4 if MySQL is down: add `super_read_only=ON` under `init_file=`, not a guarantee, start; missing lock file -> printf rewrite; still not starting -> stop, remove nothing -> 5 check immediately; what a 0 means; SET PERSIST -> 6 remove init_file line and the added line -> 7 remove lock file -> 8 role files -> 9 open only if it becomes the application's master. Folded in from D-3 because the section was rewritten: m-1 (full mysqld paths), m-2 (open only if master, not for unassigned), m-3 ("memastikan … tersimpan ON", "agent memakai syarat yang sama (tersimpan ON)"), m-4 (stop when MySQL still won't start), m-5 (printf command).

Five-case walk (the re-review does not list cases (a)–(e); I used these):
| Case | End state | What the README told the operator |
|---|---|---|
| (a) lock effective, MySQL down, persisted ON | starts 1 1; released; persisted ON; apps cut until step 9 | steps 1-9 in order; check passes |
| (b) lock bypassed (--init-file in the unit or a later init_file), MySQL down, persisted ON | starts 1 1 from the persisted values | same; check passes |
| (c) lock not used, nothing persisted (quarantine written while MySQL was down) | starts 1 1 thanks to the added line (measured (a)); check shows nothing persisted -> SET PERSIST -> passes | step 4 line, step 5 SET PERSIST |
| (d) lock not used, persisted OFF | starts 0 0 (measured (b)); no writes because step 2 cut the apps; step 5 shows 0 -> README says MySQL was writable since start and writes are not on the new master -> SET PERSIST -> 1 1 | step 2 (mandatory, before start), step 5 meaning of 0 |
| (e) MySQL already up (agent dead) | no start; step 5 check; a 0 means it was writable, writes possibly arrived before step 2 and are not on the new master -> SET PERSIST | step 4 skipped ("Bila MySQL mati"), step 5 "juga bila MySQL tadi sudah menyala" |
Also: lock file missing/unreadable -> "Failed to open the bootstrap file" -> printf rewrite then start; MySQL still down -> stop, nothing removed.

README commands added/changed in D-1 and how each was run:
- `printf 'SET GLOBAL super_read_only=ON;\nSET PERSIST read_only=ON;\nSET PERSIST super_read_only=ON;\n' > <berkas kunci>`, `chown root:root <berkas kunci>`, `chmod 0644 <berkas kunci>`: run on a scratch path; content equals startLockContent (startlock.go:41) byte for byte; that file then worked as init_file in (b4).
- `mysqld --verbose --help 2>/dev/null | grep -A1 '^Default options are read'` and the grep line: run on this machine (read-only; prints the option file list; grep found nothing, exit 2 because /etc/my.cnf.d and /usr/etc/my.cnf are absent here). Full mysqld paths: from layout.go mysqldPaths; only /usr/sbin/mysqld exists here.
- The two SELECTs and two SET PERSIST: run on the scratch mysqld as `mysql -S <sock> -u root` (README: `sudo mysql` / `mysql -u root -p`).
- Unchanged from before: systemctl stop/start dbpanel-agent, systemctl start mysql/mysqld, echo master > /etc/dbpanel/role, rm -f of the pin files, SET PERSIST … OFF (not run here; not touching this machine's services or /etc).
Quoted texts: "kunci start tidak bisa diperiksa" (cli.go setRoleNoCheck), "master lama belum terjamin terkunci saat MySQL menyala; jangan nyalakan MySQL-nya sebelum agent tersambung" (monitor/alerts.go:384), "bila MySQL di server ini … pastikan master lain tidak lagi dipakai" (cli.go setRoleNextMaster), "Failed to open the bootstrap file" — all compared character for character.
Operator-facing prose not given verbatim: the whole rewritten section (mine, following the re-review's corrected sentences where they exist).

## D-4 — N-1
Verified first: web/handlers_mysql.go hostRe `^[A-Za-z0-9_.:-]{1,255}$` + validHost (colon only for an IP) used by the master replication address (setMasterSource) and the connect form; approval.go had its own validHostName (RFC 1123). Also found: `net.SplitHostPort("[db]:3306")` returns host "db", so the old rule accepted a bracketed NAME despite the P-3 intent.
Change: one definition `orchestrator.ValidHost` (moved hostRe + validHost from web; web now calls it at both former call sites); validApprovalSource = SplitHostPort + ValidHost(host) + brackets only around an IP + port 1-65535.
Tests (approval_test.go): unusableApprovals: removed "source underscore", "source long label", "source empty label" (now usable by the reused rule: the panel and agent accept `db..lan` too); "source long name" made 256 characters; added "source with U+200F", "source with tab", "source bracketed name" ([db_baru]:3306), "approved bracketed name", "source colon in name" ([db:baru]:3306). Existing space, newline, U+202E cases kept. TestRebuildApprovalUsable: added db_master (request and approved), trailing dot `srv-db.rs.local.`, 64-char label, 255-char name, `[10.0.0.1]:3306`.
RED: `go test ./internal/panel/orchestrator -run 'TestRebuildApprovalUsable$|TestRebuildApprovalUnusableAnswers'` -> "approved bracketed name: usable" (+ StartCreateReplica accepted it) and five "not usable" lines for db_master, trailing dot, 64-char label, 255-char name. GREEN: same run plus TestHostileApproval|TestUnusableApprovalLogged: ok; `go test ./internal/panel/web -run 'Source|Host|MySQL'`: ok.
Existing tests changed: the three unusable cases above (their expected outcome is exactly what N-1 changes) and the long-name length.

## Gates
1. `go test ./internal/panel/... -race -short -timeout 60m` — exit 0, 156 s (head 549cd1c).
2. Addendum agent gate — not run (no agent change made; D-8 blocked).
3. `make test`, `make test-e2e` — not run; not pushed.
gofmt -l clean, go vet ./internal/panel/... clean, go build ./... ok, `graphify update .` after the code commit.

## Files changed
docs/superpowers/plans/2026-11-12-05b1-mode-ketat-perbaikan-agent.md; README.md; internal/panel/orchestrator/approval.go; internal/panel/orchestrator/approval_test.go; internal/panel/web/handlers_mysql.go.

## Choices the brief does not fix
- D-1: kept the `super_read_only=ON` line despite (b) (useful in case (a)), worded as no guarantee; step 2 names stopping apps or closing the MySQL port, without a firewall command (I could not run one); step 9 tells an `unassigned` operator to leave read-only and not point apps at the server; m-1…m-5 folded into the rewrite.
- D-4: the rule lives in orchestrator (web imports orchestrator, not the reverse); brackets accepted only around an IP (also IPv4).

## For the next round (D-2 material, verified, not committed)
- Backup: `mysqldump --single-transaction --routines --triggers --events --set-gtid-purged=OFF --databases <nama> > /root/<nama>-<tanggal>.sql` (run against scratch with -S/-u root and a scratch path), then check size (`ls -l`) and that it has CREATE DATABASE.
- Drop: `sudo mysql -e 'SET GLOBAL super_read_only=OFF; DROP DATABASE `<nama>`; SET GLOBAL super_read_only=ON;'` works on a quarantined server (measured); on an error the last statement is skipped -> run `sudo mysql -e 'SET GLOBAL super_read_only=ON;'`; the agent does not restore it while MySQL stays up; persisted values unchanged; read_only stays 1 so ordinary accounts cannot write meanwhile.
- Preflight text: "Server replika khusus: server ini masih berisi database lain (<daftar>); versi ini hanya mendukung server replika khusus" (failedChecks format Title: Detail; preflight.go:544).

## Self-review / concerns
- Two README paragraphs from parts B/C remain false at my head (A6 and C-6 advice, see the blocking finding).
- A6 and errNoLoadRecord texts in the agent point at actions the panel cannot do from that state.
- The brief/re-review assumption "the guard puts super_read_only back within 5 s" is false for a running MySQL.

---

# Round 2 (after addendum 2) — appended

Commits (in order, after 549cd1c):
- b2f5a47 fix(panel): offer a new rebuild of an old master whose rebuild failed (D-9)
- 03e2393 fix(agent): tell a rebuild without a copy record to start the rebuild again (D-8)
- 90dba46 docs: save and drop a database the new master lacks, then go on (D-2)
- bc2ca6b docs: correct the minor README statements the re-review found (D-3)
- 0e039d4 fix(panel): refuse a not-required approval answer that carries approval data (D-5)
- 1a40218 fix(panel): leave local_approval out of the audit when the agent was not read (D-6)
- b4a8059 docs: name the unusable approval answer among the unread ones (D-6 README follow-up; a second commit for D-6)
- 552b47b fix(panel): drop interest-cohort and fail the browser check on page errors (D-7)

## D-9
Predicate `(*app).oldMasterAwaitingRebuild` (web/handlers_oldmaster.go): role quarantined; or role replica AND replica row failed AND GetTopologyByOldMaster finds a topology. Used by rebuildTarget (form GET and POST), oldMasterViews (box), connectionsOf (old-master wording of the connections texts; before, a failed-rebuild server got the new master's texts, e.g. "Master baru tidak terjangkau"), and `rebuildURL` (replica page, job page).

Point 2 — per action/text of the Master lama box in the new state (role replica, row failed):
- Header "Master lama (dikarantina sejak …, promote … ke …)": true (agent pin still quarantined; promotion record unchanged). Shown.
- Status (status-full with statusOf(id, "replica")): the monitor's view of the server as a failed replica (not judged -> "Replika belum siap dipantau"); true. Shown.
- Cutover-pending banner/link: cannot be pending (a rebuild started only after it); shown as before if it were.
- Data tertinggal results (last check, tables): historical, true. Shown.
- Periksa data tertinggal / Amankan data tertinggal: POST goes through oldMasterFromPath (role must be quarantined -> 400 msgNotOldMaster) and Orchestrator.StartLeftover/StartSalvage refuse role != quarantined (oldmaster.go:102). Cannot work -> not offered (box.RebuildFailed hides the buttons, the offline help line and the "Amankan menyimpan …" sentence).
- Bangun ulang sebagai replika: works (form 200, POST reaches StartCreateReplica, which removes the failed row and sets quarantined). Offered.
- Hapus server (in the box): deleteServer refuses a registered replica (controls.go deletable). Not offered (the page-actions delete is already hidden whenever the box shows).
- Salvage file list (separate card): Unduh/Hapus endpoints are not role-gated and the agent allows salvageList/Read/Delete on quarantined; works. Unchanged.
- "Server ini adalah replika." banner above the box: hidden when the box shows (it read as an ordinary replica).

Point 3 — replica page (/replicas/<id>):
- Resync ulang: StartResync accepts a failed row; the agent (pin quarantined) refuses its protected steps without an approval (needs_local_approval -> P12) and runs them with a valid rebuild approval. It can therefore succeed, but it is no rebuild: no approval check before the start, no connections acknowledgement/leftover warnings, and replicaVerify calls ClearOldMaster only for RebuildOldMaster, so after it the server stays recorded as the topology's old master and any later failed seeding job would bring the Master lama box back on a server whose agent is no longer quarantined. Not needed for a way out (the rebuild is). Not offered. (Choice: the addendum's rule names "cannot succeed"; I applied the spirit, see concerns.)
- Lepas replika: DetachReplica(stop=false) just RemoveReplica -> role unassigned while the agent stays quarantined (exactly the case point 3 names); with stop=true resetReplica is protected (refused without approval -> error, row kept) and releaseRole is role-refused (logged). Not offered.
- In their place: `Server ini master lama yang masih dikarantina. Pakai <a>Bangun ulang sebagai replika</a>.` (link to /masters/<new master>/replicas/new?bangun-ulang=<id>).
- Residual: the POST endpoints /replicas/<id>/resync and /detach are not refused by the web layer in this state (only not offered); I added no handler refusal (not asked).

Point 4 — failed rebuild job page: under the "Ulangi dari tahap ini" button, when CanRetry and the job is create_replica with rebuild_old_master and the server matches the predicate: `Bila "Ulangi dari tahap ini" tidak menolong, mulai lagi <a>Bangun ulang sebagai replika</a>.` Not shown for an ordinary failed create_replica job, nor when the job is superseded (CanRetry false).

Point 5 — tests (web, fake orchestrator; orchestrator, real runner):
- web_failed_rebuild_test.go: TestFailedRebuildOffersRebuildAgain (server page 200, box + rebuild link, no Periksa/Amankan/Hapus, no "Server ini adalah replika."; form GET 200 with an enabled button; no new-master connection wording), TestFailedRebuildStartsAsFirstRebuild (no token 403; wrong typed name 400 and no orchestrator call; orchestrator approval refusal shown as for a first rebuild; then 303, StartCreateReplica with RebuildOldMaster, target 1, master 2, Approval set; audit create_replica with rebuild_old_master true, local_approval true, age 60), TestFailedRebuildNotOtherServers (ordinary failed replica: no box, form 400, its replica page keeps Resync ulang and Lepas replika; old master with its rebuild running: no box, form 400), TestFailedRebuildReplicaPage, TestFailedRebuildJobPage.
- RED (before the code): box/link missing, modal-hapus and the replica banner present, form 400, POST 400 (both refusal text and 303), replica page lacks the line and shows resync/detach, job page lacks the line. NotOtherServers passed before (expected: it pins what must not change). GREEN: ok.
- Orchestrator: no existing test of a second rebuild after a failed one (checked approval_test.go, promote_test.go, create_replica_test.go). Added TestRebuildAgainAfterFailedRebuild: first rebuild fails at configureReplication -> row failed, role replica; second start with an unusable approval -> ErrRebuildNeedsApproval, row still failed; with an approval -> job succeeded, role replica, no longer old master. Passed at once (orchestrator behaviour already existed; a coverage test, no RED).

Point 6 — monitor (not changed), for role replica + failed row (monitor's s.role = replica, repState failed):
- KindMasterStartUnguarded (start lock not guaranteed, alerts.go ~355): open requires role quarantined -> not raised; an open one is CLOSED with notQuarantinedReason when the role becomes replica (close: v.Role != quarantined).
- Start-state reconciliation from snapshots (start_state.go:130, late/failed/bypassed): skipped unless role quarantined -> a bad MySQL start of the old master while the rebuild has failed raises no alert.
- KindMasterStartLate: an open one is kept while the row is creating/failed (promote.go:732, SP-2), but new ones come only from the start-state path above, so none are raised.
- quarantinedFromStore (deferred-quarantine follow-up, promote.go:301) and the promote-notice close reasons (promote.go:499): skip role != quarantined.
- ruleApplies: no longer the quarantined exemption; as a failed replica its status is "Replika belum siap dipantau" (not judged) so replica status alerts are not raised either.
- DeriveQuarantined (status text "Master lama setelah promote; dikarantina …") is not used; status is the replica one.
This state existed before D-9 (it is how a failed rebuild always left the server); D-9 does not change it. Risk for the owner: between a failed rebuild and the next one, the old master's start-lock and bad-start alerts are silent.

## D-8
(a) "Ulangi dari tahap ini"; (b) now possible (D-9): server page box, the job page's link, the replica page's link; (c) the kept approval serves the new start while valid (24 h, same source); otherwise the form shows a new code.
Text (quarantine.go errNoLoadRecord), last sentence replaced exactly as addendum 2 gives it. Test pinning it: TestConfigureReplicationEndsQuarantine (pins_test.go) — RED: old text returned; GREEN: ok (real mysqld). No panel test pins it (grep). README: new quotation; the retry repeats only the configure step and fails again; numbered steps (where the button is; same form checks; approval rule).
D-8.4: unchanged, as in round 1 (true at both places).

## D-2
Section `#### Database yang tidak ada di master baru`, placed after "Persetujuan di server untuk bangun ulang". The A6 paragraph now says when A6 appears, that a database the new master also has is dropped by a new rebuild, and that one it lacks stops the next rebuild at the preflight and points to the section. Corrections from my measurements applied: the guard does not restore super_read_only while MySQL runs; the command sets it back itself; on an error run `sudo mysql -e 'SET GLOBAL super_read_only=ON;'`; check `SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;` = `1 1`. Both ways forward, simple first.
Commands (all run on a fresh scratch mysqld 8.0.46 as `mysql -S <sock>` / `mysqldump -S <sock>` with the dump file in the scratch dir instead of /root; instance stopped and removed):
- `sudo mysqldump --single-transaction --routines --triggers --events --set-gtid-purged=OFF --databases <nama> > /root/<nama>-cadangan.sql` (exit 0, 2138 bytes), `ls -l …`, `grep -c 'CREATE DATABASE' …` (printed 1).
- `sudo mysql -e 'SET GLOBAL super_read_only=OFF; SET SESSION sql_log_bin=OFF; DROP DATABASE `<nama>`; SET GLOBAL super_read_only=ON;'` (exit 0; 1 1 after; gtid_executed unchanged; persisted ON ON unchanged).
- error path with a wrong name: exit 1, ERROR 1008, state 1 0; `sudo mysql -e 'SET GLOBAL super_read_only=ON;'` -> 1 1.
Choice not in the brief: `SET SESSION sql_log_bin=OFF` in the drop, so the DROP does not become a transaction of this server's own GTID on a (soon) replica — the panel's promote checks treat such transactions as errant ("Perlu dibangun ulang", promote.go repoint). Measured: gtid_executed unchanged. One invocation, still SET GLOBAL only.
Quoted texts checked at head: A6 (rebuild_load.go errNotReplaced), preflight "server ini masih berisi database lain (…); versi ini hanya mendukung server replika khusus" (preflight.go:544; in the job error it is preceded by the step and check titles), errNoLoadRecord (new), "Can't drop database" (MySQL).
Facts used: Retry relaunches the failed step (Runner.Retry, j.CurrentStep); settleRebuilt keeps the load record on A6 (no clearing on that return); the agent account rule from part C is not involved (the section uses MySQL root).

## D-3
m-1…m-5 were done in D-1. Now: m-6 (Perketat refused "selama bangun ulangnya masih berjalan"), m-7 (dump accounts "nama berawalan `dbpanel_dump_`" in the approval list), the out-of-scope line ("membuat akun replikasi `dbpanel_repl@'%'` (hanya bila belum ada baris `dbpanel_repl` di host mana pun; akun lain, juga akun dump, ditolak)"; pins.go createReplUser/strictReplUser, createDumpUser role == master only), m-8 ("non-sistem"), m-9 ("Sejak perannya `unassigned`, agent tidak lagi memasang baca-saja:"), m-10 ("situs atau host lain"), m-11 ("(bila terpasang) tetap ada"), m-12: verified — a stored non-local or unresolvable host fails every Open (OpenDB); the health snapshot carries the text as MySQLError; the panel shows it as the detail of status "Tidak terjangkau" for a master and for a judged (ready/attention) replica, as "Pesan MySQL" under "Detail teknis" on the master page and in the replica page's health table; for a quarantined old master the status is DeriveQuarantined's fixed text and it is not shown. README now says exactly that, and that the DNS text (part C) applies to an unresolvable name.

## D-5
RebuildApprovalUsable: a not-required answer must also carry no Approved, Source, Addr, RemainingSec, AgeSec (Invalid/InvalidCode not listed by the brief; left). Verified the agent's not-required answer is the zero value (strict.go rebuildApprovalState). Tests: five new unusableApprovals cases. RED: "not required approved: usable" (the loop's Fatal stops at the first); GREEN ok.

## D-6
localApprovalDetail writes nothing when the approval was not read (refused before the agent was asked, agent error, or an unusable answer — ApprovalSeen.Read false); false only for a read "not approved". Test TestRebuildAuditTellsNotReadFromNotApproved (new) and TestRebuildAuditHasLocalApproval's offline Setujui case changed from `"local_approval":false` to "no local_approval" (exactly what N-3 changes). RED: the not-read entry held local_approval false; GREEN ok. README audit sentence extended (+ a follow-up commit naming the unusable answer).

## D-7
Permissions-Policy without interest-cohort (router.go, TestSecurityHeaders expectation changed — RED then GREEN).
Browser harness (scripts/e2e_browser_confirm.js, Playwright 1.63, Chromium build 1243 here) can observe console messages, requestfailed and responses, so the check now fails on: any console error; any failed request except net::ERR_ABORTED (requests a navigation cuts off); any answer >= 400; except the provoked wrong-password POST /confirm (401) and its console line. RED evidence: with that exclusion disabled the run failed with "console error: Failed to load resource: the server responded with a status of 401 (Unauthorized) (https://127.0.0.1:…/confirm)". GREEN: `go test -tags e2e_browser -count=1 -run TestE2EBrowserConfirm ./internal/panel/` ok (33 s).
Finding: in this Chromium the Permissions-Policy notice is a console WARNING, not an error, and with the old header it reported only "Unrecognized feature: 'bluetooth'" (4-5 times), nothing for interest-cohort. So: the check (errors only, as the brief says) would not have caught the interest-cohort notice; `bluetooth=()` still produces that warning on every page with the new header. I did not remove bluetooth (not asked); owner decision. The browser test is not part of make test / make test-e2e (build tag e2e_browser); I ran it myself.

## Gates (round 2, head 552b47b, one at a time)
1. `go test ./internal/panel/... -race -short -timeout 60m` — exit 0, 152 s
2. `go test ./internal/agent/drivers/mysql/... -race -short -timeout 60m -run 'TestConfigureReplication|TestStrict|TestQuarantine|TestRebuild|TestSettleRebuilt|TestPin'` — exit 0, 241 s
3. `make test` — exit 0, 2553 s
4. `make test-e2e` — exit 0, 627 s
Also: `go test -tags e2e_browser -count=1 -run TestE2EBrowserConfirm ./internal/panel/` — ok (33 s). gofmt clean, go vet ./... clean, `graphify update .` after code commits, `bash scripts/tailwind.sh` run (no CSS change).
Pushed: origin/claude/install-superpowers-plugin-ev2k9i = 552b47b; git status clean.

## Concerns (round 2)
- D-9 Resync ulang: hidden although it can succeed with a valid approval (it is no rebuild and leaves the server recorded as old master); the addendum's rule names only "cannot succeed". Owner may disagree.
- D-9: the resync/detach POST endpoints are not refused in this state (only not offered).
- D-9 monitor: start-lock and bad-start alerts are silent while the role is replica + failed (recorded risk; unchanged).
- D-2: `SET SESSION sql_log_bin=OFF` added to the drop (my choice, measured).
- D-6: two commits (README follow-up).
- D-7: this Chromium logs Permissions-Policy notices as warnings and flags `bluetooth`, not `interest-cohort`; the errors-only check would not catch either.

## D-10
Commit 6799dc3 fix(panel): refuse resync and detach of an old master whose rebuild failed. Pushed; origin = HEAD = 6799dc3; tree clean.

One definition: the predicate moved from the web layer to `orchestrator.AwaitingRebuild(st *store.Store, srv store.Server)` (approval.go); web's `oldMasterAwaitingRebuild` only calls it. `ErrAwaitingRebuild` = exactly `server ini master lama yang masih dikarantina; pakai Bangun ulang sebagai replika`. `(*Runner).refuseAwaitingRebuild(serverID)` returns it for a server with role replica that matches (store reads only).
Refused, before any agent call and before any store change:
- StartResync: first thing under startMu.
- DetachReplica, both stop variants: in detachable (before stopReplica/resetReplica/releaseRole) and again under startMu just before RemoveReplica.
- StartCreateReplica when NOT a rebuild (point 2, see below): after refuseBusy, before the "failed attempt removed" block, which would otherwise remove the failed row and set the role to quarantined.
Web: the handlers already render orchestrator refusals through replicaError (replica page, 400, flash). No new template.

Tests:
- Orchestrator TestFailedRebuildRefusesResyncAndDetach (approval_test.go): a real failed rebuild (fails at configureReplication) -> AwaitingRebuild true; StartResync, DetachReplica(false), DetachReplica(true), and StartCreateReplica without RebuildOldMaster each fail with the exact text; afterwards the row is still failed, the role still replica, no job; no command reached the old master's agent after the mark. Ordinary failed replicas (rep-b, rep-d): AwaitingRebuild false, DetachReplica(keep) succeeds, StartResync starts a job (waited to its end).
- RED: compile failure first (AwaitingRebuild undefined), then after the move behavioural: "StartResync = <nil>, want …"; the Buat replika case: "StartCreateReplica (not a rebuild) = server tujuan adalah master lama yang dikarantina; pakai Bangun ulang sebagai replika, want …" (row and role had already been changed by then). GREEN ok.
- Web TestFailedRebuildResyncDetachRefused: with the fake orchestrator returning ErrAwaitingRebuild, POST /replicas/1/resync and /detach (keep, stop) answer 400 with the sentence on the replica page and the resync is audited. It passed at once (it pins the existing refusal rendering, as point 3 asks).

Point 2 — other entry points for a server in this state (role replica, failed row, old master):
- StartAddDatabases / StartRemoveDatabases: refused by changeableReplica (state must be ready or attention: "replika belum siap (status …)"). Unchanged.
- StopReplica / StartReplica / Relock: refused by controllableReplica (ready or attention only). Unchanged.
- RepointNow: refused (state must be needs_repoint). Unchanged.
- Promote: as candidate, not eligible ("Replika gagal", and also the start lock present); as one of the other replicas, the promote's repoint step skips states other than ready/attention/needs_repoint with a log line. Unchanged.
- StartTightenTLS: refuseBusy on replica rows, and tightenSkip leaves out rows that are not ready/attention (TestTightenTLSLeavesOutQuarantined covers a quarantined server with a failed row). Unchanged.
- StartMySQLTLS (single server certificate): NOT refused for role replica (only quarantined is). Its job sends tlsRequest/tlsInstall (and may restart MySQL), which the quarantined agent runs only with a valid approval. It removes no row, changes no role, and changes no data, so per the rule I left it and report it. RenewMySQLTLS skips only role quarantined, so a daily renewal of an expiring certificate could start for such a server and would fail without an approval.
- DeleteServer: refused with ErrServerIsReplica (a registered replica). Unchanged.
- StartPrepareMaster: refused (role replica). StartLeftover / StartSalvage: refused (role must be quarantined). Unchanged.
- StartCreateReplica WITHOUT RebuildOldMaster (Buat replika with this server as target, which the form lists as a retry target): before, it removed the failed row and set the role to quarantined, then refused. That changes the row and the role without the rebuild's checks, so it is now refused the same way, with no store change. With RebuildOldMaster it proceeds as D-9 intends.

Gate: `go test ./internal/panel/... -race -short -timeout 60m` exit 0, 165 s (head 6799dc3). gofmt and go vet ./internal/panel/... clean; `graphify update .` run. make test / make test-e2e not run, as addendum 3 says.

Concerns: StartMySQLTLS and the daily renewal are not refused for this state (reported above, not changed per the rule). The "Buat replika" form still lists such a server as a target; choosing it now gets the refusal sentence.

## Part G
BASE abb56bc. Commits:
- 763cc5f fix(panel): end the old master's record when its agent ends the quarantine (G-1)
- 5d4a974 feat(panel): offer "Batalkan percobaan bangun ulang" for a failed rebuild of an old master (G-2)
- 4e7c138 fix(panel): stop offering the leftover check once a rebuild started the copy (G-3)
- fcf82bb fix(panel): leave a failed rebuild's old master out of the Buat replika targets (G-4)
- 68d01b1 fix(panel): keep certificate jobs off an old master whose rebuild failed (G-5)
- 0c88ce1 fix(panel): keep the panel's own quoted phrases and the advised account in job errors (G-6)

### G-1 (N-B1)
Verified first: the agent ends the quarantine only inside mysql.configureReplication (settleRebuilt, its last act); every error return leaves the pin quarantined.
Built:
- jobState.QuarantineEnded (json quarantine_ended).
- In replicaConnect, right after configureReplication returns nil on a rebuild: set the flag, jr.save(), Store.ClearOldMaster(target), and a job log line. This runs before startReplica. replicaVerify's ClearOldMaster stays as a no-op.
- AwaitingRebuild needs no change: with the marker cleared its third condition fails.

Readers of the old-master marker between the configure step and the end of the job, and why clearing earlier is right:
- replicaVerify ClearOldMaster: idempotent.
- web rebuildTarget, oldMasterViews, rebuildURL (box, D-9 lines): with the marker cleared they no longer treat the server as an old master, which is true (the agent's pin is replica).
- AwaitingRebuild / D-10 refusals / quarantinedForTLS: the server is an ordinary failed replica, so Resync, Lepas and TLS apply as for any replica (the agent's pin is replica, no approval needed).
- refuseQuarantinedTLS and tighten: same as above.
- cancelJob keepQuarantined: see below.
- monitor resolveQuarantined / promote.go pending-quarantine resend: read the marker only for role quarantined; during the job the role is replica. Unaffected.
- StartPromote / SwapMaster: overwrites the marker anyway.
- leftoverCheck (oldmaster.go): only for role quarantined jobs. Unaffected.

Cancel after the settle (the note's item 3) follows necessarily, so I built it. Before G-1, a cancel after the settle put the role back to quarantined while the agent's pin was replica, and the marker still made it a rebuildable old master. With G-1 the marker is gone, so that cancel would leave a "quarantined" server with no old-master record (box without a rebuild button). Now cancelJob uses `oldMaster := st.RebuildOldMaster && !st.QuarantineEnded`: keepQuarantined and the releaseRole skip apply only before the settle. After the settle the job is cancelled like any Buat replika: row removed, role unassigned, mysql.releaseRole sent.

Tests (orchestrator/failed_rebuild_test.go, new):
- TestRebuildFailureBeforeSettleKeepsOldMaster: flag false, marker kept, AwaitingRebuild true.
- TestRebuildFailureAfterSettleIsOrdinaryReplica: subtests for startReplica and waitSynced (verify) failing. The flag is set, the marker cleared, AwaitingRebuild false, refuseAwaitingRebuild nil, and DetachReplica works.
- TestRetryConnectHealsQuarantineEnded: the connect step fails before the answer (the same stored state as a panel stop between the answer and the save); a Retry sets the flag and clears the marker.
- TestCancelAfterSettleIsOrdinaryCancel: role unassigned, releaseRole sent.
- Web TestRebuildFailedAfterSettleIsOrdinary: no box, no D-9 lines, Resync and Lepas offered, form 400.

RED: the after-settle subtests ("QuarantineEnded not set …"), the Retry heal test, and the cancel test (role quarantined, want unassigned). The before-settle test passed at once, as expected. GREEN: ok.

### G-2 (N-B2)
Wiring: its own route `POST /servers/{id}/rebuild-attempt/cancel`, with confirm action `cancel_rebuild_attempt` (kind server) and audit action `cancel_rebuild_attempt` (target server, detail {}, outcome). Labels:
- handlers_audit.go "Batalkan percobaan bangun ulang";
- audit_summary confirmActionLabels (same text).

I chose a separate route rather than reusing detach-keep. That keeps the detach audit, its "dilepas" notice and its typed-name dialog meaning what they say. Detach stays refused in both modes (D-10).

Code:
- Store.ReturnToQuarantine (one transaction). It checks: a row exists, the row is failed, the role is replica, and the row's topology has old_master_server_id = server. Otherwise it returns ErrNotFailedRebuild and changes nothing.
- Runner.CancelRebuildAttempt: under startMu; refuseBusy; then the store call. It maps the refusal to "server ini bukan master lama dengan percobaan bangun ulang yang gagal", then replicaChanged and jobEnded. No agent call.

Web:
- In the box (role replica + AwaitingRebuild, i.e. RebuildFailed): the button and the sentence (help line). It also goes in data-confirm-text, prefixed "Batalkan percobaan bangun ulang di <server>. ".
- On the replica page: the same partial (_ui.html "cancel-rebuild-attempt") in place of Lepas replika.
- Redirect notice ?pesan=percobaan-dibatalkan.

After the cancel (all tested):
- the box shows Periksa, Hapus and the rebuild link;
- the form returns 200;
- the failed job is superseded ("server ini tidak lagi terdaftar sebagai replika") and Retry is refused;
- a new rebuild runs to the end;
- the old master can be deleted;
- once the other replicas are detached, the new master can be deleted. Before the cancel, deleting it is refused with ErrServerIsMaster, so the reviewer's point is confirmed and closed.

Tests:
- store TestReturnToQuarantine: happy path; refuses a running (creating) row, an ordinary failed replica, a server with no row, and the state after the settle (marker cleared, the "flagged job" case).
- orchestrator TestCancelRebuildAttempt (no agent command, row and role, superseded, Retry refused, second call refused, ordinary failed replica refused, new rebuild succeeds) and TestCancelRebuildAttemptThenDelete.
- web TestCancelRebuildAttemptOffered (box and replica page; not on a plain quarantined server nor on an ordinary replica) and TestCancelRebuildAttemptPost (403 without a token, 303 with one, audit entry, page afterwards, refusal shown 400).

RED: compile failures for the store and orchestrator (ReturnToQuarantine, CancelRebuildAttempt undefined). I wrote the web tests before the handler but ran them only after it, so there is no recorded web RED for G-2.

### G-3 (N-B3)
Code:
- orchestrator.RebuildCopyStarted(st, id). It returns true when any create_replica rebuild job of this server, created at or after its promotion's start, has CurrentStep >= the index of the stored step "Salin data" (new const stepCopyName). Unreadable params count as started.
- Choice: I check "any rebuild since the promotion", not only "the last" one. An earlier attempt that reached the copy has already destroyed the data, even if a later attempt failed before it.
- StartLeftover and StartSalvage refuse with orchestrator.CopyStartedText (the box sentence) right after oldMasterOf.

Web:
- box field CopyStarted;
- template: copy started → the box sentence; else, in the failed state (RebuildFailed) → the attempt-pending sentence; else the buttons.
- rebuildTarget: when copy started or role replica, the start-late warning keeps only its first part, the "N transaksi … akan hilang" warning is shown only when the copy has not started (after the copy that data is already gone), and then the copy-started or attempt-pending sentence. The "Data tertinggal belum diperiksa …" and "Jalankan Periksa … dulu" texts are not shown there.

Tests:
- orchestrator TestLeftoverAfterCopyStarted, three cases:
  - cancel attempt after copy: both refusals exact;
  - cancel of a running job past the copy: role quarantined, refusal;
  - before copy: the leftover job runs.
- web TestLeftoverWordingAfterFailedRebuild, four cases: failed/quarantined × copy started/not.

RED: the orchestrator test was a compile failure (RebuildCopyStarted undefined); the web test failed on every missing sentence and on the leftover form being shown. GREEN ok.

### G-4 (N-B4)
replicaForm skips failed replica rows whose server is AwaitingRebuild. Test TestBuatReplikaLeavesOutFailedRebuild (an ordinary failed replica is still offered). RED shown by stashing the change ("body contains <option value="1""); GREEN ok. The direct POST is still refused (D-10 test TestFailedRebuildRefusesResyncAndDetach, Buat replika case).

### G-5 (R-5)
I verified that the predicate is the right test at the four places the note names (F7), so I built it. New Runner.quarantinedForTLS(srv) = AwaitingRebuild; a store error counts as quarantined (fail closed). It is used by:
- StartMySQLTLS;
- RenewMySQLTLS;
- refuseQuarantinedTLS (retry of a certificate job);
- StartTightenTLS.

The existing texts are kept. Test TestTLSSkipsFailedRebuild: no renewal, StartMySQLTLS and refuseQuarantinedTLS give the exact text, no command reaches the old master, and the failed rebuild job is not superseded. RED: "renewal started [...]"; GREEN ok.

### G-6
Inventory: operator texts quoting a fixed phrase, and whether they can reach redact.Quotes.
- **Reach Quotes, through jobRun.fail / setStep / the replica's last error / the alert, and logErr:**
  - internal/agent/drivers/mysql/quarantine.go:525 errNoLoadRecord "Ulangi dari tahap ini";
  - rebuild_load.go:164 errNotReplaced (A6) "Database yang tidak ada di master baru";
  - rebuild_load.go:205-206 errListIncomplete (unsupported name) "Ulangi dari tahap ini";
  - rebuild_load.go:210-211 errListIncomplete "Memberi hak SHOW DATABASES di server yang dikarantina" and "Ulangi dari tahap ini";
  - clone.go:375 cloneError "Paksa dump" (a CLONE step failure reaches the job error);
  - internal/panel/orchestrator/create_replica.go:54 errRebuildCutoverPending, promote.go:397 errCutoverStillPending and controls.go:262 (deletable) "Sudah saya alihkan": these reach Quotes only through logErr in the panel log (audit keeps the panel's own errors as written);
  - monitor/promote.go:243 and :247 alert texts ("Sudah saya alihkan", "Ulangi dari tahap ini") are panel alert texts and are covered by the same phrases if they ever pass Quotes.
- **Do not reach Quotes:**
  - internal/agent/cli.go:332 setRoleNoCheck "Melepas kunci start dengan tangan" (CLI output);
  - orchestrator/promote.go:869 job log format (only logf's arguments are censored);
  - promote_view.go:388-389 and web/handlers_promote.go:45, :133 (pages);
  - web/handlers_oldmaster.go:36, :38 (flashes);
  - web/handlers_oldmaster.go:628 msgCancelAttempt and the G-3 msgAttemptPending "Batalkan percobaan bangun ulang" (templates and form warnings, not Quotes). So "Batalkan percobaan bangun ulang" is not in the list.
- The list in code (internal/panel/redact/redact.go panelPhrases) is exactly: Ulangi dari tahap ini; Database yang tidak ada di master baru; Memberi hak SHOW DATABASES di server yang dikarantina; Paksa dump; Sudah saya alihkan.

(b) The agent prints the account as quoteAccount(user@host) = `'user'@'host'`. The proof user charset `[A-Za-z0-9_.$-]+` holds no quote or backslash, so nothing is escaped. The redact rule keeps the pair only when it directly follows exactly `akun MySQL ` or `GRANT SHOW DATABASES ON *.* TO `, the user passes proofUser (copied rule) and the host passes host. MySQL's templates are still checked first.

Tests (redact/phrases_test.go, new):
- the four agent texts plus the listing text with `'dbpanel_admin'@'localhost'`, `'panel_admin'@'10.0.0.5'` and `'app.user-1$'@'%'` come out unchanged, as do both quote styles of "Sudah saya alihkan";
- near misses are censored: longer literal, one character off, other case, phrase in "Duplicate entry", phrase as "Query:" value, account after other words, account after another GRANT, user with a space or a quote, host of digits only, user with no host;
- row data next to a kept phrase: the phrase kept, the data censored;
- fixed point and no leak, with every new input cut at every byte. The existing table tests are unchanged and pass.

Orchestrator test TestListingRefusalReachesJobUnchanged: the job error is exactly "Sambungkan replikasi: " + text, and the replica's last error holds the text. RED (redact.go stashed): "akun MySQL '…'@'…' …"; GREEN ok.

The phrase list and the agent's texts are two places that must change together; phrases_test.go holds copies of the texts. A later agent text that quotes a new phrase needs panelPhrases extended.

### Existing tests changed (part G)
None changed in expectation. D-9/D-10 tests still pass unchanged, since the legacy state (role replica + failed row + marker) is exactly what they build. web_mysql_jobs_test.go fakeOrch gained CancelRebuildAttempt (needed by the interface).

### Operator-facing strings added (part G)
- orchestrator/controls.go errNotFailedRebuild: "server ini bukan master lama dengan percobaan bangun ulang yang gagal" (mine).
- create_replica.go job log: "Karantina master lama berakhir: agent kini menyimpan peran replika." (mine, from my note).
- web/handlers_mysql.go notice percobaan-dibatalkan: "Percobaan bangun ulang dibatalkan; server ini kembali tercatat sebagai master lama yang dikarantina." (mine).
- web/handlers_oldmaster.go msgCancelAttempt (the brief's sentence), msgCopyStartedForm, msgAttemptPending (the brief's).
- orchestrator/oldmaster.go CopyStartedText (the brief's box sentence).
- templates: button "Batalkan percobaan bangun ulang" (_ui.html partial), and data-confirm-text "Batalkan percobaan bangun ulang di <server>. <sentence>" (mine).
- audit labels "Batalkan percobaan bangun ulang".

### Choices not fixed by the brief (part G)
- Own route for G-2 (above).
- "Copy started" counts any rebuild since the promotion.
- G-3: in the failed or copy-started state, the form omits the "transaksi … akan hilang" warning once the copy has started.
- G-5: a store error counts as quarantined.
- G-6: "Paksa dump" and "Sudah saya alihkan" are in the list (found in the inventory).

## Part F
Commits:
- 039a3f5 docs(plan): record the by-hand commands on a quarantined server (part F evidence)
- 11ad6e4 docs: README for the failed-rebuild exit and the by-hand steps on a quarantined server (F-1 … F-7, plus E "README needs" and the coordinator's points)
- 347c75e docs(plan): record a database name with $ in the backup and drop commands
- 82b9df0 docs: quote the database name so the shell does not expand it

Deviation: the README items went into one commit, not one per item. They are interleaved in the same passages, and splitting needs interactive staging, which is not available here. The evidence is its own docs commit.

### What changed, per item
- **F-1:**
  - New section "Memberi hak SHOW DATABASES di server yang dikarantina" right after "Database yang tidak ada di master baru". It covers: when it appears (the agent text quoted), steps 1 role + 1 1 check, 2 the quoted heredoc (with the `$` reason and "copy the account exactly"), 3 the 1 1 check and `SET GLOBAL super_read_only=ON` when not; then Ulangi dari tahap ini; then the missing-copy text.
  - The "Persetujuan" passage now quotes the new listing text and points to the section, adding that it works at both steps with no agent restart, and that only privileges granted directly count (roles, `mandatory_roles` included).
  - Unsupported-name text quoted. What to do: Hubungkan ulang / Hubungkan MySQL on the server page with another account, and a new account created with the same heredoc form (CREATE USER + GRANT ALL, measured).
  - Verified in code:
    - the connect form is shown whenever MySQL is installed (server_detail.html "mysql-account": "Hubungkan ulang" details, or "Hubungkan MySQL" when not connected), independent of role, so it is offered for role quarantined and role replica + failed row;
    - mysql.connect is strictAllowed on a quarantined agent and checks the grants there (connect.go:146-153).
  - Missing-grants case of Hubungkan MySQL (coordinator point 1): it can occur on a quarantined server (connect.go:148-152). A paragraph at the end of the new section says to run the listed GRANTs inside the same heredoc. Measured with two GRANTs of the kind the text lists (plan evidence row). I also added a pointer in the "Mode ketat" bullet where that text is quoted.
- **F-2:** step 2 rewritten (stop or reconfigure the applications; firewall only an addition, with the two reasons), plus the processlist query and the expected rows: `event_scheduler`/Daemon and `system user`, both measured; the agent's account is not shown since the agent is stopped. When MySQL is down, only the applications can be checked.
- **F-3:** drop section:
  - step 1 "Pastikan Anda di master lama" (`cat /etc/dbpanel/role` → `quarantined`, read-only check → `1 1`, else stop, with the reason). The path is config.DefaultDir "/etc/dbpanel" + pins.go RoleFile "role"; writePin content is the role word.
  - step 2 checks on the new master that the database is absent (m-D);
  - step 3 stops the applications using it (m-C);
  - step 4 backup with an explicit stop (file missing, size 0, or grep not 1; the measured "Unknown database" case gives grep 0);
  - step 5 the drop (error/Ctrl-C note);
  - step 6 the 1 1 check, with `SET GLOBAL super_read_only=ON` "hanya di server yang lolos pemeriksaan langkah 1";
  - continuation unnumbered.
- **F-4:**
  - m-A: both by-hand sections and the new one start with `sudo -i`; their commands have no `sudo`. Step 5 of the release now says `mysql`.
  - m-B: stop-halfway sentence for the added line, and a stop when no `init_file=` line is found.
  - m-C and m-D: as in F-3.
  - m-E: dump accounts verified. strict.go: createDumpUser is strictProtected. pins.go refusedByRole refuses only role master's non-matching ones. MySQL read-only refuses CREATE USER except during the load. Wording corrected.
  - m-F: A6 causes.
  - m-G: the local_approval sentence.
  - m-H: "yang pertama atau yang dimulai lagi".
  - m-I: evidence rows.
- **F-5:**
  - New block "Setelah bangun ulang gagal": box and buttons; replica page line and the refusal text; job page line; not a Buat replika target; no certificates; Ulangi disappears after another job or after the cancel; the monitor's start alerts are not judged meanwhile.
  - The "Batalkan percobaan bangun ulang" paragraph quotes its sentence.
  - Data tertinggal: attempt pending, and after the copy, with the box and form sentences quoted.
  - G-1's paragraph: the record ends when the agent answers configureReplication OK; the log line is quoted; cancel after that point.
  - Step 1 of "Mulai bangun ulang baru" points to the block.
- **F-6:** the listing refusal is shown whole on the job page after G-6; the README says so ("halaman pekerjaan menampilkannya utuh").
- **F-7:** the measurement decided no `--force`:
  - heredoc + `--force`, failing GRANT: last statement runs, 1 1, exit 0;
  - `-e` + `--force`, failing DROP: last statement not run, 1 0, exit 1;
  - drop as heredoc + `--force`: 1 1, exit 0, as a recorded option.

  Since `--force` does not work for both forms, both commands stay without it and keep the full error branch. Each section's last numbered step is the `SELECT @@read_only, @@super_read_only;` → `1 1` check, with the fix-up after the role check. The agent's tests run the GRANT sequence without `--force`, the same as the README.
- **E "README needs" 1-4:** all in (listing text and pointer; unsupported name and roles/mandatory_roles; the new section; leftover/salvage need the source pin, with the refusal quoted). Item 5: no contradicting sentence found.
- **Coordinator point 2** (when the quarantine ends): checked at head.
  - dump.go:151 lists `before` right before the load, after read-only is lifted and the binlogs are reset.
  - rebuild_load.go:171-186 notCopied flags a present database only if it is in `before`, or, for named databases, not among them.
  - So on a FULL rebuild a database an application creates during the load is not flagged, and the quarantine ends with it. **Concern (code, not changed):** internal/agent/drivers/mysql/rebuild_load.go:177-182 together with dump.go:151.
  - The README sentence now says exactly that, and the security bullet points to it.
- **Coordinator addition 1** (quoted heredoc and `$`): done for the GRANT section.
  - Drop section: `mysql -e '…'` already puts the name inside single quotes, where the shell expands nothing, so I left it.
  - The backup step had the name and path in double quotes, where `$` expands (measured: `abZZ`). Changed to single quotes in commit 82b9df0, measured with a name holding `$` (dump, grep 1, drop, 1 1; plan rows in 347c75e).
  - A name holding `'` makes the shell reject the command, so nothing runs (safe). A name with a backtick breaks the SQL (error, then the 1 1 check).
- **Coordinator addition 2:** the measurement for both forms is named above and in the plan table.

### Commands added or changed, and how each was run
All ran on scratch mysqld 8.0.46 instances in mktemp dirs under /tmp, as root, with `-S <socket>` added and output paths in the scratch dir instead of /root. Each instance was stopped and removed.
- Measured (plan evidence rows): `mysql <<'EOF' … GRANT … EOF` (success; with `$` in the name; failing; with --force); `mysql -e 'SET GLOBAL super_read_only=OFF; SET SESSION sql_log_bin=OFF; DROP DATABASE `<nama>`; SET GLOBAL super_read_only=ON;'` (success; failing; with --force); `mysql -e 'SET GLOBAL super_read_only=ON;'`; `mysql -e 'SELECT @@read_only, @@super_read_only;'`; `mysql -e 'SHOW DATABASES LIKE "<nama>";'`; `mysqldump … --databases "<nama>" > "<path>"` (existing and missing database) with `ls -l` and `grep -c 'CREATE DATABASE'`; the processlist query on a master and on a replica; the CREATE USER + GRANT heredoc; two GRANTs of the missing-grants kind in the heredoc.
- `cat /etc/dbpanel/role`: run as `cat` on a scratch file holding "quarantined" (this machine has no /etc/dbpanel). The content format was verified in code.
- Unchanged from earlier parts: systemctl commands, printf/chown/chmod, the option-file commands.

### State walks
New section "Memberi hak SHOW DATABASES …":

| Start state | Step | End |
|---|---|---|
| old master, pin quarantined, 1 1, persisted ON, job failed with the listing text | 1: role quarantined, 1 1 → go on | — |
| same | 2: heredoc OK | 1 1, grant present, gtid unchanged |
| same | 3: check 1 1 | then Ulangi dari tahap ini → listing proved (new connection) → job continues |
| wrong server (new master: role master, 0 0) | 1: role is not quarantined / not 1 1 → stop | nothing changed |
| wrong account name typed | 2: ERROR 1410, client stops | 1 0 (read_only stays 1, apps cannot write) |
| same | 3: check shows 1 0 → `SET GLOBAL super_read_only=ON` (role check passed) → 1 1; fix the name, back to 2 | 1 1 |
| stops halfway after 2 failed | operator walks away | 1 0 until a MySQL restart or the fix; the persisted values ON; the agent does not restore it while MySQL runs (stated in the section) |
| agent restarted before Ulangi | Ulangi answers the missing-copy text | new Bangun ulang (section says so) |

Changed drop section:

| Start state | Step | End |
|---|---|---|
| old master in the A6 state (1 1, P ON) | 1: role/1 1 OK | — |
| same | 2 (on the new master): name absent | go on |
| same | 3: apps using the database stopped | — |
| same | 4: dump OK, size > 0, grep 1 | backup kept |
| same | 5: drop OK | 1 1, database gone, no binlog event |
| same | 6: check 1 1 | Ulangi / new rebuild |
| wrong server (new master) | 1: role master or 0 0 → stop | nothing changed |
| name present on the new master | 2 → stop | nothing changed |
| wrong name | 4: mysqldump "Unknown database", grep 0 → stop | nothing changed |
| drop fails (e.g. FK) | 5: error, last statement skipped | 1 0 |
| same | 6: shows 1 0 → `SET GLOBAL super_read_only=ON` (role check passed) | 1 1 |
| drop waits on a metadata lock, Ctrl-C | 5 → 6 check | 1 0 → fixed in 6 |
| stops halfway after 5 | operator walks away | at worst 1 0 (read_only 1) until a restart or the fix |

### Statements I could not verify (removed or reworded)
- "replika lain" holding a copy: removed (m-D).
- "selalu menyala baca-saja" for the leftover option line: reworded to "setiap kali tidak ada nilai lain yang tersimpan" (measured b: a persisted OFF beats it).

### Gates (parts G and F, one at a time)
The last code commit was 0c88ce1; at gate start HEAD was 11ad6e4, and 347c75e/82b9df0 are docs only.
1. `go test ./internal/panel/... -race -short -timeout 60m`: exit 0, 218 s.
2. `make test`: exit 0, 684 s. Go's test cache served 14 packages, the agent packages among them, unchanged since part E's gates.
3. `make test-e2e`: exit 0, 628 s.

TestWSLogsReplacedConnection did not fail. Before the gates, while iterating, one `go test ./internal/panel/...` I started without -short hit the default 10-minute timeout in the e2e package. It was not a gate. I stopped the two mysqld it left under /var/tmp and removed their directories.

Also: `go test -tags e2e_browser` was not re-run (no template it visits changed in a way the check covers beyond the pages in the e2e run). gofmt and `go vet ./...` are clean, `graphify update .` ran after each code commit, and `bash scripts/tailwind.sh` ran (no CSS change).

Pushed: origin = HEAD = 82b9df0; `git status --short` is empty.

### Concerns (parts G and F)
- Agent code, not changed: on a FULL rebuild a database created by an application during the load is not detected and the quarantine ends with it (rebuild_load.go:177-182 notCopied with dump.go:151 listBefore). The README now says so.
- README items went into one commit, not one per item.
- G-2 web tests were written first but only first run after the handler (no recorded web RED).
- The monitor still does not judge old-master start alerts while the role is replica + failed row (recorded; README says so).
- The README avoids `--force` because it does not apply to `mysql -e`. Converting the drop command to a heredoc with `--force` would make both commands lock the server again by themselves (measured, recorded in the plan), but then `mysql` exits 0 on an error. Owner decision.

## Part H
BASE 82b9df0. Commits:
- 3226818 docs(plan): record backup checks, the missing-grants form and history for part H (evidence)
- 5ec0d6c docs: make the by-hand backup check catch a cut dump and keep an old backup (H-1a, H-1b, H-1c)
- 5cea508 fix(panel): refuse the leftover check only on evidence that the copy began (H-1d, with H-4)
- f30bab9 fix(panel): keep the settle flag and the old-master marker consistent on store errors (H-2)
- 85c4b06 fix(panel): keep the sanitizer a fixed point and anchor the advised account (H-3)
- 1ff5c98 docs: README minors of the part F re-review and the three copy states (H-5)

H-4 shares the H-1d commit. The promotion identity is part of the new copy-state function, so splitting it out would have meant a throwaway intermediate.

### H-1d table (agent code at head, read only)
Order in which the panel sends a rebuild's commands to the old master in the step "Salin data" (`replicaSnapshot`, create_replica.go):

| # | Command / event | What it changes locally | Can leftover transactions still be checked and salvaged afterwards? | Refusals answered before any local change | What the agent emits only after local data began to change |
|---|---|---|---|---|---|
| 1 | `preflightBeforeDrop`, CLONE wait, `replAccount`, `verifySnapshot`/`trustCA` (panel side or read-only agent calls) | nothing | yes | — | — |
| 2 | `mysql.dropDatabases` (only for dump with a WipeList, not on reattach) | drops the listed databases, **without binary logging** (databases.go DropDatabases: "without binary logging"; liftReadOnly/restore around it) | **yes**: the binary log is untouched, so `mysql.leftover` and `mysql.salvage` still read the transactions. No DROP event is added, so a later Amankan does not write a DROP. | pins/strict refusal (`strictProtected`), answered to the call | nothing (synchronous answer) |
| 3a | `mysql.dumpFrom` (agent job) | after "From here on local data changes" (dump.go:131): STOP REPLICA, `liftReadOnly`, **`resetBinaryLogs`** (dump.go:142-146: the binary log is emptied, so the leftover transactions are unrecoverable), then `listBefore` and the load | **no**, once `resetBinaryLogs` ran | start refused by pins/strict (answered to the call, no job). Inside the job before line 131: `checkClients`, `completeUserDatabases` (C-1/E-6 listing refusal) end the job with an error before any change | `j.Logf("info", "dumping …")` and `j.Progress(0, "dump dimulai")` (dump.go:154-155), both **after** the binary log reset. No Progress before. |
| 3b | `mysql.cloneFrom` (agent job) | `installClonePlugin` (log only, no data), SET GLOBAL clone_* (no data), `liftReadOnly`, then **CLONE INSTANCE** replaces the data directory | no, once CLONE INSTANCE runs | start refused by pins/strict; in the job: plugin/donor setup errors before liftReadOnly | `j.Logf("cloning from …")` and `j.Progress(0, "CLONE dimulai")` (clone.go:122-123), **immediately before** the CLONE statement. Taken as evidence: nothing but the statement follows. |
| 4 | job result (OK) | copy done | no | — | the result |

Built on that:
- `CopySent` is saved before the copy command is sent (`markCopySent` at the three `runAgentJob` sites in `copySnapshot`).
- If the agent answered the start call with a refusal (`startRefused`: a `*proto.Error` that is not a timeout or offline, recorded in `callWithin` before mapping) and this attempt set the flag, the flag is cleared.
- `CopyBegan` is set on the first `EventJobProgress` of the copy job (`copyWatch`) or on a successful result.
- Stage 2, `dropDatabases`, is deliberately not evidence: it leaves the binary log intact (table above).
- A copy job that ends with an error before any change (an early refusal inside the job) and sent no progress stays **uncertain**. The panel cannot tell that from progress events lost while the agent was disconnected.

States (`RebuildCopyState`, oldmaster.go):
- **NOT STARTED:** nothing sent, or the agent refused the copy call directly.
- **UNCERTAIN:** sent with no evidence; for old-shape jobs: at the step with an agent job id or a snapshot GTID; unreadable params.
- **STARTED:** past the copy step, `CopyBegan`, or old shape past the step.

The most advanced state across this promotion's rebuild jobs wins. Only STARTED refuses Periksa/Amankan (`refuseAfterCopy`) and shows the two "sudah mulai menyalin" sentences.

UNCERTAIN in the plain quarantined state:
- the box shows the new warning banner above the buttons;
- the rebuild form shows the same sentence (in place of the "sudah mulai menyalin" one, next to the normal leftover warnings);
- in the failed state the attempt-pending sentence is shown, as in state 1.

Status advice (N-G3): `copyStatusAdvice` replaces the monitor's StatusQuarantined advice with `CopyStartedText` on a quarantined server in state STARTED. It applies on the server page box and in `/servers/{id}/status-fragment`; state 1 and 3 keep the advice.

Still untrue in the monitor (not changed, rule): `monitor/alerts.go:385` startLateText and `monitor/promote.go:695` say "jalankan Periksa data tertinggal" for a master_start_late alert. In state STARTED that alert cannot close through a check.

Tests (orchestrator copy_state_test.go):
- NothingSent: dump, createDumpUser fails, so the copy is never sent → not started, Periksa accepted after Batalkan.
- RefusedDirectly: cloneFrom ParamError → not started, accepted.
- AnswerLost: dropReply → uncertain, accepted.
- Evidence: a progress event, then the job fails → started, refused.
- EarlierEvidenceCounts: two attempts, only the first with evidence → started.
- StoredJobs table: old shape before / at / at+agent job / at+GTID / past; past but before the promotion; unreadable; new shape sent / evidence / clock stepped back; another promotion id.

Web tests:
- TestCopyUncertainWording: failed state → attempt-pending; plain → warning + leftover form; started → box sentence, no form.
- TestCopyStartedStatusAdvice: uncertain keeps the advice, started replaces it, on the page and in the fragment.

RED:
- with the part G rule substituted into RebuildCopyState: NothingSent, RefusedDirectly and AnswerLost failed ("state = 2"), and four StoredJobs rows failed;
- the status-advice test failed with the function disabled;
- the new web wording test was a compile failure first.

GREEN: ok. Existing tests changed: TestLeftoverAfterCopyStarted now calls RebuildCopyState instead of RebuildCopyStarted (the function was replaced).

### H-1a / H-1b / H-1c (README)
Verified and measured (evidence rows in 3226818):
- complete dump: exit 0, trailer present;
- missing database: exit 2, grep 0;
- aborted dump (a view on a dropped table): exit 2, 1630 bytes, grep **1**, last line `DROP TABLE IF EXISTS `m`;` and no trailer. So the old check would have passed it; the new four-part check stops.
- A view with a dropped definer did not abort on 8.0.46 (row recorded).
- File-exists branch: `ls` shows the old file; `SHOW DATABASES LIKE` shows whether the database is still there; `mv` keeps the old file intact.

Agent restart re-asserts read-only (H-1c): `guardQuarantined` runs on the first guard tick after the agent starts (`guard.up` false), every `monitor.GuardEvery` = 5 s. It calls `persistReadOnly` (SET PERSIST read_only/super_read_only ON) when MySQL answers and no rebuild load runs.
- `systemctl restart dbpanel-agent` was not run: this machine has no dbpanel-agent unit. That is the same unit name as in the other README commands (scripts/install.sh).

Changed:
- step 1 of both sections: the role decides "wrong server"; `1 0` → `SET GLOBAL super_read_only=ON` and check; `0 0` or no answer → stop, restart the agent, check;
- drop step 4: no existing file / check the database / mv; dump with `; echo "keluar $?"`, size, grep, `tail -n 1` trailer; stop on any error; copy off the server.

### H-2
Built:
- `saveErr` (save without swallowing) used only at the settle: the flag is set, saved, and on failure reset and the step fails before the clear.
- The clear runs on every pass once the flag is set (`first` only gates the log line).
- `CancelRebuildAttempt` refuses with the existing errNotFailedRebuild text when the server's newest create_replica job is a rebuild with QuarantineEnded (`latestRebuildEnded`).
- Seams: Runner.settleSave and settleClear (tests only).

Tests:
- TestSettleSaveFails: flag not stored, marker kept; a Retry sets both.
- TestSettleClearFails: flag stored, marker kept, Batalkan refused, role unchanged; a Retry clears the marker.

RED: compile failure (the seams are new); behaviour: both would fail on the part G code (the save error was swallowed, and the clear ran only when the flag was first set). GREEN ok.

### H-3
- **N-G4:** Quotes tracks whether the output ends with a kept literal (`kept`). A censored token (closed or unclosed) written right after a kept literal ending in `'` uses `"` instead of `'`. This holds for phrases, advised accounts and the pre-existing dbpanel_ account path.
- **N-G5:** `accountLeads` are now the agent's whole sentence parts, verified against rebuild_load.go:208-209 character for character:
  - `tidak bisa dipastikan lengkap: akun MySQL `
  - `di server ini: GRANT SHOW DATABASES ON *.* TO `

  A lead counts only at the message start or after a non-word byte (`word` is the existing helper, so a multi-byte letter before it also counts as a word).
- Tests:
  - TestQuotesAdjacentFixedPoint: seven inputs including the re-review's, every cut, no leak;
  - TestQuotesAdvisedAccountAnchored: the re-review's four shapes plus a word-glued lead and a UTF-8-glued lead are censored;
  - the agent texts still pass (TestQuotesKeepsPanelPhrases and the orchestrator test).
- RED by stashing redact.go: fixed-point failures ("'Paksa dump''…" → "'…") and seven "keeps" failures. GREEN ok.

### H-4
`jobState.RebuildPromotionID` is set at `StartCreateReplica` for a rebuild. A store error leaves it 0, which falls back to the time rule. `RebuildCopyState` matches by it; jobs without it use the time comparison. Tests: StoredJobs rows "clock stepped back" (counted) and "another promotion" (not counted).

### H-5 (README) — what changed, and how each command was run (evidence rows in 3226818 unless noted)
- **m-1:** the paragraph tells the operator to end every GRANT with `;`, also the last. Measured as the agent prints it: ERROR 1064, exit 1, `1 0`. With the semicolons: exit 0, `1 1`. The word "heredoc" is gone: "Ketiga baris itu satu perintah: `mysql` membaca baris di antara `<<'EOF'` dan `EOF` sebagai masukannya". The ambiguous "baris GRANT" is now "perintah `GRANT SHOW DATABASES …;` di tengah baris perintah itu". The "Mode ketat" bullet points to the paragraph.
- **m-2:** compare the database lists of the two server pages, verified: server_detail.html "mysql-account" lists Inventory.Databases on every server page. Also what to do with a database found only on the rebuilt server.
- **m-3:** `_` and `%` wildcards.
- **m-4:** restart the agent when stopping at step 2 or at step 3 (no init_file line), and run the step-5 check if MySQL is up.
- **m-5:**
  - the events query (measured);
  - `event_scheduler=OFF` added with `super_read_only=ON` in step 4. Measured: `@@event_scheduler` OFF, no daemon, the event does not write. I wrote that a persisted value would win; that comes from measurement (b) for read_only, not measured for event_scheduler.
  - the connection check is repeated at step 5;
  - both lines are removed in step 6 and on abandonment.
- **m-6:** the "Akun admin MySQL ini kurang hak" banner, the same way.
- **m-7:** names with `'`, `"` or a backtick: do not use these commands.
- **m-8:** the Batalkan purpose sentence now applies only "bila bangun ulang belum mulai menyalin data", plus H-2's refusal text quoted.
- **m-9:** `EOF` at the start of the line; Ctrl-C when the shell keeps showing `>`.
- **m-10:** `mysql -u root -p <<'EOF'` measured through `script` (password prompt on the terminal, exit 0, `1 1`); the sentence is kept.
- **m-11:** the new account is created inside interactive `mysql`, measured through `script` with MYSQL_HISTFILE. The mysql history omits the `IDENTIFIED BY` line, and the shell history gets nothing from inside mysql.
- The data tertinggal passage follows the three states, quoting msgCopyUncertain and the status advice.

### Operator-facing strings added or changed (part H)
- web/handlers_oldmaster.go msgCopyUncertain: the brief's sentence, verbatim.
- Status advice: in state STARTED it shows orchestrator.CopyStartedText (an existing text).
- H-2 refusal reuses errNotFailedRebuild ("server ini bukan master lama dengan percobaan bangun ulang yang gagal").
- "simpan keadaan pekerjaan: <err>" (create_replica.go, the settle save failure; mine).

### State walks, by-hand sections (README at 1ff5c98)
Drop section:

| Case | Walk | End |
|---|---|---|
| Right server (pin quarantined, 1 1) | 1 ok → 2 (new master) empty → 3 apps stopped → 4 no file, dump exit 0, size>0, grep 1, trailer → copied off → 5 drop → 6 `1 1` | database gone, backup verified and copied |
| Wrong server (new master: pin master) | 1: role not quarantined → stop | nothing changed |
| Interrupted earlier run (1 0) | 1: role ok, `1 0` → SET ON → `1 1` → continue | 1 1 |
| 0 0 / MySQL not answering | 1: stop, restart the agent, check again | read-only set by the guard (if MySQL answers) |
| Aborted dump | 4: `keluar 2`, last line not the trailer → stop | nothing dropped |
| Missing database (typo) | 4: "Unknown database", `keluar 2`, grep 0 → stop | nothing dropped |
| Second run (first run dropped it) | 4: `ls` shows the file → `SHOW DATABASES LIKE` empty → skip 4 and 5 | old backup intact |
| Second run (database still there, old file present) | 4: mv old file aside → dump | both files kept |
| Drop fails / Ctrl-C | 5 → 6 shows `1 0` → SET ON | 1 1 |

GRANT section:

| Case | Walk | End |
|---|---|---|
| Right server | 1 ok → 2 block → 3 `1 1` → Ulangi | grant present |
| Wrong server (pin not quarantined) | 1 → stop | unchanged |
| Interrupted earlier run (1 0) | 1 → SET ON → `1 1` → 2 | ok |
| Typo in account | 2: ERROR 1410, stops → 3 `1 0` → SET ON → fix, 2 again | 1 1 |
| Missing-grants variant pasted without the final `;` | 2: ERROR 1064 → 3 `1 0` → SET ON; the paragraph says to end every statement with `;` | 1 1 |
| EOF indented | shell shows `>` → Ctrl-C (nothing ran) → retype | unchanged |
| Root with password | `mysql -u root -p <<'EOF'` asks on the terminal | as right server |

### Not closed (and why)
- Monitor texts startLateText (`monitor/alerts.go:385`) and `monitor/promote.go:695` still advise "Periksa data tertinggal" in state STARTED, and that alert cannot close by a check. Monitor change not allowed.
- Monitor still silent for old-master start alerts while role replica + failed row (R-G1 / R-5).
- R-G2 / R-4: when the configure answer is lost after the agent settled, the panel still shows an old master and Batalkan is accepted (no flag). Nothing in the panel can know.
- R-G3 / R-3: a second promote of the topology while a failed rebuild is pending.
- Full-rebuild databases created during the load are not detected (agent code; README states it).
- A copy job that the agent ends early (before local changes) with its progress lost stays UNCERTAIN (offered with a warning) rather than NOT STARTED. That errs in the allowed direction.
- `-e` drop form with a name holding `'`: the shell waits for more input rather than refusing (README now says not to use these commands for such names).
- The re-review's out-of-scope items (users.go:545 dump-grant advice censored, RenewMySQLTLS on a GetServer error, the agent texts' missing `;` and `$` in the unsupported-name text) are agent or other code, not changed.

### Choices the brief does not fix
- CLONE's "CLONE dimulai" progress (emitted right before CLONE INSTANCE) counts as evidence.
- `dropDatabases` alone is not evidence (binary log intact, drop not binlogged).
- A direct refusal clears CopySent only when this attempt set it.
- The H-2 refusal reuses the existing text.
- Status advice replaced only in state STARTED and only for role quarantined.
- event_scheduler=OFF goes in the same step as super_read_only=ON.

### Gates (part H, one at a time)
1. First run, `go test ./internal/panel/... -race -short -timeout 60m`: exit 1, 190 s. The only failure was TestWSLogsReplacedConnection. Run alone it passed (`ok`, 1.3 s).
2. Re-run of the gate: exit 1, 17 s (cached), the same test failed again.

Investigated:
- The hub calls OnOnline inside `Attach` (hub.go:160).
- The websocket handler logs "agent connection replaced …" only after `Attach` returns (agentapi/ws.go:48-50).
- The test read the log right after the second OnOnline, a test-side race.

Fixed in the test only, commit 8b0389d `test(panel): wait for the replacement log line in TestWSLogsReplacedConnection`: it waits up to `wait` for the line. The agentapi package then passed three times in a row under -race.

Final run on 8b0389d:
1. `go test ./internal/panel/... -race -short -timeout 60m`: exit 0, 121 s.
2. `make test`: exit 0, 686 s (agent packages from the test cache; unchanged since part E).
3. `make test-e2e`: exit 0, 617 s.

Pushed: origin = HEAD = 8b0389d; `git status --short` is empty.
Additional commit (not in the brief's list): 8b0389d (the flaky test fix above).

## Part I

Status: done. BASE 78a8c9f. Commits: 9d8a422 docs(plan) evidence rows; cf83f05 docs(readme) fixes; 7e65a81 docs(readme) line rewrap (one line exceeded the wrap width after m-H7). `git diff --stat 78a8c9f..HEAD`: README.md and the plan file only (101+/32-). Pushed; `git status --short` empty; HEAD = origin = 7e65a81. Evidence rows: plan file, new subsection "Bukti langkah tangan, tinjauan ulang H (bagian I)". Scratch mysqld 8.0.46 under /tmp/dbpanel-measure.*: stopped and removed, with no mysqld left running.

### Per item (README line numbers at 7e65a81)

- **I-H1:** I verified this in the code at head.
  - In `replicaSnapshot` (create_replica.go ~753-760), for a dump that is not a reattach, `mysql.dropDatabases(WipeList)` runs before `copySnapshot`.
  - `WipeList` is the approved list of databases the target also shares with the master (create_replica.go ~494-522). The approval happens in a job wait (WaitWipe, the approve dialog on the job page), not in the form. The README says "daftar yang Anda setujui saat pekerjaan meminta persetujuan penghapusan" and not "di form".
  - `DropDatabases` (databases.go) works like this: refuseRunningReplica/refuseOnSource, then liftReadOnly, then DROP with `sql_log_bin = 0`, then restore.
  - README 2013-2022: the bullet now uses the review's corrected text, with only that one wording change.

  Other sentences I found and corrected:
  - (a) README 1974-1978, after the quoted Batalkan panel text "data di server ini tidak diubah". That text comes from the code and I did not change it. I added: "Artinya tombol itu sendiri tidak mengubah data; percobaan bangun ulang sebelumnya bisa sudah menghapus atau mengganti data di server ini (lihat di bawah)."
  - (b) README 2318-2321, the bullet "Menjadi master lagi" (set-role master fallback). I added a warning that a failed or cancelled dump rebuild may have dropped the databases the new master also has, and a step to check `SHOW DATABASES` first.

  Sentences I checked and left unchanged:
  - README ~1384, the promote partial-replica sentence "data itu tetap utuh di master lama". It covers databases NOT on the new master, and those are never in WipeList.
  - "Hapus server … tidak mengubah apa pun" (2258). It describes the delete action itself.
- **m-H1:** README 1835-1841 now uses the corrected text. **Segarkan** is verified in web/templates/server_detail.html:263-264: POST /servers/{id}/mysql/refresh, shown when the MySQL account is connected, in the MySQL box. I added a parenthesis that the list may still be the one from before the load.
- **m-H2:** both sections, README 2072-2079 and 2219-2226, use the corrected text. The service names match the rest of the README (`dbpanel-agent`, `mysql`/`mysqld`). The order "agent first, then MySQL" matches guardQuarantined: it persists read-only when MySQL answers after not answering. These are systemctl steps, so I did not run them here (no systemd service in this container).
- **m-H3:** README ~2104-2120. I added "hanya nama yang persis sama yang dihitung". The `mv -n … -cadangan-"$(date …)".sql` command plus an `ls -l` check is in a code block. Measured with the exact quoting, on a name containing `$`:
  - the target is free: exit 0, the file is moved;
  - the target exists: "mv: not replacing", exit 1, both files are unchanged.
  
  Finding: GNU coreutils before 9.2 exits 0 silently in that case, so the README checks with `ls -l` afterwards, not with the exit code. It also tells the operator to wait a second and repeat.
- **m-H4:** README 2137-2142. I added the scp example and a `sha256sum` comparison on both servers. I ran `sha256sum`: a copy gives the same hash, and a truncated file gives a different one. **scp was not run:** there is no second host here.
- **m-H5:** README 1991-1993 now uses the corrected wording. Verified in the code:
  - dump.go:142 runs resetBinaryLogs only when `gtidMode == "ON"`. This comes after liftReadOnly and before listBefore and "dump dimulai" (155).
  - clone.go:123 reports "CLONE dimulai".
  - Note: the dump's "data lama mulai diganti" really happens during the load, after the Progress. The wording follows the review.
- **m-H6:** README 1917-1921. Measured with interactive mysql through `script`:
  - The account already exists, and the README block is typed as written. The result is ERROR 1396, the session continues, GRANT ALL goes to the existing account, and the state ends `1 1`. The new password fails with 1045, the old one works. This confirms the review.
  - The added instruction (after 1396, type only `SET GLOBAL super_read_only=ON;` then `exit`) ends `1 1`, with grants unchanged.
  - Batch mode without --force stops at line 3 with exit 1 and leaves `1 0`. That is evidence only: the README does not use batch mode for this block.
- **m-H7:** measured. With `SET PERSIST event_scheduler=ON` and the option line `event_scheduler=OFF`, after a restart the value is `ON` (VARIABLE_SOURCE PERSISTED). After RESET PERSIST it is `OFF` (EXPLICIT). I kept the clause and made it precise at README 2408-2409: "pernah disimpan `ON` dengan `SET PERSIST`: nilai tersimpan itu yang dipakai".
- **m-H8:** README 1981-1983 uses the corrected text. Verified: `latestRebuildEnded` (controls.go ~185-201) checks `QuarantineEnded` on the newest create_replica job; the error text is controls.go:149.

### Not closed / concerns
- The panel's wait message for WaitWipe on a rebuild calls the old master's own databases "database dari percobaan sebelumnya" (create_replica.go:498). That is misleading on an old master, where they are its production databases. Changing it is a code change, so I left it; this is a follow-up for the code.
- The Batalkan quoted text "data di server ini tidak diubah" is panel text. The README now qualifies it, but the panel still says it. That would also need a code change.
- scp and the systemctl steps (m-H2) were not run here.
- I did not run `graphify update .`: no code changed, and the rebuild would have touched files outside the allowed set.
