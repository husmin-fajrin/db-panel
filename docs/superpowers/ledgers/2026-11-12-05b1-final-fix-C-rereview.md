# Final fix wave, part C — security re-review (472fd35..0b1c760)

Method: I read the brief, the context, the part-A re-review, the implementer's report and the whole diff file. I ran `graphify query` first. I read code at the head with `git show 0b1c760:<path>`, never from the working tree. I ran no `go test`, `make` or mysqld. One throwaway Go program in the scratchpad (outside the repo) ran a verbatim copy of `globalGrant`, `revokeRow` and `listingProof` on crafted SHOW GRANTS rows. Line numbers below are at 0b1c760.

### Finding Verdicts

**C-1 (HIGH) — the settle must trust only a database listing proved complete** — NOT ADDRESSED.

What the fix does:
- `completeUserDatabases` (rebuild_load.go:209-226) takes one `*sql.Conn`. On it, it reads `CURRENT_USER()`, runs `listingComplete` and lists the databases. The proof and the listing therefore share one connection and one account.
- `listingComplete` (rebuild_load.go:232-265) reads `SHOW GRANTS FOR <quoteAccount(account)>` by name and parses the rows with `listingProof` (rebuild_load.go:274-295). Complete means SHOW DATABASES (or ALL) on `*.*` with no REVOKE row naming it, or SELECT (or ALL) on `*.*` with `@@GLOBAL.partial_revokes`=0.
- Three places use it: the early check in `dumpFrom` (dump.go:122-128, before STOP REPLICA at dump.go:133, liftReadOnly and the reset), `listBefore` (rebuild_load.go:97-107, called at dump.go:151) and `databasesNotCopied` (quarantine.go, via `completeUserDatabases`).
- At the settle, `errListIncomplete` is returned before `pins.settleRebuilt` (quarantine.go:461-468, 481), so the role and the approval are untouched.
- The text matches the brief character for character (rebuild_load.go:198-202).

**The proof can be forged by the attacker's choice of account, role or column name.** `listingProof` reuses `globalGrant` (connect.go:396: `^GRANT (.+?) ON \*\.\* TO .*?…$`). That regex does not limit the privilege list to privilege tokens, so the first literal ` ON *.* TO ` anywhere in a row ends the "privilege list". MySQL user names (32 chars), role names and column names may contain spaces, commas, `*` and `.` (database and table names may not contain `.`, but user, role and column names may). I ran the verbatim functions in the scratchpad:

