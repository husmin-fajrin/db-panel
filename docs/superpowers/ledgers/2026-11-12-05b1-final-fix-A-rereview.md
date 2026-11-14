### Finding Verdicts

Method: I read the diff file `review-8ade0e0..7482857.diff` in passes (A-1 hunks, A-2, A-3/startlock, A-4/approval, strict/pins, CLI, monitor, tests, proto). Code outside the diff was read with `git show 7482857:<path>` after a `graphify query`. No tests were run. Line numbers are at 7482857.

**A-1 (HIGH) — settle only when the copy replaced the data** — NOT ADDRESSED (the auditor's exact sequence is closed; a nearby variant is still open; see New Breakage item 1).
- What the fix does: `dumpFrom` records the non-system databases before the load (`mark.listBefore`, dump.go:139-144). A successful job records `loadCopy{whole, before, named}` (rebuild_load.go `done`; snapshot.go:87). `settleRebuilt` (quarantine.go:443-490) lists the databases under `promoteMu` within a 30 s bound. A database that existed before the load, or that the dump did not name, keeps the quarantine and the approval and returns A6 (`errNotReplaced`). A6 matches the required text character for character, including the cut to 10 names followed by ", …".
- The auditor's sequence fails: `dumpFrom{small_db}` lists `before`=[every old db], then `configureReplication` returns A6. The role stays quarantined and the approval stays. `TestRebuildPartialDumpKeepsQuarantine` checks this and would fail without the fix.
- Variants that hold:
  - A dump of everything, or of a named database the old master still has: the old database is in `before`, so the server is not settled (`TestRebuildDumpOverOldDatabaseKeepsQuarantine`).
  - An agent restart between the load and the configure: the in-memory record is lost, so the server is not settled (fails closed).
  - A MySQL restart mid-load: the job fails and nothing is recorded.
  - A listing failure at settle: an error, not settled.
  - `mysql.connect` between the load and the settle on a quarantined server: since A-2 that needs `masterGrants`, which include global SELECT, so the listing stays complete.
- Legitimate flows, walked against panel `create_replica.go` at 7482857:
  - Preflight: `replicaPreflight` fails "dedicated" on target databases the master lacks. `leftoverDatabases` puts every other target database in WipeList, and `replicaSnapshot:729-740` drops all of it before any dump, with or without a filter. So `before`=[] and both the full and the filtered rebuild settle (tests exist).
  - Retry after a failed load: `preflightBeforeDrop` (create_replica.go:780-800) runs the preflight again on every new run. The partial databases, which are master databases, come back as leftovers, are approved and dropped, so `before`=[] again.
  - Reattach: the earlier agent job keeps its own record.
  - AdminReplaced: the reconnect on a quarantined server needs a full account, so the settle listing works.
- Open variant: the listing in `userDatabases` (`SELECT schema_name FROM information_schema.schemata`, run through the stored admin account) shows only the databases that account has privileges on. Global SELECT or SHOW DATABASES would show all of them, but nothing checks that the account has either. A-2's grant rule applies only to `mysql.connect` on a quarantined server, and on a master `mysql.connect` stores an account even with missing grants (connect.go:142-149). So an account the compromised panel stored while the server was still a master can carry global admin privileges and data privileges on one database only, and the attack then succeeds. Details are in New Breakage item 1.

**A-2 + F-10 — `mysql.connect` only to a local MySQL; no weak account on a quarantined server** — ADDRESSED.
- `localMySQL` (connect.go:206-235) accepts "" or localhost, loopback, an interface address, or a name all of whose addresses are one of those. Anything else, an unresolved name, or an interface-list error is refused. It runs before any login (connect.go:124-129).
- The stored account is checked on every `Host.Open` (`OpenDB`, conn.go:74-78) and in `storedAccount` (dump.go:297-299), so a stored remote host fails closed with the text. The text is exact. `realhost.Open` goes through `OpenDB`. The only other `openWith` callers are install's root-over-socket opens, which are local.
- F-10 was settled with real mysqld. `TestConnectWeakAccountOnQuarantinedServer` would fail without the fix: the weak account was stored and the guard got 1227. On a quarantined server, or with unreadable pins (fail closed), an account with any MissingGrants is refused and the stored account is kept (connect.go:139-149).
- Attack walked: `mysql.connect{host:M}` on every role is refused. A remote host stored by an older agent fails closed on load. The strict.go comment is corrected.
- Residuals are listed below: re-resolution between the check and the dial, a second local instance, and a weak account stored before the quarantine.

**A-3 — `--print-defaults` with the service account's HOME runs as that account; bounds** — ADDRESSED.
- The evidence was committed first and on its own (d680fe7) and is consistent with the design.
- (a) The home run uses `ExecEnvAs` with uid, gid and supplementary groups (startlock.go `accountCredential`; realhost.go sets `SysProcAttr.Credential`). The root run with HOME=/nonexistent is kept. `checkStartLock` judges each run with the unchanged rules (`judgeInitFiles`); the first run that does not hold gives the reason. With `--defaults-file` there is one root run.
- (b) `boundedBuffer` caps output at 256 KiB. Past that it cancels the run and fails it with "keluaran --print-defaults terlalu besar". The `over` flag is read only after `Run` has waited for the copy goroutine, so the read is safe.
- (c) The list keeps 20 entries, deduplicated across runs. Each entry goes through `cutInitFile` (4096 B) and the reason is cut to 300 B on a rune boundary. This applies in `startLockStatus`, `setStartLockState` and `withStartLock`. The worst-case JSON is under 0.5 MiB, and the proto comments say the values may be truncated.
- (d) The timeout is 10 s.
- Attack walked: a FIFO `!include` in ~/.my.cnf is now opened as the service uid and blocks for at most 10 s (`TestPrintDefaultsFIFOBounded`, real mysqld). `/dev/watchdog` cannot be opened by that uid. 5,000 lines are capped. The 460 MB output is cut at 256 KiB.
- The `TestServiceAccountMyCnfBreaksStartLock` rewrite still covers "an init-file in ~/.my.cnf breaks the lock" and adds the root-only include case.
- A related root-open is still left; see Out-of-Scope.

**A-4 + F-5 — validity needs every resolved address to be the approved one; approve always resolves; B15/B16** — ADDRESSED.
- `checkApproval` (rebuild_approval.go:142-158) is valid only when the answer is non-empty and every IP equals the approved address. A mixed answer or no address is invalid (`TestRebuildApprovalValidity` "mixed answer" would fail without the fix).
- `RebuildApprovalAddr` always resolves unless the host is an IP literal. It refuses more than one distinct address with B16, and a single address that differs from the recorded one with B15. A DNS failure keeps its existing text. B15 and B16 are exact.
- The CLI prints them before the prompt, exits 1 and writes nothing (cli.go approve switch). The removed line "(nama sumber juga mengarah ke …)" is correct to drop.
- The F-5 precondition holds: `set-source` has no role check, removes `source-addr`, and the request's `validFor` compares host:port only. This is proven end to end in `TestCLIApproveNameChecks`.

**A-5 — install refused on a quarantined server; dropUser limited; approve-screen line** — ADDRESSED for the agent side.
- (a) `mysql.install` is `strictRoleRefused`, `roleRefuses[quarantined]["mysql.install"]` is set, and the handler now calls `d.allow` (install.go:137). It is refused both with and without an approval (`TestQuarantinedApprovalLimits`). No panel rebuild path sends install to an old master (NeedsInstall is set only when MySQL is absent).
- (b) `refusedByRole` limits `dropUser` on master and quarantined servers to `dbpanel_dump_*` (pins.go:893-896).
- (c) The screen line is exact and placed after "…tidak bisa dikembalikan." and before the prompt. The test checks the order.
- The README update was handed to fixer B.

**A-7 — syslog for approve/cancel; Info per approved command** — ADDRESSED.
- `cliSyslog` uses LOG_AUTH|NOTICE with tag dbpanel-agent. It writes one line after a written approval and one after a cancel, with action, source, address, expiry, request time and user (`%q`-quoted SUDO_USER, SUDO_UID, uid). The code is never logged. A failure to log is a stderr warning and the outcome stands.
- `logApprovalUse` keeps the first-use line and adds an Info line for every protected command that passes the gate. `TestApprovedCommandsLogged` would fail without the fix.
- The test init makes `cliSyslog` a no-op in agent tests.

**F-1 — the monitor's `changed()` counts a start-state change** — ADDRESSED.
- `startResultChanged` (changes.go) compares nil against non-nil, Result, StartID, the number of kinds, and each kind's Kind, StartID and Over. Ages are not compared.
- `TestChangedOnStartResult` and `TestReportsOnStartJudged` cover a report at the next check and none on age-only changes.

**F-2 (b) — the agent accepts `role_hint` "quarantined" as ""** — ADDRESSED.
- monitor.go:247-250. This is safe because `Bootstrap("")` never pins anything (checked against the fixer's claim). The test shows the 30/600 intervals applied.

**F-3 — no test touches /var/lib/dbpanel or /etc/dbpanel** — ADDRESSED for the agent packages.
- SeenFile, SeenPortsFile, StartResultFile, DefaultSalvageDir and config.DefaultDir are now variables. The test `init()` of package mysql and of package agent point them at a per-process temp directory, and guard tests check this. Every agent set-role test stubs `cliLayoutHost` (checked by scanning cli_test.go).
- `/var/lib/dbpanel` was last modified at 03:25:46, before the F-3 commit (03:37). The gates that ran afterwards (about 96 minutes) left it untouched.
- The panel-side case the fixer reports is real and still open (fixer B's): `internal/panel/e2e_replica_test.go:1506` runs `set-role unassigned` through the real `cliLayoutHost`, that is, against this machine's MySQL layout. `/etc/dbpanel-config.lock` exists, dated Nov 13.

**F-4 — `mysql.rebuildApproval` with unreadable pins** — ADDRESSED.
- strict.go `rebuildApprovalState` uses `roleErr` and answers a ParamError in the gate's text family. `TestRebuildApprovalWithUnreadablePins` would fail without the fix.

**F-6 — half-done start-lock release** — ADDRESSED.
- `releaseStartLock` returns `*StartLockFileError` only on the file-removal path, which runs after the re-read confirmed no agent line is left (startlock_guard.go:96-103). The CLI tells the case apart with `errors.As` and prints C16 exactly; the role is unchanged and the exit code is 1.
- The test uses a non-empty directory as the lock path, so the old text would have failed it, and it shows that re-running the command finishes the release.

**F-9 (b), (c)** — ADDRESSED. `effectiveOpts.InitFiles` is removed. The pins.go header now describes what strict mode does, with no planning language.

**P-2 (a), agent and protocol half — `InvalidCode`** — ADDRESSED.
- `RebuildApprovalResult.InvalidCode` is `json:"invalid_code,omitempty"`, and `Invalid` is kept. The code comes from a typed `approvalInvalid` produced by the check that failed, not from the text: role, source, addr, future, lifetime, expired, file, and dns (from `SourceLookupError`).
- With no approval on file both fields are empty and the key is absent from the JSON (tested).
- One small inconsistency is listed under New Breakage (Minor).

**T12-7 — test for a judged start after a failed load** — ADDRESSED.
- `TestStartJudgedAfterFailedLoadPicksUpFile`: an untrusted file makes the load fail, the file is then made trusted, and a judged start keeps `late` as worst plus both kinds. It passes on the current code; the fixer showed it fails against the old "publish the empty state" behaviour, and the assertions on the kept `late` kind would catch that.

**F-7 (CLI part) — no verdict.** The fixer's reason for stopping is true. `GuardTick` (promote_lock.go:567-571) runs `guardQuarantined` only for role quarantined. For any other role `guardStartLock` only keeps or releases the init_file line. `mysql.setReadOnly{on:false}` is refused only where `replicaLike` holds (replica or quarantined, pins.go:140), so it is allowed on an unassigned server. The proposed C12 sentence ("agent … menjaga MySQL-nya baca-saja selama agent berjalan") would therefore be false.

### New Breakage in the Fix Diff

1. **Important — the A-1 rule trusts a database listing the stored account may be unable to complete.** Locations: rebuild_load.go `userDatabases` (used by `listBefore` and by `databasesNotCopied`, quarantine.go:493-510); connect.go:142-149.
   - Why it is open: `information_schema.schemata` lists only the databases the account has some privilege on, unless it has global SELECT or SHOW DATABASES. Nothing checks the listing account. The A-2 grant rule applies only to `mysql.connect` while the server is quarantined, and on a master an account with missing grants is stored as before.
   - Scenario: a compromised panel, while the server is still a master, connects an account with SUPER (or SYSTEM_VARIABLES_ADMIN), RELOAD and REPLICATION_SLAVE_ADMIN, plus data privileges on `x`.* only (no global SELECT). Because it has SUPER the guard still works, so F-10 does not show.
   - After the promote, root approves a normal rebuild. The panel sends `dumpFrom{databases:["x"]}`: `before`=[] because no old database is visible. The load creates `x` and the listing at settle sees only `x`.
   - `configureReplication` then settles the server as a replica. `stopReplica`, `releaseRole`, `resetReplica` and `setReadOnly{off}` follow, and the old master is writable with all its pre-promotion data. That is the A-1 outcome: two masters.
   - Cost: the HIGH finding stays exploitable by a panel compromised before the promotion, which the trust model covers ("the panel may be compromised").
   - Fix (fail closed): before relying on the list, both `listBefore` and the settle check that the listing account holds global SELECT or SHOW DATABASES (for example by running `missingGrants` for those two on `SHOW GRANTS`). If it does not, do not settle and give an error naming the missing privilege. A more robust option is to list the schema directories of the data directory as root. Add a real-mysqld test with such a DB-scoped admin account. Also note in the residual list that a weak account stored before the quarantine is still used into the quarantine (F-10 for that order).
2. **Minor — `InvalidCode` for an empty lookup answer.** In `CheckRebuildApproval`, a lookup that returns no addresses and no error goes to `checkApproval` and gets `addr` ("mengarah ke alamat lain"). `RebuildApprovalAddr` treats the same answer as DNS ("tidak ada alamat"), so the panel will word one condition two ways. Map an empty answer to `dns`.
3. **Minor — the locality text on a DNS failure.** `localMySQL` returns errNotLocal when a stored name does not resolve (connect.go:213-219). During a DNS outage, every `Open` for an account stored with a host name fails with "… bukan alamat server ini.", which sends the operator to reconnect when the real cause is DNS. It fails closed, which is correct; only the wording misleads.

### Residual Risks
- A-2: the check resolves the name, and then `openWith` dials it by name and resolves again, so a DNS answer can change between the two (TOCTOU). Storing or dialling the checked IP would close this.
- A-2: a second MySQL instance on the same machine (another port, a container that publishes a local port, a local forwarder) counts as "local".
- A-2: an address of this server that is not on one of its interfaces (NAT or public IP) is now refused. After the upgrade such a stored account makes every MySQL access fail, including the guard on a quarantined server, until the operator reconnects from the panel with a local host and, on a quarantined server, a full account.
- F-10: an account with missing grants stored before the quarantine (while the server was master, replica or unassigned) is kept into the quarantine. Only `mysql.connect` during the quarantine checks grants.
- A-1: the check is per database. A database created by anything else while the load window has read-only lifted counts as copied.
- A-4 (c): the rebuild and the replication link connect by name and resolve again; the peer is authenticated only by TLS verification when the new master has the panel's certificate. A dual-stack source name (A + AAAA) is refused with B16 and needs an /etc/hosts entry.
- A-3: the root run still reads the system option files as root, which is acceptable because those files are root-owned. The service account's run reads its own files with its own rights.
- A-7: the syslog record is local to the server and is lost if no syslog socket exists (a warning only); the approval file itself is still deleted at settle.
- F-3: the panel e2e test at `internal/panel/e2e_replica_test.go:1506` runs the real CLI start-lock release against this machine's MySQL layout, and the panel e2e approve writes one auth syslog line. Both are open with fixer B.

### Out-of-Scope Observations
- `watchBootFailure` (startlock_guard.go:329-344; the line changed by this diff, behaviour as before) takes `log_error` from the service account's run, which honours ~/.my.cnf. `readTail` then opens that path as root. `O_NOFOLLOW|O_NONBLOCK` stops symlinks and FIFOs but not a device node: `log_error=/dev/watchdog` makes root open the watchdog, and an open without the magic close arms it. Suggested fix: `Lstat` and refuse anything that is not a regular file before opening, or read the log as the service uid.
- `settleRebuilt` (quarantine.go:450-456, behaviour as before): after an agent restart between the load and `configureReplication`, the command succeeds while the server stays quarantined, with only a Warn log. The panel job looks finished.
- Making `config.DefaultDir` a variable turned `panelDirMessage` into a package variable that is fixed at package init. It is harmless in production.

### Verdict
**Fix round:** Findings remain open: A-1. The auditor's sequence is closed, but the settle listing can be incomplete through a pre-quarantine stored account (New Breakage item 1, Important). Every other item this fixer owned is ADDRESSED. F-7's CLI part has no verdict; the fixer's reason for stopping was confirmed true.
