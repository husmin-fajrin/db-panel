# Final fix wave, part E — report

BASE: 6799dc3 (part D's head; nothing of part D was undone). Commits:
- 991b142 docs(plan): record how names, ANSI_QUOTES and roles show in SHOW GRANTS (E-1 evidence)
- 24e9a30 docs(plan): record how root grants SHOW DATABASES on a read-only server (E-6 evidence)
- 2c9c2ac fix(agent): parse SHOW GRANTS rows by privilege tokens and account (E-1)
- a6059a7 fix(agent): give the listing refusals advice that works on a read-only server (E-6)
- f410582 fix(agent): dial the IPv4 address of a dual-stack local MySQL name (E-2)
- 29b760c fix(agent): treat localhost in any letter case as localhost when dialling (E-3)
- 88e9ce6 fix(agent): resolve the error log path once with O_PATH before reading it (E-4)
- abb56bc fix(agent): refuse leftover and salvage on a quarantined server without a source (E-5)

I made no README change (the controller's instruction 2). What the README needs is under "README needs".

## Measurements
All measurements ran on a scratch mysqld `8.0.46-0ubuntu0.24.04.4`. They are recorded in the plan, in subsection "Bukti daftar database yang lengkap", parts E.

### E-1
| Created | SHOW GRANTS FOR row (default sql_mode) | with ANSI_QUOTES in the session |
|---|---|---|
| user `'p, SHOW DATABASES ON *.* TO x'@'localhost'`, ALL ON x.* | ``GRANT ALL PRIVILEGES ON `x`.* TO `p, SHOW DATABASES ON *.* TO x`@`localhost` `` | `GRANT ALL PRIVILEGES ON "x".* TO "p, SHOW DATABASES ON *.* TO x"@"localhost"` |
| column ``q, SHOW DATABASES ON *.* TO z``, GRANT SELECT (col) ON x.t TO u | ``GRANT SELECT (`q, SHOW DATABASES ON *.* TO z`) ON `x`.`t` TO `u`@`localhost` `` | same, with `"` |
| role ``'a`,SHOW DATABASES ON *.* TO b'`` granted to u | ``GRANT `a``,SHOW DATABASES ON *.* TO b`@`%` TO `u`@`localhost` `` | ``GRANT "a`,SHOW DATABASES ON *.* TO b"@"%" TO "u"@"localhost"`` |
| user `'q"x'`, SELECT ON *.* | ``GRANT SELECT ON *.* TO `q"x`@`localhost` `` | `GRANT SELECT ON *.* TO "q""x"@"localhost"` |
| user sel, SELECT ON *.* | ``GRANT SELECT ON *.* TO `sel`@`localhost` `` | `GRANT SELECT ON *.* TO "sel"@"localhost"` |

| Experiment | Result |
|---|---|
| role r_show (SHOW DATABASES ON *.*) in mandatory_roles, activate_all_roles_on_login=ON; m_user has no direct privileges | CURRENT_ROLE()=r_show. `FOR CURRENT_USER()` shows `GRANT SHOW DATABASES ON *.*` as the account's own. `FOR` name shows only `GRANT USAGE` (a mandatory role prints no row). The listing is complete. |
| same, activate_all_roles_on_login=OFF | CURRENT_ROLE()=NONE. The listing has information_schema and performance_schema only. |
| server started with skip-show-database; account with SELECT ON *.* | information_schema.schemata lists every database. SHOW DATABASES is refused (1227). |
| same server; account with SHOW DATABASES ON *.* | Both list every database. |

No measurement contradicts the rule. The mandatory-role case lists every database, but the proof says not complete. That fails closed, as for any privilege held only through a role.

### E-6
Setup: an instance with binlog and GTID, `SET PERSIST read_only=ON` and `super_read_only=ON`, and an account gu2@localhost with SELECT ON x.*.

| Step | Result |
|---|---|
| before | read_only 1, super_read_only 1, gtid `…:1-2`, persisted ON/ON |
| (i) root runs `GRANT SHOW DATABASES ON *.* TO 'gu'@'localhost'` | `ERROR 1290 (HY000) at line 1: The MySQL server is running with the --super-read-only option so it cannot execute this statement`, exit 1 |
| (ii) one `mysql -S <socket> -uroot` fed the heredoc below | Exit 0. In the middle: read_only 1, super_read_only 0. After: 1 1, gtid unchanged (`…:1-2`), persisted values unchanged (ON/ON). |
| new connection of gu2 after (ii) | SHOW GRANTS has `GRANT SHOW DATABASES ON *.*`; the listing has all 6 schemata |
| a connection of gu opened before the same GRANT | Still lists 3 after the GRANT. A global grant reaches new connections only. |
| (iii) the same heredoc naming a nonexistent account | `ERROR 1410 (42000) at line 1: You are not allowed to create a user with GRANT`, exit 1. The client stops at that error, so super_read_only stays 0. read_only stays 1, gtid is unchanged and the persisted values stay ON/ON, so a MySQL restart restores the flag. The quarantine guard does not set it again without a MySQL restart, so the operator must run `SET GLOBAL super_read_only=ON`. |

The agent's retry needs no agent restart. Every listing proof runs on a fresh pool: in dumpFrom through openAdmin, and at the settle through databasesNotCopied → openAdmin → Host.Open → OpenDB (a new sql.DB). So after the GRANT, "Ulangi dari tahap ini" sees it at both places:
- the copy step, proved by TestRebuildPartialRevokeAccount;
- the configure step, proved by TestRebuildRoleOnlyAccountAtSettle.

Both tests now grant exactly as (ii) does, on a target that is super_read_only. They assert that state before and after, check that gtid is unchanged, and then retry on the same driver.

Command sequence (ii) as run (`<socket>` is the instance's socket):
```
mysql -S <socket> -uroot <<'EOF'
SET GLOBAL super_read_only=OFF; SET SESSION sql_log_bin=OFF; GRANT SHOW DATABASES ON *.* TO 'gu2'@'localhost'; SET GLOBAL super_read_only=ON;
EOF
```

## E-1 — the proof forged by names
**Verified first:** the install account (dbpanel_admin@localhost) and the README example (panel_admin@localhost, the only account in the README's examples) both pass the name rule (TestProofAccount). No stop.

**Built:**
- A shared `grantPrivileges` pattern, `([A-Z0-9_ ,]+)`, in connect.go.
- `globalGrant` is now `^GRANT <privs> ON \*\.\* TO (.+?)( WITH GRANT OPTION)?$`. The account is group 2 and WITH GRANT OPTION is group 3. missingGrants and users.go `grantable` now read group 3.
- The privilege class is upper case only, because MySQL prints privileges in upper case. The old code upper-cased after matching, so a lower-case row would no longer match; MySQL never prints one. `(?s)` is dropped.
- `revokeRow` is now `^REVOKE <privs> ON .+ FROM (\S+)$`.
- `listingProof(grants, user, host)`:
  - A GRANT row counts only when it matches and its account is exactly `` `user`@`host` `` or `"user"@"host"` (ANSI_QUOTES). Other GRANT rows are ignored.
  - A row starting `REVOKE ` that does not parse, or that names another account, makes the listing not proved. This is a choice: the brief allowed ignoring a REVOKE row of another account, but I fail closed, since such a row cannot occur in SHOW GRANTS FOR the account itself.
- `proofAccount` splits CURRENT_USER() at the last `@`. The user must match `^[A-Za-z0-9_.$-]+$` and the host `^[A-Za-z0-9_.%:/-]+$`. Otherwise the error is `errListIncomplete{unsupported}` with the E-1 text (it later got the E-6 ending).
- SHOW GRANTS is built from the checked parts with quoteString. A safe string needs no escaping, so NO_BACKSLASH_ESCAPES cannot change it; the charset closes the re-review's row 4.
- The refusal is the same type everywhere: the early check in dumpFrom, listBefore, and the settle. The role and the approval are kept, with the same log line carrying the cause.

**Tests** (RED before the code):
- Unit tests:
  - TestListingProof gained: crafted user, column and role rows (including ANSI_QUOTES), a REVOKE with a crafted identifier, a REVOKE of another account, a GRANT of another account (ignored), and ANSI_QUOTES rows.
  - TestProofAccount covers the brief's accepted and refused lists, plus more.
  - TestListUnsupportedAccountText and TestMissingGrantsCraftedRow.
  - RED: the new signatures failed to compile. TestMissingGrantsCraftedRow at the old code: "user name/role name/column name: missingGrants = [], want SELECT and RELOAD missing".
- Real mysqld tests:
  - TestRebuildCraftedAccountNameCannotSettle: account `'p, SHOW DATABASES ON *.* TO x'@localhost` with SUPER, RELOAD, PROCESS, REPLICATION CLIENT, SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, ALL ON kecil.* and SELECT ON performance_schema.*.
  - TestRebuildCraftedRoleNameCannotSettle: rs_scoped with role ``'a`,SHOW DATABASES ON *.* TO b'``; refused with the ordinary listing text.
  - RED at the old code: both dumps passed. A throwaway test showed the full attack at the old code: "dumpFrom OK=true; configureReplication OK=true; role pin = replica, rs_lama still there = true".
  - configureReplication's status read needs SELECT ON performance_schema.*. Without it, part C's scoped scenario failed its configure step on that read, not on the proof.
- GREEN: `TestRebuild|TestConnect|TestDump|TestCreateDumpUser|TestPreflight` ok (437 s).

## E-6 — advice that works on a read-only server
The errListIncomplete text and the unsupported-name ending are replaced exactly as given.
- RED: I updated the tests that pin both texts first. TestListIncompleteText, TestListUnsupportedAccountText, TestRebuildPartialRevokeAccount and TestRebuildRoleOnlyAccountAtSettle then failed on the old texts.
- The two real-mysqld tests now grant through `grantAsAdvised`, which feeds the (ii) sequence to the mysql client on the super_read_only target.
- GREEN: ok (54.6 s, together with the crafted and scoped tests).

## E-2 — dual-stack names
localMySQL returns the first IPv4 address among the checked ones, otherwise the first address.
- Test: TestDialPrefersIPv4. The seam answers ::1 then 127.0.0.1, and MySQL listens on 127.0.0.1.
- RED: "login MySQL gagal: dial tcp [::1]:…: socket: address family not supported by protocol".
- GREEN: connect and a stored-name Open both succeed.

## E-3 — LOCALHOST
localMySQL returns "localhost" for any letter case without a lookup; "" stays "".
- Test: TestLocalhostAnyCaseIsLocalhost (uses the new export LocalMySQLDial).
- RED: `"LOCALHOST": dial "LOCALHOST"`. GREEN ok.

## E-4 — intermediate directory swapped for a symlink
**Built** (readTail):
- Open the path with `syscall.Open(path, O_PATH|O_NOFOLLOW|O_CLOEXEC)`.
  - O_PATH is a local constant, 0x200000, because package syscall does not name it. The value is the same on amd64 and arm64, the release arches.
  - golang.org/x/sys is only an indirect dependency, so I did not use it.
- fstat that descriptor. Only a regular file is opened for reading, through `/proc/self/fd/<n>` with O_RDONLY|O_NONBLOCK|O_CLOEXEC; the seam openLogFile is kept for this open.
- The opened file must be regular and `os.SameFile` with the O_PATH fstat.
- Any failure, including a missing /proc, returns an error and takes the existing unreadable path.
- The Lstat is gone: the O_PATH open is the only path resolution.
- It builds clean for linux/arm64 with CGO_ENABLED=0.

**Tests:**
- TestReadTailNeverOpensDevice: the seam is never called for /dev/null; a regular file is read once.
- TestReadTailDirectorySwappedForSymlink: d is a symlink to a directory holding a FIFO, or a symlink points to /dev. Both are refused and nothing is opened for reading.
- TestReadTailSwapBetweenCheckAndRead: the seam swaps d for a symlink to the FIFO directory between the check and the read open, and asserts that what it opens is a regular file.
- RED at the old code: "opened for reading: …/d/error.log (prw-r--r--)". GREEN ok.

**Note:** with the reopen through the descriptor, a swap between the two opens is no longer refused. It is harmless: the checked file itself is read. A swap before the O_PATH open is refused.

**Existing test changed:** TestReadTailRefusesSwappedFile is replaced by TestReadTailSwapBetweenCheckAndRead. A file renamed over the path after the check no longer matters, because the descriptor's file is what gets read. TestReadTailOnlyRegularFiles is unchanged and passes.

## E-5 — unsourced quarantine and leftover/salvage
**Verified first:**
- The refusal text (unsourcedQuarantineText) tells the operator what to do: "Pin master barunya dulu di server ini: sudo dbpanel agent set-source <host:port>". After set-source both commands get past the pins (tested).
- `mysql.salvageRead` in that state reads only files in the agent's own 0700 salvage directory, and only `mysql.salvage` writes there. With this fix no salvage runs without a source pin. A file in that directory therefore comes either from a salvage bounded by a leftover set computed against a pinned source, or from before this upgrade (such files are kept at most 7 days). salvageRead cannot return more than salvage wrote, so I did not refuse it; it must also stay reachable on a rebuilt replica.
- Panel side:
  - oldmaster.go calls `mysql.leftover` (line 187) and `mysql.salvage` (line 212) through callWithin.
  - The refusal is a bad_params `*proto.Error`. It passes oldAgentErr unchanged.
  - jobRun.fail shows "<step title>: <agent text>" after redact.Quotes. The E-5 text holds no quotes, so it reaches the operator whole. No panel change.

**Built:** Pins.Allow refuses `mysql.leftover` and `mysql.salvage` on a quarantined server without a source pin, next to dropDatabases, with unsourcedQuarantineRefusal.

**Test:** TestUnsourcedQuarantineRefusesLeftoverAndSalvage checks the exact text for both commands, and that both get past the pins after SetSource.
- RED: both commands reached the host ("MySQL tidak ditemukan di server ini").
- GREEN: ok (111 s, together with `TestUnsourced|TestQuarantinedWithoutSource|TestLeftover|TestSalvage|TestStrict`).

## Operator-facing strings added or changed
Both are in internal/agent/drivers/mysql/rebuild_load.go (errListIncomplete).
- Listing refusal (E-6, verbatim): `daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent tidak punya hak SHOW DATABASES. Beri hak itu sebagai root MySQL di server ini: GRANT SHOW DATABASES ON *.* TO %s; Server ini baca-saja, jadi perintah itu harus dijalankan dengan langkah di README, bagian "Memberi hak SHOW DATABASES di server yang dikarantina". Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- Unsupported name (E-1 with the E-6 ending, verbatim): `daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat karakter yang tidak didukung pemeriksaan ini. Hubungkan MySQL di panel dengan akun lain yang namanya hanya huruf, angka, garis bawah, titik, atau tanda hubung (misalnya dbpanel_admin). Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`

E-5 reuses the existing unsourced-quarantine text. The log causes are English and are never shown to operators, for example "account name %q not supported by the proof" and "neither SHOW DATABASES nor SELECT granted directly on *.*".

## README needs (not edited by me)
1. In "Persetujuan di server untuk bangun ulang", where part C quoted the listing refusal:
   - Replace that quotation with the new listing text above, character for character.
   - Replace "Jalankan `GRANT` itu, lalu tekan **Ulangi dari tahap ini**" with: the server is read-only, so run the grant with the steps in the new section "Memberi hak SHOW DATABASES di server yang dikarantina", then press **Ulangi dari tahap ini**. This works for the copy step and the configure step, with no agent restart.
2. In the same place:
   - Add the unsupported-name text above.
   - What to do: connect MySQL from the panel (**Hubungkan MySQL**) with an account whose name holds only letters, digits, `_`, `.` and `-` (for example `dbpanel_admin`), then press **Ulangi dari tahap ini**.
   - Also say that the rule reads only privileges granted directly to the account, not through a role (including `mandatory_roles`).
3. A new section "Memberi hak SHOW DATABASES di server yang dikarantina" (the agent text cites this name), with the measured sequence, run as MySQL root on that server:
   ```
   mysql -uroot <<'EOF'
   SET GLOBAL super_read_only=OFF; SET SESSION sql_log_bin=OFF; GRANT SHOW DATABASES ON *.* TO '<user>'@'<host>'; SET GLOBAL super_read_only=ON;
   EOF
   ```
   - I measured it as `mysql -S <socket> -uroot`; any root login that works on that server will do.
   - What it does: read_only stays ON throughout, and super_read_only is off only for that one statement. Nothing is written to the binary log (gtid_executed is unchanged), and the persisted values are unchanged.
   - Warning, measured as (iii): if the GRANT fails (for example a wrong account name), the client stops and super_read_only stays OFF, while read_only stays ON. Then run `mysql -uroot -e "SET GLOBAL super_read_only=ON"` and check that `SELECT @@read_only, @@super_read_only` shows `1 1`.
   - A grant reaches new connections only. The agent opens new ones for each step.
4. The "Mode ketat" list has the bullet "penyelamatan: **Periksa data tertinggal** (`mysql.leftover`), **Amankan data tertinggal** (`mysql.salvage`) …". Add the condition that both need the server's source pin. On a quarantined server without one they are refused with "server ini dipin sebagai quarantined tanpa sumber yang dipin; perintah mysql.leftover ditolak. Pin master barunya dulu di server ini: sudo dbpanel agent set-source <host:port>" (the same with mysql.salvage). Listing, downloading and deleting salvage files still work.
5. Optional: if the README describes dialling a local name, say that the agent dials the first IPv4 address of a local name (otherwise the first address) and treats `localhost` in any letter case as localhost. I found no current README sentence that contradicts this.

## Choices the brief does not fix
- The privilege-list class is upper case only (MySQL's own output), and `(?s)` is dropped.
- A REVOKE row naming another account makes the listing not proved (fail closed) instead of being ignored.
- The account is split at the last `@`; the user charset holds no `@`.
- E-2: "the first IPv4 address among the checked ones" follows the order of the lookup answer.
- E-4: O_PATH is a local constant, and the Lstat is removed.
- E-5: `mysql.salvageRead` is not refused (reasons above).

## Concerns
- **Panel display (not changed, outside my scope).** jobRun.fail passes the agent's text through redact.Quotes. I ran the new listing text through it in a throwaway test in the redact package (deleted afterwards):
  - The account becomes `'…'@'…'` unless it is a panel account (`dbpanel_*`).
  - The README section name and "Ulangi dari tahap ini" also become `'…'`.
  - So on the job page the advice reads "… GRANT SHOW DATABASES ON *.* TO '…'@'…'; … README, bagian '…'. Sesudah itu tekan '…' …".
  - The same censoring hits the README section name quoted in A6.
  - This needs a panel decision, for example letting redact keep the agent's own known quoted phrases and account, or showing the text through AgentQuote.
- **Residuals:**
  - The race between proof and listing on one connection (re-review row #14).
  - RENAME TABLE into a named database during the load window.
  - System schemas are not compared.
  - A weak account stored before the quarantine is still used in it (F-10 order).
  - A second MySQL instance on this machine counts as local.
  - (iii): a failing GRANT in the README sequence leaves super_read_only OFF until fixed by hand or by a MySQL restart.

## Gates (one at a time, on head abb56bc, which includes part D)
1. `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 60m`: exit 0, 2638 s.
2. `go test ./internal/panel/... -race -short -timeout 60m`: exit 0, 143 s.
3. `make test`:
   - First run: exit 2, 2665 s. The only failure was the known flaky TestWSLogsReplacedConnection (agentapi; "replacement not logged at Warn"). It passed when re-run alone.
   - Full re-run: exit 0, 75 s. Go's test cache served the 22 unchanged packages that had just passed; agentapi ran fresh (32 s), and vet and install_test.sh ran again.
4. `make test-e2e`: exit 0, 614 s.

gofmt -l is clean and `go vet ./...` is clean. I ran `graphify update .` after each code commit. No failure came from part D.

## Existing tests changed
- TestListingProof: listingProof's signature changed (it now takes the account), and cases were added.
- TestListIncompleteText: the E-6 text.
- TestRebuildPartialRevokeAccount and TestRebuildRoleOnlyAccountAtSettle: the E-6 text, and the grant now runs as the README sequence does on a super_read_only target.
- TestReadTailNeverOpensDevice: rewritten for the /proc/self/fd reopen.
- TestReadTailRefusesSwappedFile: replaced (reason under E-4).
- export_test.go / seen_cap_export_test.go: added LocalMySQLDial and DefaultOpenLogFile.
