## Final whole-branch review: Plan 2 "Buat replika MySQL" (a6e9a25..67997c5)

### Review method (passes taken)
I followed the project rule and ran `graphify query` before reading any source. I read the full ledger, spec §4, §6, §7.3, §7.3a, §7.4, §8, §9, §10a and §11, and the plan's Global Constraints, Review Focus and Tasks 12–15. I then reviewed the code in five passes:
1. **Panel orchestrator:** runner.go, prepare_master.go, create_replica.go, change_databases.go, agentjob.go, plus the hub, listener, localagent and panel.go wiring.
2. **Web layer:** handlers_mysql.go, handlers_jobs.go, the job, replica and master templates, and app.js.
3. **Agent driver at the seams:** registration and validators against every command/params pair the orchestrator sends; clone.go, dump.go, snapshot.go, replication.go, preflight.go, configure.go, service.go, realhost.go, conn.go, install.go (skimmed), cli.go `RegisterMySQL`.
4. **Store:** migration 0003 and the migrate guard.
5. **Docs:** README, Makefile, docs diff.

I read the new files from the repo at HEAD (identical to the diff) and used the packaged diffs for the modified files.

Only one command ran beyond reading. To settle a doubt about restarts on aaPanel-style configs, I ran `mysqld --validate-config` as root against a scratch config with no `user=`. It works, so that concern is dropped. No mysqld was left running, and the working tree, index and HEAD are unchanged.

### Strengths
- **GTID design for adding databases matches spec §7.3a closely.** The steps are:
  - restore the stored filter, then wait for catch-up;
  - stop replication;
  - dump in COMMENTED mode and take P2 from the dump's own header;
  - `START REPLICA UNTIL SQL_AFTER_GTIDS=P2` with the old filter;
  - widen the filter.

  The reset-before-snapshot rule (`create_replica.go:367-380`) closes the duplicate-replay hole found in T12.
