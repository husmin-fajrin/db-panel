# Final fix wave, part C — report

BASE: 472fd35. Commits (in order):
- a1a7305 docs(plan): record what a MySQL account's database listing shows (C-1 evidence)
- f2a8adc fix(agent): settle a rebuild only on a database listing proved complete (C-1)
- 54257b5 fix(agent): point the A6 refusal at an action that exists (C-7, added by the controller)
- eac9e50 fix(agent): word an empty source lookup as DNS in the approval check (C-2)
- e410abd fix(agent): name DNS as the cause when a MySQL host cannot be resolved (C-3)
- 1472e82 fix(agent): dial the MySQL address the locality check accepted (C-4)
- a79b7b6 fix(agent): open MySQL's error log only when it is a regular file (C-5)
- 564b45c fix(agent): fail a configureReplication that leaves the quarantine on (C-6)
- 0b1c760 docs: describe the listing, DNS and missing-copy refusals of a rebuild (README)

## C-1 (HIGH) — the settle trusts a listing that may be incomplete

### Measurements (real mysqld 8.0.46-0ubuntu0.24.04.4, throwaway instance; also in the plan, new subsection "Bukti daftar database yang lengkap (temuan tinjauan ulang A-1)")
Databases old1, old2, x. "listing" = `SELECT schema_name FROM information_schema.schemata` as that account. "FOR CURRENT_USER()" is what `inventory`/`missingGrants` read; "FOR name" = `SHOW GRANTS FOR 'u'@'localhost'` run by the account itself.