| Rows given to `listingProof` | show | sel |
|---|---|---|
| `GRANT RELOAD, SUPER ON *.* TO \`u\`@\`localhost\``, `GRANT ALL PRIVILEGES ON \`x\`.* TO \`u\`@\`localhost\`` (scoped account of the scenario) | false | false |
| the same account named `p, SHOW DATABASES ON *.* TO x` (29 chars): `GRANT ALL PRIVILEGES ON \`x\`.* TO \`p, SHOW DATABASES ON *.* TO x\`@\`localhost\`` | **true** | false |
| plain name `u`, plus a column grant `GRANT SELECT (\`q, SHOW DATABASES ON *.* TO z\`) ON \`x\`.\`t\` TO \`u\`@\`localhost\`` | **true** | false |
| plain name `u`, plus a privilege-less role named ``a`,SHOW DATABASES ON *.* TO b`` (29 chars): ``GRANT `a``,SHOW DATABASES ON *.* TO b`@`%` TO `u`@`localhost` `` | **true** | false |

The re-review's attack therefore still works. The panel, compromised while the server was a master, stores the scoped admin account under the name `p, SHOW DATABASES ON *.* TO x`@`localhost`. That store needs no new privilege: the scenario already requires the attacker to create this account. After the promote and root's approval, the panel sends `dumpFrom{databases:["x"]}`:
- the early check passes;
- `before` is empty, because the old databases are invisible to the account;
- the load creates `x`;
- the settle "proves" the listing again and sees only `x`;
- the server becomes a replica, is released and can be opened writable with all its old data.

The role and column variants work with an ordinary account name. The role variant needs CREATE ROLE and ROLE_ADMIN (or the admin account the operator typed in). The column variant needs only GRANT OPTION on `x`.

Check for the controller (real mysqld, not run by me): take `TestRebuildScopedAccountCannotSettle` and name the scoped account `p, SHOW DATABASES ON *.* TO x`. Expected at the head: `dumpFrom{kecil}` passes the check, and the settle ends the quarantine.

Bypass table (attacker controls the panel, or the server is simply misconfigured):

| # | Way the listing could be incomplete, or the check bypassed | Stopped at head? | Evidence |
|---|---|---|---|
| 1 | Crafted user name containing ` ON *.* TO ` and `, SHOW DATABASES` | **No** | connect.go:396 regex; rebuild_load.go:278-282; experiment above |
| 2 | Crafted role name (role row `GRANT \`role\`@\`%\` TO …`) | **No** | same |
| 3 | Crafted column name in a column-privilege row | **No** | same |
| 4 | Account name with `\`, and global `sql_mode` set to NO_BACKSLASH_ESCAPES (the scenario account holds SYSTEM_VARIABLES_ADMIN) | **No** | `quoteAccount`/`quoteString` (connect.go:466-475) always write `\` as `\\`, unlike `sessionQuoter` (replication.go:364). Under NO_BACKSLASH_ESCAPES, `SHOW GRANTS FOR 'a\\b'@'localhost'` names a different account `a\\b`, which the attacker gives SHOW DATABASES ON *.*. `listingProof` never checks that a row's `TO` matches the account. The advice's GRANT names the wrong account in the same way. (Unmeasured.) |
| 5 | `ALL PRIVILEGES` vs `ALL`, and the case of the keywords | Yes | rebuild_load.go:280 (ToUpper), 293 |
| 6 | Several rows (static row plus dynamic row) | Yes | rows aggregated in `held` |
| 7 | `ON \`db\`.*` read as global | Yes for real rows; No for crafted identifiers (#1-3) | regex needs a literal `*.*` |
| 8 | Privilege only through a role (default role, `activate_all_roles_on_login`, `mandatory_roles`) | Yes: read by name, role privileges never appear as the account's rows; roles can only add visibility | rebuild_load.go:233; measured (e) and f_show. Mandatory roles were not measured, but they can only add. |
| 9 | Anonymous account, wildcard host | Yes: `CURRENT_USER()` is the matched account (`''@'localhost'`, `'u'@'%'`) and is quoted and read by that name | rebuild_load.go:216, 233 |
| 10 | Proxy user | Yes, in principle: `CURRENT_USER()` is the proxied account, whose privileges apply (not measured) | — |
| 11 | `@` in the user name | Yes: `LastIndex`; a host containing `@` cannot match a local login | connect.go:467-470 |
| 12 | partial revoke of SELECT | Yes: the SELECT path needs partial_revokes=0, and MySQL refuses to turn it OFF while restrictions exist | rebuild_load.go:257-263 |
| 13 | partial revoke of SHOW DATABASES (d') | Yes: REVOKE row naming SHOW DATABASES or ALL | rebuild_load.go:284-291 |
| 14 | partial_revokes or a REVOKE changed by a concurrent session between the grants read and the listing | No, a race window of milliseconds on one connection. It needs a second privileged session, and every lost try ends in A6 with the approval kept, so it can be retried. | Residual (below) |
| 15 | Proof on one connection, listing on another | Yes: one `*sql.Conn` | rebuild_load.go:210-225 |
| 16 | Load account differs from settle account; account swapped by `mysql.connect` between load and settle | Yes: every listing proves its own account. `before` and the settle are each complete for the account in effect | dump.go:126,151; quarantine.go (databasesNotCopied) |
| 17 | Databases created after `listBefore`, before the settle | Partly: with a named dump, a new name not in `named` is caught. A whole dump, or tables moved (RENAME TABLE) into a named database during the load window, are not | existing A-1 residual, extended below |
| 18 | `information_schema.schemata` vs `SHOW DATABASES`, `skip_show_database` | Probably yes: skip_show_database governs the SHOW statement; schemata uses CAN_ACCESS_DATABASE. Not measured. | controller should measure a global-SELECT account with `--skip-show-database` |
| 19 | lower_case_table_names | Yes: the comparison ignores case, which only adds refusals | rebuild_load.go `notCopied` |
| 20 | System schemas skipped (`mysql`, `sys`, `information_schema`, `performance_schema`) | By design: tables left in `mysql` or `sys` are never compared | snapshot.go:29; residual |
| 21 | Dump without a rebuild mark | Yes: no record, so the settle fails with C-6 | snapshot.go (`mark.done`), quarantine.go:453-458 |
| 22 | Early check in `dumpFrom` before every local change | Yes: earlier steps only read, write temp option files and run the client probes | dump.go:66-128 |
| 23 | Every error path keeps role `quarantined` and the approval | Yes: dumpFrom touches no pins; the settle returns before `pins.settleRebuilt` (quarantine.go:461-481). Afterwards the panel's `stopReplicaAfter` stops the configured replica | create_replica.go:1075 |

Required fix:
- Limit the privilege list to privilege tokens: `^GRANT ([A-Z_ ,]+) ON \*\.\* TO ` (no backtick, quote, parenthesis or `` ` ``), and the same for `revokeRow`.
- Require each row's `TO`/`FROM` account to equal the account being proved.
- Quote the account for the session's sql_mode (`sessionQuoter`), or count an account containing `\` or `'` as not proved.
- Add `listingProof` cases for the crafted user, role and column rows, and a real-mysqld test with a crafted account name.
- The same regex flaw also fools `missingGrants` (out of scope here).

**C-2 — empty source lookup reported as `addr`** — ADDRESSED. rebuild_approval.go:301-305 maps no address and no error to `SourceLookupError{"tidak ada alamat"}`, so `InvalidCode` is `dns`, the same as `RebuildApprovalAddr` (rebuild_approval.go:343). The "no address and no error" subtest checks text and code.

**C-3 — DNS failure worded as "not this server's address"** — ADDRESSED.
- connect.go:236-243: a lookup error gives `errLookupLocal{host, err}`, and an empty answer gives the same type with "tidak ada alamat". Both fail closed.
- The text matches the brief exactly (connect.go `errLookupLocal.Error`).
- An interface-list error stays `errNotLocal`, which is right.
- Tests cover `mysql.connect`, which is refused before login with the stored account kept, and `Open` with a stored name.

**C-4 — dial the checked address** — ADDRESSED.
- `localMySQL` returns `ips[0]` from its single lookup (connect.go:260).
- Every dial site I found uses the returned address:
  - `mysql.connect` dials `dp` with Host set to the checked address and stores the name (connect.go:128-135).
  - `OpenDB` puts the address in the DSN (conn.go:68-85), so every pool re-dial goes there. The guard, commands, settle, `storedLoginRefused` and the monitor (`monitorDB` uses `h.Open`, health.go:110-123) all reach MySQL through `h.Open`.
  - `storedAccount` gives the dump's local client the address (dump.go:296-314, then `clientDefaults` `host=`).
- Other sites stay local: `checkServedCert` (tls.go:681-700) and install's opens use only the socket or 127.0.0.1.
- No TLS host name is verified on local connections.
- Two small gaps are under New Breakage: a dual-stack regression, and `LOCALHOST` in other letter cases.

**C-5 — root opens a path named by the service account** — ADDRESSED for the briefed design. A direct `log_error=/dev/watchdog` is no longer opened, but a race remains (see below).
- readTail (startlock_guard.go:395-418): `Lstat`, open only a regular file through `openLogFile` (same flags), then `Fstat`, which must show a regular file that is `os.SameFile` with the checked one.
- The tests prove with the seam that `/dev/null` is never opened, and that a file swapped before the open is refused.
- Remaining race, stated honestly: `O_NOFOLLOW` covers only the last path component. The service uid controls `log_error` (for example `/var/lib/mysql/d/wd`) and the directory `d`.
  - Between `Lstat` (a regular file `d/wd`) and `open`, it swaps `d` for a symlink to `/dev` (for example with `renameat2(RENAME_EXCHANGE)` in a loop), and names the file `watchdog`.
  - Root's `open` then follows `d` and opens `/dev/watchdog`. That arms the watchdog; the later Fstat/SameFile check refuses the file, but too late.
  - The window is small. The attacker can repeat it on every guard pass that reads the log.
- Closing the race: open with `O_PATH|O_NOFOLLOW` (no device open), `fstat` that descriptor, and only for a regular file reopen it through `/proc/self/fd/N`. Alternatively use `openat2(RESOLVE_NO_SYMLINKS)`, or read the log as the service uid.

**C-6 — configureReplication without a load record answered success** — ADDRESSED.
- quarantine.go:453-458 returns `errNoLoadRecord`, whose text matches the brief exactly (quarantine.go:523-525). It does so after `ConfigureReplication` and `claimSource`, and before any pin change. The role and the approval are kept, and the test checks both.
- No legitimate path is broken:
  - The only sender to a quarantined server is `replicaConnect` (create_replica.go:934).
  - tls.go:659 and :726 run only in TLS jobs, which skip quarantined servers (tls.go:493-530, 787).
  - A retry after a settle finds role `replica` and returns early.
  - CLONE and reattach keep the record within the same agent run.
  - After the failure, `stopReplicaAfter` stops the replica, and a new rebuild resets it (create_replica.go:671).

**C-7 — A6 text** — ADDRESSED. rebuild_load.go:157-166 matches the required text exactly. The test is updated. The README changes only the quotation.

**README** — ADDRESSED.
- The C-1, C-3, C-6 and A6 texts are quoted character for character.
- The NAT/public-address upgrade paragraph is present.
- The advice is true: "Ulangi dari tahap ini" after the GRANT works for both the dump-time refusal and the settle-time refusal, because the record survives in memory. For C-6, it says to start a new rebuild.
- The only inaccuracy is inherited from C-1: the README promises a check that #1-#4 above defeat.

### New Breakage in the Fix Diff

1. **Minor — C-4 availability regression for dual-stack names.** connect.go:260.
   - Scenario: a stored or given host name has both an IPv6 and an IPv4 local address, for example a name in `/etc/hosts` with `::1` and `127.0.0.1`, or an interface name with A and AAAA records. MySQL listens on IPv4 only (Ubuntu's packaged `bind-address=127.0.0.1`).
   - Before the fix, Go's dialer tried each address. Now only `ips[0]` is dialled, which may be IPv6 after RFC 6724 sorting, so `mysql.connect` and every `Open` fail with a connection error.
   - It fails closed, so this is availability only, not safety.
   - Fix: dial the checked addresses in order (try each in `localMySQL`'s caller, or use a registered dial function over the checked list), or prefer the first address that answers.
2. **Minor — C-4: `localhost` in other letter cases is dialled by name.** connect.go:229 accepts `strings.EqualFold(host, "localhost")` and returns the host unchanged. `DSN` (conn.go:39-43) and `clientDefaults` treat only the exact `localhost` as local, so `LOCALHOST` is dialled over TCP by name, with a resolver lookup the check never made (in practice `/etc/hosts`).
   - Fix: return `"localhost"` (or `""`) for any letter case.
3. **Minor — C-1 text in one case.** When the account contains `\` and the session runs NO_BACKSLASH_ESCAPES, the advised `GRANT … TO %s` names a different account (quoteAccount). This is folded into the C-1 fix above.

The C-1 forgery is not counted here as new breakage: it is the C-1 finding still open (Critical under the trust model).

### Residual Risks

- C-1, race (#14): with a second session holding SYSTEM_VARIABLES_ADMIN and CREATE USER, a REVOKE (or partial_revokes turned ON) between the proof and the listing on the agent's connection is not caught. The window is milliseconds and can be retried.
- C-1 / A-1: during the load window, with read-only lifted, an account other than the agent's can RENAME TABLE old data into a database the dump names (or create a database during a whole dump). That data counts as copied, because the check is per database, not per table.
- C-1: tables in the skipped system schemas (`mysql`, `sys`) are never compared.
- C-1: a minimum-grants account (global SELECT, no SHOW DATABASES) on a server with `partial_revokes=ON` is refused until the advised GRANT is run (documented).
- F-10: an account with missing grants stored before the quarantine is still used into it. `missingGrants` is fooled by partial revokes, by active roles and by the crafted-name rows of #1-3.
- C-4: a second MySQL instance on this machine (another port, a container, a local forwarder) still counts as local.
- C-5: the intermediate-directory swap race described above. It needs the MySQL service uid.
- C-6: after an agent restart mid-rebuild, the rebuild must be started again. The job's Ulangi button fails the same way. The README says so, but the agent text "Ulangi bangun ulang dari panel" may be read as that button.

### Out-of-Scope Observations

- `missingGrants` (connect.go:406-463) shares the `globalGrant` regex, so the crafted names of #1-3 can fake any global privilege for the F-10 rule on `mysql.connect` during a quarantine. It should get the same token-only fix.
- `inventory`/`listDatabases`, the panel's leftover list and the preflight, run through the stored account and can show an operator an incomplete leftover list. The settle now refuses in that case, so this is about information, not safety.

### Verdict

**Fix round:** Findings remain open: C-1. The listing proof can be forged by an account, role or column name containing ` ON *.* TO ` (verified against the verbatim parser), and by a name with a backslash under NO_BACKSLASH_ESCAPES, so the re-review's two-masters attack still works. C-2, C-3, C-4, C-5 (as briefed, with a race left), C-6, C-7 and README are addressed. There is no new Critical or Important breakage; three Minor items are listed.
