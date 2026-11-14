# Final fix wave, part A — report (in progress)

## A-1 (HIGH) — commit 280ab90
Verified first (panel's legitimate rebuild flow, internal/panel/orchestrator/create_replica.go):
- replicaPreflight: the agent's preflight fails "dedicated" (CheckFail) on any target database the new master does not have (preflight.go "Server replika khusus"), so a legitimate rebuild never reaches the copy with such a database.
- leftoverDatabases = every target database the master has; the user approves that exact list (WipeList), and replicaSnapshot drops ALL of it with mysql.dropDatabases before a dump ("Every confirmed leftover goes, also those not selected now"), also with a filter (jr.st.Databases subset).
- So before a legitimate dump load the target has no non-system database; the rule below does not break the legitimate flow (with or without a filter). Reattach to a running earlier dump skips the drop, but that earlier job recorded its own state.
Rule built:
- "System databases" = `systemDatabases` in snapshot.go (mysql, sys, information_schema, performance_schema), the set dropDatabases/validateFilter use.
- dumpFrom (rebuild mark only) lists the non-system databases right before the load (after read-only is lifted). The successful job records loadCopy{before, named} with the same lifetime as `loaded` (in memory, replaced by the next successful snapshot job). CLONE records whole=true (settles as before).
- settleRebuilt (under promoteMu, 30 s bound) lists the server's non-system databases now; a database that existed before the load, or (named dump) is not among the named ones, is "not copied" -> role stays quarantined, approval kept, error A6 (sorted names, max 10 then ", …").
- Choice not fixed by the brief: "loaded by this rebuild" is taken as "absent before the load began" (stricter than "named in the dump"): a database the dump loaded INTO keeps the old tables the dump did not overwrite, so it does not count as replaced. This also closes "create an empty db of that name on the new master and dump it".
- A dump that could not list before its load fails (no record); listing failure at settle -> not settled, error text (new, not given verbatim): "sumber replikasi sudah diatur, tetapi daftar database di server ini tidak bisa dibaca; karantina tidak diakhiri: %w" (internal/agent/drivers/mysql/quarantine.go).
- The pin-write error wording "sumber replikasi sudah diatur, tetapi pin peran replica gagal ditulis: %w" moved from the handler into settleRebuilt (unchanged text).
- Residual: databases created by something else during the load window (read-only is lifted) count as copied; whole-database granularity (table-level leftovers inside a database the load created cannot exist).
Tests: rebuild_settle_test.go (real mysqld): TestRebuildPartialDumpKeepsQuarantine (auditor's sequence; RED before: settled, OK:true), TestRebuildDumpOverOldDatabaseKeepsQuarantine (RED before), TestRebuildFullDumpSettles, TestRebuildFilteredDumpSettles (passed before and after); rebuild_load_internal_test.go: TestNotReplacedNamesAtMostTen, TestNotCopied. Existing TestQuarantineGuardLeavesRebuildAlone, TestRebuildConfigureAndCloneKeepReadOnly, TestSettleRebuilt* pass. MarkRebuildLoaded (test export) now records a CLONE.
Operator text A6: internal/agent/drivers/mysql/rebuild_load.go (errNotReplaced).

## A-2 (+ F-10) — commit 8ee681e
- F-10 settled with real mysqld (TestConnectWeakAccountOnQuarantinedServer): before the fix, mysql.connect on a quarantined server stored an account with only REPLICATION SLAVE (OK:true), and the next guard round failed with "karantina (SET PERSIST read_only = ON): Error 1227 ... SUPER or SYSTEM_VARIABLES_ADMIN" — read_only and super_read_only stayed OFF. Suspicion confirmed.
- Fix: on a quarantined server (or with unreadable pins — fail closed) mysql.connect refuses an account whose MissingGrants (the grant check mysql.connect already runs: requiredGrants(masterGrants)) is not empty; stored account kept. Text (new; built on the preflight's existing missing-grants wording, not given verbatim): "server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai root lalu periksa ulang: %s" (GRANT statements joined by "; ") — internal/agent/drivers/mysql/connect.go.
- Locality (every role): localMySQL accepts "" / localhost (socket; or 127.0.0.1 with a port, as DSN does), loopback, an address of a local interface (net.InterfaceAddrs), or a name every resolved address of which is one of those (5 s lookup; a name that does not resolve is refused). Text exactly as given (errNotLocal, connect.go). mysql.connect checks it before any login (ParamError).
- Stored account loaded: OpenDB (every Host.Open: guard, monitor, commands) and storedAccount (dump's local client) refuse a stored non-local host with the same text, so the health snapshot's MySQLError and the guard's error carry it. Operator repair: reconnect MySQL from the panel ("Hubungkan MySQL") with a local host; mysql.connect replaces the stored account (on a quarantined server, with a full account).
- strict.go comment corrected.
Tests: connect_local_test.go — TestLocalMySQL, TestConnectRefusesRemoteMySQL (all four roles), TestStoredRemoteAccountFailsClosed (RED with the check disabled: connect reached the dial, "login MySQL gagal: dial tcp 10.20.0.5:3306 ..."), TestConnectWeakAccountOnQuarantinedServer (RED shown above), TestConnectFullAccountOnQuarantinedServer.

## A-3 — commits d680fe7 (docs, evidence), fdb7e2a (code; also F-9 b)
Evidence first (plan section "Bukti kunci start di lingkungan layanan", new table): this machine has no /etc/my.cnf or /etc/mysql and no `mysql` account; measured as `nobody` via setpriv, with `$MYSQL_HOME/my.cnf` standing in for a system option file (same global-file search path; system config not touched). Results: the uid's run reads HOME/.my.cnf and the system files it can read; a system file it cannot read is SKIPPED SILENTLY (exit 0) while the root run still sees it; !include of a root-only file -> errno 13 on stderr, exit 0; !include of a FIFO blocks until the timeout; user= in .my.cnf shows. Consistent with the design; no stop.
Built:
- Host gains ExecEnvAs(ctx, *syscall.Credential, env, ...) (realhost sets SysProcAttr.Credential; ExecEnv = ExecEnvAs(nil)). The home run uses the account's uid/gid and supplementary groups (os/user GroupIds, seam lookupGroupIDs). Root run with HOME=/nonexistent kept. readEffectiveOptions returns both runs; startLockStatus judges each with the unchanged rules (judgeInitFiles); the first that does not hold gives the reason. --defaults-file: one root run as before.
- (b) boundedBuffer: >256 KiB per run -> run cancelled, "keluaran --print-defaults terlalu besar" (reason "konfigurasi efektif MySQL tidak bisa dibaca: keluaran --print-defaults terlalu besar").
- (c) boundStartLock (in startLockStatus, setStartLockState, withStartLock): first 20 operator files (collection itself capped, dedup across runs), each cut by the existing cutInitFile (4096 B), reason cut to 300 B on a rune boundary; proto comments say they may be truncated. Worst case JSON: 20*4096*6 + 300*6 B < 0.5 MiB.
- (d) printDefaultsTimeout 10 s.
- watchBootFailure takes log_error from the service run (the last run).
- Choices: OperatorInitFiles is the union of both runs (root first); a failure to read the account's groups fails the check ("id akun layanan MySQL %s tidak bisa dibaca: %v", new text, startlock.go).
Tests: startlock_runs_test.go — TestPrintDefaultsHomeRunAsServiceAccount, TestStartLockNeedsBothRuns, TestPrintDefaultsOutputBounded, TestStartLockStateBounded (hostile 2500-entry list -> 20 entries, snapshot JSON < 64 KiB), TestPrintDefaultsFIFOBounded (real mysqld, as nobody, round ends at ~10 s). RED shown by temporarily lifting the bounds (Locked:true over 256 KiB; 26 entries; 40047-byte reason). TDD note: these tests were written right after the code for (b)-(d), so RED was demonstrated by temporarily undoing the bound, not before writing it.
Existing test changed: TestServiceAccountMyCnfBreaksStartLock — the agent's line now comes from $MYSQL_HOME/my.cnf (a system-level file both runs read) instead of an !include in ~/.my.cnf, since the root run no longer agreeing would (correctly) say "not locked"; the home is now a directory the service uid can traverse; added the case "~/.my.cnf includes a root-only file" (RED with the home run as root: "init_file lain dibaca sesudah baris agent").

## A-4 + F-5 — commit 7aa72b3
Verified first (F-5): `sudo dbpanel agent set-source <host:port>` (mainSetSource -> Pins.SetSource) has no role check, so it is accepted on a quarantined server; it removes SourceAddrFile (the recorded address) and rewrites the source; it does not touch rebuild-request/approval, and the request's validity (validFor) compares host:port only, so re-pinning the same host:port leaves the code valid. Shown end to end in TestCLIApproveNameChecks ("name moved, then set-source and approve").
Built:
- checkApproval: valid only if the lookup returned at least one address and every one equals the approved address (repeats allowed); otherwise the existing "mengarah ke alamat lain" reason.
- Pins.RebuildApprovalAddr(ctx, lookup) now always resolves a name (IP literal: itself, no lookup), even with a recorded address; returns the single address. >1 distinct -> SourceAddrsError (B16, exact); one address != recorded -> SourceMovedError (B15, exact; resolved addresses joined by ", "). DNS failure keeps its existing text. B16 is checked before B15 (a recorded address among two answers gives B16).
- CLI approve prints B15/B16 to stderr, exit 1, before the prompt, nothing written. The "(nama sumber juga mengarah ke: …)" screen line is gone (a name with other addresses is now refused). New seam cliLookupIP (nil = system resolver).
- Residual (A-4 c, for the controller's audit): the rebuild and the replication link connect by name and re-resolve; TLS verification authenticates the peer when the new master has the panel's certificate.
Tests: TestRebuildApprovalValidity (+mixed answer, no address, same address twice; RED with the old any-address rule: "mixed answer: <nil>"), TestRebuildApprovalAddr (rewritten for the new signature/rules), TestRebuildApprovalAddrDNSNamesHostPort (adapted to the lookup parameter), TestCLIApproveNameChecks (B16, B15 + set-source way out, recorded address resolved).
Existing tests changed: TestRebuildApprovalAddr and TestRebuildApprovalAddrDNSNamesHostPort — signature (lookup parameter) and the old expectations "others" / "recorded address returned without lookup" are exactly what A-4 removes.
README: no quote of the removed line or the new texts found (grep); the README's description of approve belongs to the other fixer.

## A-5 — commit (see git log: "fix(agent): an approval never installs MySQL ...")
Verified first: the panel sends mysql.install only from replicaInstall when replicaCheckTarget's probe says MySQL is not installed (create_replica.go: NeedsInstall = !pr.MySQL.Installed); an old master has MySQL, so no rebuild path sends it. No stop.
- (a) strict.go: mysql.install -> strictRoleRefused; pins.go roleRefuses[quarantined]["mysql.install"]; the install handler now calls d.allow (it never asked the pins before, so a role-refused class alone would have let it through). Refusal is the existing roleRefusal text ("server ini dipin sebagai quarantined; perintah mysql.install ditolak. ...").
- (b) refusedByRole: mysql.dropUser on a quarantined server refused unless the account has the dump prefix (dbpanel_dump_), the same rule as on a master; the panel's rebuild drops only dump accounts (sweepDumpAccounts / snapshot.go dropUser goes to the MASTER).
- (c) approve screen: the given line, right after "…Data yang dibuang tidak bisa dikembalikan." and before the prompt (internal/agent/cli.go).
- README: not touched (brief: the other fixer owns README) — the README's description of what an approval allows needs the A-5 changes (install refused, dropUser limited, new screen line). Handed over.
Tests: TestQuarantinedApprovalLimits (RED: install answered needs_local_approval / passed with approval; dropUser dbpanel_repl passed), TestCLIApprove/approved (RED: line missing). Existing test changed: protectedCases in strict_test.go no longer lists mysql.install (it is role-refused now, not protected).

## A-7 — commit d2d0f2a
- (a) CLI: approvalSyslog via cliSyslog (log/syslog, LOG_AUTH|LOG_NOTICE, tag "dbpanel-agent"; seam replaced in tests). One line after a written approval ("rebuild approval written: source=… addr=… expires=… request_created=… user="…" sudo_uid=… uid=…") and after a successful cancel ("rebuild approval cancelled: …", fields from the approval/request read before clearing, "-" when absent). Times UTC RFC3339; SUDO_USER is %q-quoted (no log injection); uid always. The code is never logged. Failure: stderr warning (new text, not given verbatim): "Peringatan: catatan ke log sistem gagal ditulis (%v)." — outcome unchanged.
- (b) agent: logApprovalUse keeps the once-per-approval line and adds an Info line "protected command allowed by the local rebuild approval" (cmd, source, approved) for every protected command passing the gate under an approval.
- internal/agent/system_test.go: init() makes cliSyslog a no-op so no test writes to the machine's system log.
Tests: TestCLIApproveSyslog (RED: did not compile, cliSyslog undefined), TestApprovedCommandsLogged (RED: "logged 0 times, want twice").

## F-1 — commit 0df4c0d
changes.go: startResultChanged — nil vs non-nil, worst Result, worst StartID, number of kinds, and per kind (ordered late, failed, bypassed) Kind, StartID, Over; ages (AgeSec) and InitFile are not compared. Report goes at the next check.
Tests: TestChangedOnStartResult (RED: appears/goes/worst/kind cases changed=false), TestReportsOnStartJudged (monitor rig: judged start -> report at next check; same state with ages advanced -> none).

## F-2 (b) — commit 87c2d1f
Verified: Pins.Bootstrap with hint "" falls to `default: return nil` (never pins), and never changes an existing pin (settled / role file seen). monitor.Configure now accepts "quarantined" and stores it as "". proto comment updated. The panel half (a) is the other fixer's.
Test: TestConfigureAcceptsQuarantinedHint (RED: "role_hint harus master, replica atau kosong"; GREEN: intervals 30/600 applied, LastRoleHint "").

## F-3 — commit (see git log: "test(agent): keep every test away from /var/lib/dbpanel and /etc/dbpanel")
- mysql.SeenFile, SeenPortsFile, StartResultFile, DefaultSalvageDir and config.DefaultDir are now variables (production values unchanged; panelDirMessage became a var as it concatenates DefaultDir).
- internal/agent/drivers/mysql/testpaths_test.go (package mysql) and internal/agent/system_test.go: init() points them (and agent's cliSeenPath/cliSeenPortsPath, config.DefaultDir) at a per-process os.MkdirTemp dir, removed in TestMain; agent's init also makes cliSyslog a no-op. Guard tests TestDefaultsAwayFromSystemPaths in both packages (RED: before the change the defaults were constants under /var/lib/dbpanel, /etc/dbpanel; the tests could not have been written).
- Drivers built in tests without SeenPath/StartResultPath (found by grep, now all on the temp defaults): configure_test 1, connect_test 2, connections_test 2, health_test 8, install_test 1, pins_test 3, preflight_test 1, promote_files_test 1, promote_test 1, quarantine_test 3, rebuild_settle_test 1, salvage_test 2, snapshot_test 2, start_kinds_test 1, users_test 9; agent: RegisterMySQL (strict_commands_test, mainRun) uses the defaults. guard_send_test and panel e2e_helpers set SeenPath and SalvageDir explicitly (start-result and seen-ports follow SeenPath).
- Other system defaults found:
  * PinDir: no default (empty = no pins) — safe.
  * config.DefaultDir (/etc/dbpanel): CLI flag default; agent tests now see the temp dir. Panel tests that call agent.RunCLI/Main all pass --dir (e2e_replica_test.go:1506, e2e_helpers_test.go:849, localpair_test.go:89).
  * panel.DefaultDataDir /var/lib/dbpanel/panel and DefaultConfigDir /etc/dbpanel/panel: panel code, not touched (not in my brief); no panel test references them by name.
  * FINDING for the panel side (not fixed, internal/panel is not mine): e2e_replica_test.go:1506 runs `agent.RunCLI(set-role unassigned --ya --dir <agent dir>)`; the CLI's cliLayoutHost discovers THIS MACHINE's MySQL layout (not the test instance's) and runs ReleaseStartLock on it: it flocks <dir of the config>/dbpanel-config.lock (on this machine it created /etc/dbpanel-config.lock, present since Nov 13) and, on a machine with a real MySQL whose managed config has the agent's line, would remove that line and the lock file. This is the "touched the machine's real MySQL layout" incident. On this machine (no /etc/my.cnf, no /etc/mysql) it is harmless, so I ran the e2e gate. Also panel e2e_helpers_test.go:849 runs `agent.RunCLI(approve …)` which now writes one auth syslog line (A-7) — the agent's test init cannot reach the panel test binary.
  * Pre-existing traces from earlier runs: /var/lib/dbpanel/replicas-seen ("localhost 2026-11-07…") and /var/lib/dbpanel/salvage/ (empty), /etc/dbpanel-config.lock — left as found.

## F-4 — commit 9cd11b5
rebuildApprovalState reads the role with roleErr; unreadable pins -> ParamError "pin peran/sumber agent tidak bisa dipakai, perintah mysql.rebuildApproval ditolak: %v" (the gate's text family; bad_params). Test TestRebuildApprovalWithUnreadablePins (RED: OK:true with required:false).

## P-2 (a), agent + protocol half — commit (see git log: "feat(agent): say why an approval does not hold …")
- proto.RebuildApprovalResult.InvalidCode `json:"invalid_code,omitempty"`; Invalid kept.
- checkApproval / CheckRebuildApproval return a typed approvalInvalid (code + the unchanged agent text); InvalidCode(err) gives the code (SourceLookupError -> dns). Mapping: role (not quarantined), source (other source), addr (other / mixed / no address), future (>5 min ahead), lifetime (Expires before Approved, or > 24 h), expired, file (missing or unusable file), dns.
- rebuildApprovalState sets Invalid and InvalidCode together; no approval on file -> both empty (and invalid_code absent from JSON).
Tests: TestRebuildApprovalValidity (codes for every case), TestCheckRebuildApproval (file, file, dns), TestRebuildApprovalCommand (source, expired, none -> absent). RED: compile failure (InvalidCode undefined).

## F-6 — commit 896bd6c
releaseStartLock returns *mysql.StartLockFileError{Path, Err} (Error() text unchanged: "berkas kunci start %s tidak bisa dihapus: %v") when the file removal fails; it is reached only after the re-read under the lock confirmed no agent line is left, so "line removed, file not" is exactly this type. cliReleaseStartLock tells it apart with errors.As and prints C16 exactly (%v = the removal error), role unchanged, exit 1.
Test: TestSetRoleMasterHalfDoneRelease (lock path made a non-empty directory; RED: old text "kunci start tidak bisa dilepas (…)"; then removing it and re-running finishes).

## F-7 (CLI part) — NOT DONE: verification failed (stop and report, as the item says)
Checked against the guard for a server whose role is `unassigned` after leaving quarantine:
- GuardTick runs guardQuarantined (which puts read_only/super_read_only back) only when the role pin is quarantined (promote_lock.go GuardTick).
- For any other role guardStartLock (startlock_guard.go) only keeps the agent's init_file line while MySQL does not answer or read-only is not proved persisted (readOnlyProvedNow: both PERSISTed ON, or this run PERSISTed them and both still on); once proved it RELEASES the start lock. It never sets read-only itself.
- mysql.setReadOnly{on:false} is allowed on an unassigned server (refusedByRole: replicaLike = replica|quarantined only), and nothing re-applies read-only after a SET GLOBAL read_only=OFF.
So "Sampai itu agent di server ini menjaga MySQL-nya baca-saja (read-only) selama agent berjalan." would be false: what the agent actually does for unassigned is keep the start lock until read-only is persisted ON (so a restart comes up read-only), then remove it; MySQL stays read-only only as long as nobody (including the panel via mysql.setReadOnly) turns it off. C12 left unchanged, and the "also print C12 with unreadable pins + target unassigned" half was not done either (it would print the current unconditional claim in more cases). Needs a decision on the wording (e.g. a sentence that says the persisted read-only stays until someone turns it off), or an agent change that keeps unassigned-after-quarantine read-only.

## F-9 (b), (c) — (b) in fdb7e2a (effectiveOpts.InitFiles removed with the A-3 rewrite of printDefaults); (c) commit 95a9df2
pins.go header now says what the code does for SA-2: a panel can still get a healthy master quarantined; strict mode means dropping/replacing its data needs root's approval on the server.

## T12-7 — commit 7482857
TestStartJudgedAfterFailedLoadPicksUpFile: an untrusted (0666) start-result file holding a late start fails the load (StartResult nil, warning); after chmod 0644 a judged bypassed start loads the file: worst stays late (its Decided kept), kinds late + bypassed. Passes on the current code (the behaviour existed; the test was missing). Proven to guard: with the old behaviour (publishing the empty state on a failed load) it fails ("file = {Result:bypassed …}", "want the stored late kind kept").

## Operator-facing strings added or changed (file)
Given verbatim by the findings:
- A6 — internal/agent/drivers/mysql/rebuild_load.go (errNotReplaced)
- A-2 locality text — internal/agent/drivers/mysql/connect.go (errNotLocal)
- B15, B16 — internal/agent/drivers/mysql/rebuild_approval.go (SourceMovedError, SourceAddrsError), printed by internal/agent/cli.go
- A-5 approve screen line — internal/agent/cli.go
- C16 — internal/agent/cli.go (cliReleaseStartLock)
- "keluaran --print-defaults terlalu besar" — internal/agent/drivers/mysql/startlock.go
Not given verbatim (my wording):
- "sumber replikasi sudah diatur, tetapi daftar database di server ini tidak bisa dibaca; karantina tidak diakhiri: %w" — quarantine.go (A-1 listing failure)
- "server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai root lalu periksa ulang: %s" — connect.go (A-2/F-10; built on the preflight's existing wording)
- "id akun layanan MySQL %s tidak bisa dibaca: %v" — startlock.go (A-3, groups/ids unreadable)
- "Peringatan: catatan ke log sistem gagal ditulis (%v)." — cli.go (A-7)
- "pin peran/sumber agent tidak bisa dipakai, perintah mysql.rebuildApproval ditolak: %v" — strict.go (F-4; the gate's existing text with this command's name)
Removed: the approve screen line "(nama sumber juga mengarah ke: …)" (A-4: such a name is refused now).
Agent log lines (English, slog): "quarantine kept: databases not copied from the pinned source remain"; "protected command allowed by the local rebuild approval"; syslog lines "rebuild approval written: …" / "rebuild approval cancelled: …".

## Choices the brief does not fix (summary; details per item above)
- A-1: "loaded by this rebuild" = created by the load (absent when the load began), and for a named dump also among the named databases; record kept with `loaded` (memory, replaced by the next successful snapshot job). Listing at settle under promoteMu, 30 s bound. On a quarantined server A-2 guarantees the stored account has SELECT on *.*, so the settle's information_schema listing sees every database.
- A-2: a name that does not resolve is not local; interfaces read via net.InterfaceAddrs; unreadable pins count as quarantined for the grants rule.
- A-3: OperatorInitFiles = union of both runs (root's first), capped at 20 while collecting; entries cut with the existing cutInitFile (4096 B).
- A-4: B16 checked before B15.
- A-7: facility auth, priority notice; SUDO_USER quoted with %q; times UTC RFC3339.
- F-3: per-process temp dir removed in TestMain.

## Gates (one at a time, on head 7482857)
1. `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 60m` — exit 0, 2460 s
2. `go test ./internal/panel/... -race -short -timeout 60m` — exit 0, 148 s
3. `make test` — exit 0, 2527 s
4. `make test-e2e` — exit 0, 628 s
gofmt -l clean, go vet ./... clean, graphify update . run. No panel test or e2e flow needed a setup change.

## Status: DONE_WITH_CONCERNS
- F-7 (CLI part) not done: its verification failed (the guard does not keep an unassigned server read-only); needs a wording decision.
- Panel-side items handed over: e2e_replica_test.go:1506 runs the real CLI release against THIS machine's MySQL layout; e2e approve writes one syslog line; README must reflect A-5 (and A-4's removed screen line).
- TDD order: for A-3 (b)-(d), A-4 and the syslog/T12-7 items the RED was shown by temporarily undoing the change or by compile failure, not always before writing the code.