| Case | Account | partial_revokes | Listing | SHOW GRANTS FOR CURRENT_USER() | SHOW GRANTS FOR 'u'@'h' |
|---|---|---|---|---|---|
| (a) | SELECT ON *.* | 0 and 1 | all | GRANT SELECT ON *.* | same |
| (b) | SHOW DATABASES ON *.* | 0 | all | GRANT SHOW DATABASES ON *.* | same |
| (c) | RELOAD, SUPER, SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.*; ALL ON x.* | 0 | information_schema, performance_schema, x only | those rows | same (allowed without mysql.* privileges) |
| (d) | SELECT ON *.*, REVOKE SELECT ON old1.* | 1 | all but old1 | GRANT SELECT ON *.* + REVOKE row | same |
| (d') | SHOW DATABASES ON *.*, REVOKE SHOW DATABASES ON old1.* (MySQL accepts it) | 1 | all but old1 | GRANT + REVOKE SHOW DATABASES row | same |
| (e) role active | role r_sel (SELECT ON *.*), default role | 0 | all | **GRANT SELECT ON *.*** (role privilege shown as the account's) + role row | GRANT USAGE ON *.* + role row |
| (e) role not active | same role, no default | 0 | information_schema, performance_schema | GRANT USAGE + role row | same |
| (e) SHOW DATABASES via role, active / not active | r_show | 0 | all / i_s, p_s | **GRANT SHOW DATABASES ON *.*** / USAGE | USAGE + role row (both) |
| f_show | direct SHOW DATABASES + active role carrying REVOKE SHOW DATABASES ON old1.* | 1 | all | GRANT SHOW DATABASES + role row | same |
| k_all | ALL ON *.*, REVOKE ALL ON old2.* | 1 | all (the REVOKE row lists only database-level privileges, not SHOW DATABASES) | | |

After `GRANT SHOW DATABASES ON *.* TO <account>` (the advice): (c), (d), (d') (the REVOKE SHOW DATABASES row disappears), (e) active and not active, (a): all list every database.

missingGrants is fooled: by (d) (REVOKE rows are ignored; `SELECT ON *.*` counts as held) and by (e) with an active default role (role privileges appear as the account's `GRANT … ON *.*` rows). Confirmed with a throwaway unit call: (d) -> no missing grant; (e) active -> none; (e) inactive -> asks GRANT SELECT. Not fixed: the C-1 check does not use missingGrants (it reads the grants by name and its own revoke rows), so this item needs no change there. It remains a residual for the F-10 rule (`mysql.connect` on a quarantined server accepts such accounts).

### Rule built (corrected by the evidence)
The brief's intended rule was wrong in one point: (d') shows SHOW DATABASES itself can be partially revoked. Rule, checked on one `*sql.Conn` right before the listing on that same connection (`completeUserDatabases`, rebuild_load.go):
- account = `CURRENT_USER()`; grants = `SHOW GRANTS FOR '<user>'@'<host>'` (by name, not CURRENT_USER(), so role privileges never count);
- complete if SHOW DATABASES (or ALL [PRIVILEGES]) is granted on `*.*` and no REVOKE row names SHOW DATABASES or ALL; or
- SELECT (or ALL) granted on `*.*` and `@@GLOBAL.partial_revokes` reads 0. Every supported version (8.0.26+, ParseVersion) has the variable, so "no such variable" is not special-cased: any read error is no proof.
- anything else (role only, SHOW GRANTS error, variable unreadable) -> not complete.
Not complete -> `errListIncomplete` with the exact C-1 text (account = quoteAccount(CURRENT_USER())); the cause goes only to the log.
- dumpFrom with a rebuild mark: the check runs once before any local change (before STOP REPLICA / lifting read-only / RESET of binary logs), and again inside listBefore. Choice: the early check is not in the brief; without it a refused dump would already have reset the old master's binary logs.
- settle (`databasesNotCopied`): the C-1 error is returned unwrapped (exact text), role stays quarantined, approval kept; logged as "quarantine kept: the database listing cannot be proved complete".
- If `SELECT CURRENT_USER()` itself fails, the account cannot be named: that error is returned as "baca akun MySQL: …" (before the load) or through the existing "…daftar database di server ini tidak bisa dibaca; karantina tidak diakhiri: …" text (settle). Fails closed either way.

### Verified first (step 4)
- Install path (install.go) creates `dbpanel_admin@localhost` with `GRANT ALL PRIVILEGES ON *.* … WITH GRANT OPTION` -> SHOW DATABASES directly -> passes regardless of partial_revokes.
- README example `GRANT ALL ON *.*` -> passes.
- The README's minimum list (masterGrants: global SELECT, no SHOW DATABASES) passes while partial_revokes is OFF (MySQL default; measured 0). With partial_revokes ON such an account is refused with the advice; the advised GRANT makes it pass. This is the one legitimate configuration that can now be refused; documented in README.
- Part A's full and filtered rebuild tests (root account) keep passing.

### Tests (real mysqld unless noted)
- rebuild_listing_test.go: TestRebuildScopedAccountCannotSettle (the scenario: SUPER, RELOAD, PROCESS, REPLICATION CLIENT, SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN on *.* + ALL ON kecil.*; dumpFrom{kecil} fails with the C-1 text, kecil not loaded, role and approval kept), TestRebuildPartialRevokeAccount (partial_revokes ON, ALL minus SHOW DATABASES, REVOKE ALL ON rs_lama.*: dump refused; after the advised GRANT the dump runs and the settle answers A6 naming rs_lama, i.e. the hidden database is now seen), TestRebuildRoleOnlyAccountAtSettle (copy as root, stored account holds ALL only through a default role: settle refused with the C-1 text, role/approval kept; after the advised GRANT it settles).
- rebuild_load_internal_test.go: TestListingProof (rows incl. install account, scoped, role only, partial revokes of SELECT / SHOW DATABASES / ALL), TestListIncompleteText (exact text).
- RED (before the code; `go test ./internal/agent/drivers/mysql -run 'TestRebuildScopedAccountCannotSettle|TestRebuildPartialRevokeAccount|TestRebuildRoleOnlyAccountAtSettle'`): scoped `dumpFrom{kecil} … OK:true` (the dump went through; the re-review's sequence then settles), partial revoke `OK:true`, role-only `configureReplication … OK:true` (settled). Unit tests: compile failure (listingProof, errListIncomplete undefined).
- GREEN: those plus TestRebuildPartialDumpKeepsQuarantine, TestRebuildFullDumpSettles, TestRebuildFilteredDumpSettles, TestRebuildDumpOverOldDatabaseKeepsQuarantine: ok (75.8 s).
- settleRig gained a `creds` field (test helper only).

### Residual (stated, not fixed)
- An account with missing grants stored before the quarantine (as master, replica or unassigned) is still used into the quarantine; only `mysql.connect` during the quarantine checks grants (F-10 in that order). With C-1 such an account can no longer end a quarantine through an incomplete listing, but it can still lack e.g. SYSTEM_VARIABLES_ADMIN for the guard.
- missingGrants is fooled by partial revokes and by active roles (see above).

## C-7 (added by the controller) — A6 text
New A6 in rebuild_load.go (errNotReplaced), exact. TestRebuildPartialDumpKeepsQuarantine updated. README: only the quotation of A6 changed (no new section, advice around it untouched). RED: with the old code the updated test failed ("…Selamatkan atau hapus database itu lewat panel…"); GREEN ok. No panel test pins A6 (grep).

## C-2 — empty lookup in CheckRebuildApproval
Verified: sourceIPs returns (nil, nil) for an empty answer and checkApproval mapped it to `addr`. Now an empty answer is `SourceLookupError{Err: "tidak ada alamat"}` -> InvalidCode `dns`, as in RebuildApprovalAddr. Test: TestCheckRebuildApproval/"no address and no error" (RED: `… mengarah ke alamat lain … (code "addr")`; GREEN ok).

## C-3 — locality refusal on a DNS failure
localMySQL: lookup error -> `errLookupLocal` with the exact text; empty answer -> same text with "tidak ada alamat". An interface-list error stays errNotLocal. Tests: TestLocalMySQLLookupFailure (both cases), TestConnectRefusesUnresolvedMySQL (real mysqld: mysql.connect refused before login with the text, stored account kept; Open with a stored unresolvable name fails with the text). RED: both returned "… bukan alamat server ini."; GREEN ok. TestLocalMySQL: the "db.tidakada" case moved to the new test (existing test changed: that case's expected text is exactly what C-3 changes).

## C-4 — dial the checked address
Verified: every place that opens MySQL with a stored or given account: mysql.connect (openWith), OpenDB (every Host.Open: guard, monitor, commands; the pool re-dials with its DSN), storedAccount (dump's local mysql client, given host via the --defaults-file `host=` line). install's root opens and checkServedCert use the socket/127.0.0.1 only. TLS: the local DSN has no TLS config and the local client gets clientTLS{} (no ssl-mode/verify), so nothing verifies a host name locally — an IP changes no TLS behaviour. No stop.
Built: localMySQL returns the address to dial: "" / localhost / an IP unchanged; a name -> the first address of its single lookup (all were checked local). connect dials it and stores the account as given (name); OpenDB puts it in the DSN (every pool connection goes there); storedAccount returns it as Host for the client's option file. Socket path unchanged. Choice: the first address of the answer (Go's dialer also starts with the first).
Tests: TestDialGoesToCheckedAddress (seam answers 127.0.0.1 first, 10.20.0.5 afterwards, name unknown to the system resolver; mysql.connect succeeds and stores the name; a stored-name Open succeeds and a second pool connection works), TestDumpLocalClientUsesCheckedAddress (dump with a stored name only the seam knows). RED: "dial tcp: lookup db.berubah.test …: no such host" for connect and Open, the dump at openAdmin. Proof the dump test covers the client: with only storedAccount's substitution undone it fails with "ERROR 2005 … Unknown MySQL server host 'db.lokal.test'". GREEN ok. export_test LocalMySQL became a wrapper returning only the error.

## C-5 — readTail opens only a regular file
Lstat first, open only a regular file (through the seam openLogFile, same flags), then Fstat: regular and os.SameFile with the Lstat result, else "%s bukan berkas biasa" (existing text/path: treated as unreadable by watchBootFailure). Tests: TestReadTailNeverOpensDevice (proved with the open seam: /dev/null refused and the seam never called; a regular file read through the seam), TestReadTailRefusesSwappedFile (seam renames another file over the path before opening: refused); TestReadTailOnlyRegularFiles (FIFO, symlink) still passes. RED: compile failure (seam undefined); behaviourally, with the Lstat/SameFile checks removed: "a device node was opened: [/dev/null]" and the swapped file read. GREEN ok.

## C-6 — configureReplication that silently kept the quarantine
Verified first (panel internal/panel/orchestrator): the only sender of mysql.configureReplication to a quarantined server is create_replica.go replicaConnect (tls.go's two senders run in TLS jobs, which leave quarantined servers alone, Runner.refuseQuarantinedTLS). Paths to it without a load record in the agent's memory:
- reattach (earlierAgentJob): the earlier agent job records on completion in the same agent run -> record present;
- CLONE: the agent survives the MySQL restart -> present;
- AdminReplaced: the job waits for the reconnect; same agent run -> present, unless the agent restarted while waiting;
- retry of replicaConnect after a settle: role is replica, settleRebuilt returns at quarantinedUnder -> unaffected;
- only an agent restart between the copy and the configure reaches the no-record branch, and there the success only made the job look finished while the quarantine could never end. No legitimate path relies on it. Built.
Now: after configuring, the command fails with the exact C-6 text (errNoLoadRecord); role and approval unchanged; Warn log kept.
Test: TestConfigureReplicationEndsQuarantine updated (existing test changed: it asserted success without a load, which is exactly what C-6 changes; now asserts the exact error, role quarantined, approval kept). RED: OK:true; GREEN ok (with TestConfigureReplication|TestStrict|TestQuarantine|TestRebuild|TestSettleRebuilt|TestPin|TestCLI: ok, 225 s).
Note for the panel/UX: Runner.Retry reruns the failed step (replicaConnect), which fails again since the copy is not repeated; the README says to start Bangun ulang sebagai replika again. The text's "Ulangi bangun ulang dari panel" may be read as the job's Ulangi button.

## README (commit 0b1c760; C-7's quotation in 54257b5)
- "Hubungkan MySQL": new paragraph with the C-3 text, the resolve-once note, and the upgrade note (account stored with a NAT/public address fails every access with the locality text, guard included, until MySQL is reconnected with a local host; on a quarantined server with an account holding every checked privilege).
- "Persetujuan di server untuk bangun ulang": the C-1 rule, the C-1 text and what to do (run the GRANT, then Ulangi dari tahap ini), and the C-6 text with what to do (start Bangun ulang sebagai replika again). Texts quoted from the code.

## Operator-facing strings added or changed
Given verbatim:
- C-1 errListIncomplete — internal/agent/drivers/mysql/rebuild_load.go
- C-3 errLookupLocal — internal/agent/drivers/mysql/connect.go ("tidak ada alamat" as the error for an empty answer)
- C-6 errNoLoadRecord — internal/agent/drivers/mysql/quarantine.go
- C-7 new A6: "server ini masih punya database yang tidak disalin dari sumber %s (%s); karantina tidak diakhiri. Ulangi Bangun ulang sebagai replika dari panel. Bila panel lalu menolak karena database itu tidak ada di master baru, lihat README, bagian "Database yang tidak ada di master baru"." — rebuild_load.go (errNotReplaced)
- C-2 reuses the existing SourceLookupError text with "tidak ada alamat" (as RebuildApprovalAddr does).
Not verbatim (mine): README prose around the quoted texts. Agent log (English): "quarantine kept: the database listing cannot be proved complete". Errors when CURRENT_USER() cannot be read: "baca akun MySQL: %w" (existing wording from inventory).

## Choices the brief does not fix
- C-1: rule corrected by evidence (no REVOKE row naming SHOW DATABASES/ALL); grants read by name; early check before local changes in dumpFrom; CURRENT_USER() failure is not named in the C-1 text; any partial_revokes read error is no proof (all supported versions have it).
- C-4: the first address of the single answer is dialled; the account is stored as given.
- C-5: seam openLogFile for the proof.

## Gates (one at a time, on head 0b1c760)
1. `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 60m` — exit 0, 2505 s
2. `go test ./internal/panel/... -race -short -timeout 60m` — exit 0, 90 s
3. `make test` — exit 0, 2551 s
4. `make test-e2e` — exit 0, 616 s
gofmt -l clean, go vet ./... clean, `graphify update .` run after each code commit.

## Existing tests changed
- TestRebuildPartialDumpKeepsQuarantine: expected A6 text (C-7 changes it).
- TestLocalMySQL: the unresolvable-name case moved to TestLocalMySQLLookupFailure with the C-3 text.
- TestConfigureReplicationEndsQuarantine: configure without a load now fails with the C-6 text (it asserted the old success).
- settleRig gained a creds field; export_test LocalMySQL is a wrapper (localMySQL now also returns the dial address).

## Files changed
docs/superpowers/plans/2026-11-12-05b1-mode-ketat-perbaikan-agent.md; README.md; internal/agent/drivers/mysql/{rebuild_load.go, quarantine.go, dump.go, connect.go, conn.go, rebuild_approval.go, startlock_guard.go}; tests: rebuild_listing_test.go (new), rebuild_load_internal_test.go, rebuild_settle_test.go, rebuild_approval_test.go, connect_local_test.go, startlock_guard_test.go, pins_test.go, export_test.go, seen_cap_export_test.go.

## Self-review / concerns
- C-1's rule differs from the brief's intended rule (SHOW DATABASES path also requires no partial revoke of SHOW DATABASES), forced by measurement (d'). The advised GRANT still makes every measured case pass.
- A minimum-grants account (README list, no SHOW DATABASES) on a server with partial_revokes=ON is now refused for a rebuild until the GRANT is run; the README says so.
- C-6: Retry of the failed configure step fails again; the operator must start a new rebuild. The verbatim text's "Ulangi bangun ulang dari panel" could be read as the Ulangi button; README spells it out. Panel wording not touched (out of scope).
- missingGrants is fooled by partial revokes and active roles (measured), left unchanged.
- TDD order: for C-6 and C-7 the test was updated before the code and RED shown on the old code; for C-5 the RED was a compile failure, then behavioural RED by removing the check temporarily; for C-4 the dump test's RED failed at openAdmin, its coverage of the client was shown by undoing only storedAccount's substitution.