- **The dump checks everything before it changes anything.** It proves both logins and runs a schema-only trial dump with `--set-gtid-purged=OFF` (so no FTWRL) before any destructive step. It uses a 0600 `--defaults-file` as the first argument and never writes passwords to argv or logs. The E2E test checks that the replication password is absent from the store plaintext, job rows and process logs.
- **Confirmations are bound properly.** The leftover-database wipe is tied to a hash of the exact list shown. The form's ConfirmWipe only pre-approves the CLONE wait.
- **The job lifecycle is solid:**
  - actions are bound to the step they were clicked on;
  - finished jobs are immutable (`ErrJobFinished`);
  - Resume fails jobs that were running when the panel stopped;
  - lost events are recovered by polling `job.status`;
  - an agent restart is detected (CodeBadParams);
  - an agent offline longer than the grace period fails the step with "agent X terputus saat <tahap>" (Review Focus #3).
- **There is one event path** (`Hub.OnEvent`), and the embedded agent registers the same commands as a remote one through `agent.RegisterMySQL`.
- **Layout and config handling is careful:** layout discovery does not need a running mysqld; the managed include sorts last as `zz-`; there are block markers, config validation with rollback, and a check after restart that the managed values actually took effect.
- **The new pages are CSP-safe.** Log lines are inserted with `textContent`; the server re-checks target eligibility; database names are checked against the master inventory; the identifier rule cannot produce a name that looks like a command flag; `_` is escaped in filter patterns.

### Issues

#### Critical (Must Fix)

**C1. CLONE, the spec's main copy path, fails at the end of every realistic run; the all-database dump has the same problem, delayed.**
- **Where:** `clone.go:107-117` (`restartAfterClone`, then `openAdmin`), `realhost.go:83-89` (readiness `ping` = a full login with the stored account), `conn.go:62-74` (no fallback when the stored account is rejected), `create_replica.go:463-470`.
- **What happens:** CLONE replaces the target's `mysql.user` with the master's accounts. The agent then waits for MySQL to come back by logging in with the replica's *stored* admin account. That account only still works if the identical user and password exist on the master. A target the panel installed itself stores `dbpanel_admin`, which the master never has, so it always fails. The wait times out after 120 s with "CLONE selesai, tetapi MySQL belum siap … 1045" and step 4 fails.
- **Why the existing handling doesn't help:** the orchestrator's "reconnect the replica's admin account, then Lanjutkan" wait is in step 5 and is never reached. "Ulangi" re-runs step 4, which first fails on login and, once the user reconnects, copies everything again with CLONE.
- **The dump variant:** an all-database dump (`dump.go:207-208`, `--all-databases`) loads the master's grant tables without `FLUSH PRIVILEGES`. The job succeeds, but the stored account stops working at the replica's next restart, with no warning at that point.
- **Effect on the user:** the headline flow (bare server, panel installs MySQL, all databases, CLONE) never finishes, and the error text points at MySQL startup rather than at credentials. This was hidden because every clone and dump test, and the E2E test, uses passwordless root on both sides; the test harness's ping also ignores credentials.
- **Fix:**
  1. Readiness after CLONE should accept 1045/1698 as "server is up". This is the T5 deferred minor, which is now load-bearing.
  2. When the stored login is refused after CLONE, return success with a flag such as `AdminReplaced`, so the orchestrator does not re-run the copy. Verify `clone_status` after the user reconnects.
  3. Move the reconnect wait into step 4. Word it for a non-expert: "masukkan akun admin MySQL milik master (akun yang sama dengan Hubungkan MySQL di master) di halaman server <replika>", with a link to that page.
  4. For all-database dumps, run `FLUSH PRIVILEGES` after the load and use the same wait.
  5. Add a driver test where the recipient's stored account does not exist on the donor, and an orchestrator test for the snapshot → reconnect → continue flow.

**C2. "Ulangi" on an old failed job can damage a working replica or a repurposed server.**
- **Where:** `runner.go:391-408` (Retry only checks for *active* jobs), `handlers_jobs.go:182-183` (every failed job shows the "Ulangi dari tahap ini" button), `create_replica.go:72-77` (a new attempt silently replaces the failed attempt's replica row).
- **What can happen:**
  - After a newer attempt succeeded on the same target, retrying the old create_replica job first sets the healthy replica to `creating`.
  - From the snapshot step, the snapshot is refused because replication is running, the job fails, and the healthy replica is marked `failed`. The form then offers it for re-creation, which wipes it.
  - From step 5 or 6, the old job's database list is pushed as the filter onto the working replica.
  - If the target was later made a master (StartPrepareMaster only refuses `RoleReplica`), a retry from the snapshot step skips preflight and runs CLONE over that master.
  - An old add/remove job has the same problem: it re-applies its stale database list as the filter and silently drops databases added since.
- **Status:** the ledger parked this for the final review. It must be fixed.
- **Fix:** Retry and Approve must refuse when:
  - a newer job exists for that server;
  - for create_replica, the target's replica row is missing, belongs to another topology, or is not `failed`/`creating`, or the server's role is no longer unassigned;
  - for change jobs, the replica's current database list differs from the job's snapshot.

  Do the check under `startMu`, which also closes the deferred race. Hide the button for superseded jobs.

**C3. `Siapkan master` stops on problems that its own next step would fix.**
- **Where:** `preflight.go:352` (`server_id` = 0 → fail, while the detail says "akan diatur otomatis"), `preflight.go:368` (`binlog_format` ≠ ROW → fail with a fix attached), `prepare_master.go:76-78` (the job aborts on any fail).
- **The contradiction:** `configure.go:137-144` fixes both issues, and `configure_test.go:138` proves configure handles a MIXED master, but the job aborts before configure runs.
- **Effect:** I believe aaPanel's stock `my.cnf` ships `binlog_format=mixed` (please verify on a current install). If so, preparing a master on aaPanel, a named supported layout, fails at step 1 with no remedy shown. Retrying fails the same way, and the user would have to run SQL by hand.
- **Fix:** downgrade both checks to warn. For `binlog_format`, add a restart reason or a clear note that application connections already open keep the old format until they reconnect. Add an orchestrator or E2E case with a MIXED master.

#### Important (Should Fix)

**I1. The restart approval is a generic button and there is no "I'll restart it myself" path.**
- **Where:** `prepare_master.go:168-174`, `job_detail.html:44-52`.
- **The gaps:**
  - The waiting step's only forward action is "Setujui & lanjutkan", which restarts the *production master* again.
  - There is no "Restart MySQL di master sekarang" label and no "Saya restart sendiri nanti" option (spec §7.3 step 2).
  - Retry on a waiting job is supported in `Runner.Retry` but hidden in the UI, and it would only wait again because `NeedsRestart` is read from stored state.
  - The three dangerous approvals (master restart, CLONE wipe, leftover-database drop) share one unlabelled button and no modal (spec §8).
- **Effect:** after restarting at 2 am as planned, the user's obvious click restarts the hospital master again in daytime.
- **Fix:** when re-checking the restart step, re-run configure or compare the effective values, and skip the restart if it is no longer needed. Add a "Sudah saya restart — periksa lagi" button. Label the approve button per step and put each dangerous approval behind a daisyUI modal with a plain-language impact summary. This is the T12 deferred minor; it should be fixed before merge.

**I2. Configure silently changes the master's binlog base name and retention.**
- **Where:** `configure.go:204-221`, `configure.go:26`.
- **What happens:** `renderConfig` always writes `log_bin=binlog` and `binlog_expire_logs_seconds=604800`, even on a master that already has `log-bin=mysql-bin` (aaPanel) and its own expiry. The change takes effect at the master's next restart, which could be months later.
  - The old `mysql-bin.*` files are stranded on disk.
  - A replica that is not caught up at that moment gets error 1236 and has to be re-created.
  - Retention silently changes: it shrinks from MySQL's 30-day default, or grows where the user had chosen less for disk reasons.
- **Fix:** write `log_bin` only when it is OFF, otherwise keep `@@log_bin_basename`. Write the expiry only when it is 0 or below a documented minimum.

**I3. Replication runs without TLS, contrary to the spec.**
- **Where:** `create_replica.go:476-481`.
- **What happens:** `SSL: false` is hard-coded with a code comment, not a ledger ruling. The spec says "SOURCE_SSL=1 (jika master mendukung)". MySQL 8.0+ generates certificates automatically, so `SOURCE_SSL=1` without server-certificate verification would stop passive sniffing.
- **Effect:** as written, every row change (patient data) and the caching_sha2 exchange cross the LAN in cleartext. CLONE and mysqldump already use TLS when it is available.
- **Fix:** set SSL when the master reports `have_ssl`/TLS. The security audit should note this.

**I4. A replica's root shell is reachable through the dump load.**
- **Where:** `dump.go:213-216`.
- **What happens:** the agent pipes the master's mysqldump output into the `mysql` client running as root. In batch mode that client still runs `\!` shell commands. mysqldump prints `SHOW CREATE` output verbatim, so a compromised master, or a compromised panel pointing `dumpFrom` at an attacker's host, gets root on the replica. This defeats spec §9.9 ("no shell from panel strings") through data rather than parameters.
- **Fix:** add `--binary-mode` to `loadArgs` (in non-interactive mode it disables client commands except `\C` and `\d`) and test it against dumps containing routines and triggers. I am flagging this for the dedicated audit, not auditing it.

**I5. A `lower_case_table_names` mismatch is not detected (T7 deferred minor).**
- **What happens:** MySQL 8 refuses to start when the copied data dictionary's setting differs from the server's own. A CLONE from a master with value 1 onto a panel-installed target with the default 0 most likely leaves the target's MySQL down after the copy, with a confusing "belum siap" error. A dump across a mismatch changes how applications on the replica see table names.
- **Fix:** add the value to MySQLStatus and fail replica preflight on a mismatch with plain instructions. Ideally the installer initialises the target with the master's value.

**I6. Reusing a target can get stuck at "Siapkan replika" (T8 deferred minor, now reachable).**
- **Where:** `configure.go:306-318`, `create_replica.go:344-363`.
- **What happens:** a target left `super_read_only` by a failed dump attempt, then retried with all databases (so the method becomes CLONE), runs `INSTALL PLUGIN clone` under super_read_only. That fails with 1290 on every retry.
- **Fix:** lift read-only around INSTALL PLUGIN the way the dump does, or call setReadOnly(false) before configure on re-attempts.

**I7. Verifying a large replica fails while it is still catching up, and the failure stops replication.**
- **Where:** `create_replica.go:492-505`, `runner.go:61` (`syncTimeoutSec=900`), `runner.go:684-686`.
- **What happens:** after a multi-hour dump on a busy master, catching up can take more than 15 minutes. The step then fails, and `fail()` stops replication, which makes the lag worse. The user sees "Gagal" on a replica that is fine.
- **Fix:** while both threads are running with no errors and progress is being made, keep waiting and report progress. Never stop replication on a "still catching up" failure.

**I8. Rulings that required a user notice are not implemented.**
- **FTWRL ruling:** the create form should say that the master is locked briefly when the dump starts and that this should be done "di jam sepi" (in quiet hours). `replica_new.html` says nothing about it, and `replica_databases.html:55` mentions the lock but not quiet hours.
- **T9 ruling 2:** the add-database page should say that the replica is *writable* during the load. It does not.
- **Why it matters:** on a master under load, a long report query delays the FTWRL and stalls every write, such as patient registration.
- **Fix:** add the notices to both forms.

**I9. The replication source address cannot be edited.**
- **Where:** `runner.go:764-769`, `master_detail.html:18`.
- **What happens:** the source address is the IP from which the agent paired. That IP is often wrong for multi-homed hosts, NAT or VPN setups. Spec §4 says the per-topology source address "bisa diubah terpisah", but there is no UI for it, even though `SetTopologySource` exists. The user hits a failed reachability check with no way to fix it. This is a plan gap.
- **Fix:** add an edit field on the master page that re-runs the reachability check.

**I10. A target whose MySQL is installed but not connected fails late.**
- **What happens:** replica preflight passes through the root/auth_socket fallback, but the dump refuses at step 4 ("akun MySQL belum terhubung"). By then the target has already been restarted and set read-only.
- **Fix:** StartCreateReplica, or the form, should refuse such targets up front with a link to "Hubungkan MySQL".

**I11. The README lacks operator documentation for the new flows.**
- **Missing topics:** connect, prepare master and restart, the firewall note for `bind_address=0.0.0.0`, the replica → master MySQL port, what super_read_only means for the target, and credentials after CLONE.
- **Most critical gap:** backing up `/etc/dbpanel/panel.key`. If it is lost, the sealed replication passwords cannot be opened and there is no rotation path. The §9.13 panel-move procedure must include it.
- **Stale text:** README lines 196 and 206 still say certificate renewal and release signing are planned for "Rencana 2" (Plan 2), but this branch adds neither.

#### Minor (Nice to Have)
- **m1.** File sizes: runner.go 794, install.go 724, replication.go 660, preflight.go 602 lines. Splitting the job lifecycle from the jobRun helpers was already noted in the ledger.
- **m2.** The agent discovers the MySQL layout only once, at startup. MySQL installed outside the panel after pairing (for example via aaPanel) gives "MySQL tidak ditemukan" until the agent restarts. Re-discover lazily, or say "restart agent".
- **m3.** When a dump step with a WipeList is retried, `dropDatabases` runs before the code checks for a dump job from an earlier attempt that is still running (`create_replica.go:419-427` vs `agentjob.go:23-33`). This matters on resume after a panel restart.
- **m4.** StartCreateReplica does not refuse while the master has an active prepare_master job, for example one waiting for a restart.
- **m5.** Spec gaps: §9.5 asks for a copy-duration estimate (missing); §7.3 step 1 asks for a `binlog_row_image` check (missing).
- **m6.** A failed target that the user abandons stays super_read_only forever. Document this, or add a release action later.
- **m7.** No end-to-end test of the CLONE path through the orchestrator with real mysqld. This is how C1 slipped through.

### Triage of the ledger's deferred minors

**Must fix before merge:**
- T5 "readiness could accept 1045": part of C1.
- T12 "old failed job retryable after its row was replaced" and "Retry guard without startMu": C2.
- T12 "no way forward after 'Saya restart sendiri nanti'": I1.
- T7 "lctn mismatch": I5.
- T8 "INSTALL PLUGIN fails on an already super_read_only replica": I6.
- T11 "job cancel SIGKILLs apt/dpkg": a few lines (`cmd.Cancel`=SIGTERM + `WaitDelay`). Without it, cancelling an install can leave a broken dpkg state that a non-expert cannot repair.
- T12 "preflight only warns for a target replicating from another source while step 3 silently resets it": at minimum, put it in the wait message. Removing someone's manual replication silently is the kind of thing the user must be told.

**Fix before release (not blocking merge):**
- T10 "CHANGE SOURCE error redaction relies on MySQL echoing the whole literal": drop the server message for that statement; the audit should confirm.
- T14 "re-adding a database removed with data kept leaves stale tables": add a note on the page.
- T8 "effective check only exercised on 8.0.46 (8.4/9.x variable names)": run on 8.4/9.x before release.
- T11 "release RPM filenames unverified" and Task 11's RPM revisions and mysql-apt-config version: these are release-checklist items and must be checked against the live repositories before tagging.

**May stay:** all other deferred items from T1–T15. They are cosmetic, need unlikely inputs, are covered by later checks, or are test gaps without a known defect. Specifically:
- the T2 shared-sentinel item, which is mostly fixed (`Hub.Call` still returns the shared pointer when offline; harmless);
- the secrets key file symlink/owner/empty-file race (root-only directory; for the audit to note);
- T3, T4 and T6 roles/quoting (inputs are validated);
- T7 disk, reachable, engine and decimal items;
- T9 redaction, stall, busy-error and late-cancel tests;
- T13 SSE gap, history limit, rate limit and Tailwind warning;
- T14 step-0 label, queued Resume and helper duplication;
- T15 test-strength items.

### Declined to judge
- Monitoring layer 1 (heartbeat, Sinkron/Ketinggalan/Terputus statuses, lag chart), Telegram, and alerts when super_read_only is lifted: Plan 3, as the spec and plan say.
- Promote, checksum, Resync, and the Stop/Start buttons on the replica page: later plans.
- Automatic certificate renewal and release signing: not in Plan 2's tasks. I flagged only the stale README text.
- Adopting existing topologies and multi-channel replicas: spec §9.12 non-goal / v1.
- Whether exact-patch install (spec §9.5) works against live repositories: unreachable from here; the release checklist covers it.
- MariaDB, Percona and 5.7 behaviour beyond preflight rejection: out of scope.
- A full OWASP/ASVS assessment, including CSP/CSRF depth, the global SSE stream visible to every logged-in user, and the `dbpanel_repl@'%'` read-everything grant: deferred to the scheduled security audit. I flagged only I3 and I4.
- Correctness of the `mysqld --validate-config`-as-root concern: checked and dropped (it works).
- Whether `SQL_AFTER_GTIDS` works with a multi-threaded applier on 8.4/9.x: covered on 8.0.46 by T10's test; not re-run here.

### Recommendations
1. Run one focused fix round for C1–C3 and I1, I5 and I6, then the ledger items marked must-fix above. Add the two missing tests: CLONE with different accounts on each side, and a MIXED master through prepare_master.
2. Add one E2E case for the CLONE path with a non-root replica account. It would have caught C1.
3. Treat I2, I3 and I4 as production-blocking for any deployment on a hospital network, even if merged earlier.
4. Before tagging a release, write the README operator section (I11), including backing up `panel.key`.

### Assessment
**Ready to merge?** With fixes.

**Reasoning:** The architecture, GTID handling and job lifecycle are well built, and the subset-dump path works end to end. But the spec's headline CLONE path always fails on real credentials (C1), a stale "Ulangi" can damage a healthy replica or a repurposed server (C2), and "Siapkan master" blocks on settings it would fix itself (C3). All three are focused, testable fixes.
