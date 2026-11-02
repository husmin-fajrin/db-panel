# Final fix wave re-review: Plan 3 (dbpanel)

- **Range:** 05a88f7..db6af8c (7 commits, 55 files, +1762/−159).
- **Reviewer:** Opus 5.5, read-only. I did not run any tests or git commands beyond reading the review package diff.
- **How I read it:** The diff (`review-05a88f7..db6af8c.diff`, 5218 lines) was read once, in five passes:
  1. README;
  2. the agent driver;
  3. the panel monitor;
  4. notify and the orchestrator;
  5. store, web and templates.
- **Inputs:** final-fix-brief.md, final-review.md, final-fix-report.md, and audit SEC-42/43/44/46/47/48/51 and §7.
- **Unchanged code:** I read it only where the graph (`graphify query`) located the lines a judgement depended on:
  - `liftReadOnly` (dump.go:372);
  - `restartReasons` (configure.go:222);
  - the agent's `monitor.snapshot` handler and its forced-check test;
  - `agentOffline`, `staleFactor` and `configureTimeout`;
  - `validBaseURL`;
  - `notify.LoadConfig`;
  - `AddReplica` in StartCreateReplica;
  - `replGrants`;
  - `formatTime`.

Line numbers below are at db6af8c (the working tree).

## Findings

| Item | Verdict | Evidence |
|---|---|---|
| A (F1) CLONE into a locked replica | ADDRESSED | See below. |
| B (F2 + I1) judging on stale reports | ADDRESSED | See below. Trade-offs are in New Issues M2. |
| C (I2) SET PERSIST unlock | ADDRESSED | See below. |
| D (I3, spec 5.5) set-source guidance | ADDRESSED | See below. |
| E Telegram errors in Indonesian | ADDRESSED | See below. |
| F resync restart | ADDRESSED | See below. |
| G "belum dihitung" | ADDRESSED | See below. |
| H base URL placeholder-only | ADDRESSED | See below. Side effect in New Issues M4. |
| I proto comment | ADDRESSED | See below. |
| J (SEC-42, SEC-51a) pinned master | ADDRESSED as briefed; PoC only partly closed | See below and the Security section. |
| K (SEC-44) audit limits | ADDRESSED | See below. |
| L (SEC-47) settings warning and audited values | ADDRESSED | See below. Side effect in New Issues M3. |
| M (SEC-46) flap control | ADDRESSED | See below. Edge cases in New Issues M6 and M7. |
| N (docs) | ADDRESSED | See below. |
| O (M1, M7, M9) | ADDRESSED | See below. |

### A (F1): CLONE into a locked replica

- **Lift:** `clone.go:74` calls `liftReadOnly` after the plugin is installed and the donor list is set, before the CLONE session.
- **Restore on early failure:** a deferred `restore()` runs unless `dataReplaced` is set, and `dataReplaced` is set only at `clone.go:128`, after `waitClone` returned nil or 3707 (restart). So it runs on every path before the data phase:
  - a `db.Conn` failure;
  - the session reads;
  - `waitClone` errors, including cancel and KILL;
  - a CLONE error that is not a restart.
- **Cancellation:** `restore` uses `context.WithoutCancel` (dump.go:378), so a cancelled job still locks the replica again.
- **After a successful CLONE:** the restart applies the persisted and my.cnf lock. On top of that, `create_replica.go:612-618` calls `mysql.setReadOnly{on:true}` before "Sambungkan replikasi" on the non-AdminReplaced path. The AdminReplaced path already relocked in `verifyCopyAfterReconnect`.
- **Agent test:** `TestCloneIntoLockedReplicaWithData` (snapshot_test.go) covers a recipient holding schema `a` under SET PERSIST. An unreachable donor fails with "CLONE gagal" and "Paksa dump" and leaves the lock ON. A real CLONE replaces `a.old` and ends with the lock ON.
- **E2E:** `e2eCloneResyncFixed = true` (e2e_helpers_test.go:42).

### B (F2 + I1): judging on stale reports

- **Hold:** `state.go:218` detects a change of state or paused on an already-resolved replica. `:226` sets `awaitFresh` and `:228` starts the background `monitor.snapshot`. That call is not rate-limited (monitor.go:154). The agent's handler forces a new check with `TakenAgoMS=0` (`TestSnapshotCommandForcesCheck`).
- **Keeping the previous status:** `state.go:262-271` keeps it until a report taken at or after the change arrives, or until `freshBound()` (`state.go:242`) passes; then the normal rules apply.
- **Hook from the orchestrator:** `Monitor.ReplicaChanged` (monitor.go:173) is called from:
  - `setReplicaState` (create_replica.go:821);
  - `replicaVerify`'s unpause (create_replica.go:745);
  - the resync start (resync.go:76);
  - Resume;
  - Start (controls.go:49) and Stop (controls.go:28).
- **Stop ordering:** `StopReplica` now writes `paused=true` before `mysql.stopReplica` (controls.go:25) and reverts it if the stop fails.
- **Monitor tests (fresh_test.go):**
  - mid-job report → ready: no `replica_stopped`, no `replica_unlocked`, no lock event, then Sinkron;
  - Start from Dihentikan, including a stale report that arrives late;
  - fallback once the bound expires.
- **E2E:** `e2eResyncQuietFixed = true`. A new strict step (c2) runs Stop→Start through the panel with no stop message (e2e_replica_test.go:694). Check (e) is strict.

### C (I2): SET PERSIST unlock

All four places use `SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF`, each with the "tetap terbuka walau MySQL di-restart" wording:
- `_status.html:52`;
- `replica_detail.html:193`;
- `handlers_mysql.go:93-94`;
- README:545-550.

Tests: `TestMasterPageDownShowsManualGuidance` and `TestDetachModes`. The second also checks that "SET GLOBAL super_read_only" is absent.

### D (I3, spec 5.5): set-source guidance

- `handlers_mysql.go:283,344-356` lists every replica right after a save (`?pesan=sumber`).
- Otherwise it lists each replica whose last reported `Source_Host:Port` differs from the topology address.
- `master_detail.html:47-58` shows `sudo dbpanel agent set-source <host:port>` per replica and says that the running replication keeps the old address until Resync ulang.
- `net.JoinHostPort` and `set-source`'s `SplitHostPort` (pins.go:269) agree for IPv6.
- Test: `TestMasterPageSetSourceGuidance`.

### E: Telegram errors in Indonesian

- `telegram.go:82` prefixes "Telegram tidak terjangkau: " and `:92` prefixes "Telegram menolak: HTTP <code>".
- Both feed telegram_error, the SendTest flash and the Peringatan page.
- Tests cover all three, and the token stays absent.

### F: resync restart

- The comment in `replicaPrepare` is corrected.
- The modal sentence is at `replica_detail.html:166`, asserted in `web_controls_test`.

### G: "belum dihitung"

- `status.go:165` sets `GTIDGapKnown` only after the replication row is reached and the master's set is known.
- `replica_detail.html:30` shows "belum dihitung" otherwise.
- Tests: monitor and web.

### H: base URL placeholder-only

The pre-fill is removed from `renderStoredSettings`. Clearing the field persists, and the test checks it. See New Issues M4 for the side effect.

### I: proto comment

`proto/mysql.go:98` is corrected.

### J (SEC-42, SEC-51a): pinned master

What is implemented:
- `refusedByRole` (pins.go:543-556):
  - `createDumpUser` only `^dbpanel_dump_[0-9a-f]{8}$` from a literal IP (`literalIP`, pins.go:563, which excludes `%` and `_`);
  - `createReplUser` only `dbpanel_repl`.
- `createDumpUser` now goes through `d.allow` (users.go:77).
- `createAccount` refuses an existing account on a pinned master and never ALTERs it (users.go:224). The create paths pass `RoleIs(master)` (users.go:58).
- Restart is granted by `GrantRestart(res.NeedsRestart)` after every successful configure (configure.go:66). `AllowRestart` uses the grant once and refuses otherwise (configure.go:77, pins.go:581-594).
- SEC-51a: on a pinned master a different `server_id` is ignored (configure.go:140).

Tests and normal flows:
- `TestMasterRefusesAccountTakeover` covers all the brief's refusal cases without MySQL.
- `TestPinnedMasterKeepsAccountsAndRestartsOnce` runs on a real mysqld and checks that grants and passwords are unchanged by logging in.
- The normal flows still work:
  - `dumpAccountHost` keeps the dump host a literal IP (snapshot.go:97);
  - a prepare_master re-run keeps the existing account (`created` logic, prepare_master.go:112-125);
  - the E2E covers prepare, create and resync on pinned agents.

The audit's PoC is only partly closed; see the Security section and New Issue I-1.

### K (SEC-44): audit limits

- **Caps:** each string ≤128 runes, each list ≤64 items, whole detail ≤2 KiB, otherwise replaced by `{"dipotong":true,"panjang":N}` (audit.go:36-37,119).
- **Lengths only:** every outcome other than ok stores names and lengths only (audit.go:66, `lengths`).
- **login_failed:** an unknown username becomes `(tidak dikenal) <8 hex>` (audit.go:200, handlers_auth.go:94).
- **Tests:**
  - `TestAuditRejectedInputIsBounded` (a ~63 KiB form, detail ≤2 KiB, no names);
  - `TestScrubDetail`;
  - `TestAuditLoginFailed`.

### L (SEC-47): settings warning and audited values

- The plain-HTTP warning is shown above the token field when `r.TLS == nil` (handlers_settings.go:200, settings.html:11).
- `settings_update` records the new chat id and the new base URL, never the token (handlers_settings.go:323,326).
- Tests are updated.

### M (SEC-46): flap control

- **Flap detection:** a reopen less than 5 min after the previous one closed is flapping (notifier.go:355-379). It produces one "berkedip N kali" message per (server, kind) per 5 min; otherwise the alert is held.
- **Recovery hold:** every "pulih" waits 1 min. It is dropped if the same kind reopened within that minute (notifier.go:401-428). The last alert of a flapping series gets one "pulih".
- **Waking Run:** held messages wake `Run` through `untilDue`, which waits at least 1 s, so there is no busy loop.
- **Test:** `TestFlappingSendsAtMostTwoMessages`: 100 open/close cycles in 2 min give exactly 2 messages, then one "pulih".

### N (docs)

The README now says:
- the panel can release a replica's role and source pins, and only the master pin is immutable from the panel (README:319-327);
- pin bootstrap needs the panel's role hint (README:339-346);
- the audit log resists the application but not root on the panel host (README:578);
- a production master should not run on the panel host (README:637);
- what data leaves to Telegram (README:488-496).

### O (M1, M7, M9)

- **M1:** migration 0005 adds `close_reason`. `evaluate` closes with a reason (alerts.go:388, `inactiveReason` :419). The notifier sends "ℹ️ … ditutup: <reason>" instead of "pulih" (notifier.go:401). The Peringatan page shows "Ditutup: …".
- **M7:** README:340 (upgraded agents write the role pin only, and only with the panel's hint).
- **M9:** `cloneFailure` adds the "Paksa dump" sentence (clone.go:341).

## Security PoC closure (J, K, L)

### J (SEC-42)

| Audit PoC row | Status after the fix |
|---|---|
| `createDumpUser{dbpanel_repl@%}` (replace the replication account) | **Closed.** Refused by the name rule, and the existing account is never altered. |
| `createReplUser{dbpanel_repl, new password}` against the existing `dbpanel_repl@'%'` | **Closed** for that row: the existing account is refused, and the password and grants stay. |
| Same effect through a **host variant** | **Open (New Issue I-1).** See below. |
| `mysql.restart` at will | **Closed**, with one exception: the grant is re-armed only when the live server still shows a restart reason (`restartReasons` depends on `log_bin` and a loopback bind, not on params). A no-change configure on a healthy master gives no grant. A master whose own my.cnf defeats the dbpanel block can still be restarted once per configure (New Issue M1). |
| `configure{server_id:N}` | **Closed** (SEC-51a). |
| Rows 1–3: a RELOAD account → `RESET MASTER` / FTWRL / `LOCK TABLES`, and SELECT on all data | **Still open, by design (deferred).** See below. |

**The host variant (New Issue I-1).** `refusedByRole` (pins.go:551-556) checks only the name for `createReplUser`. `FromHost` is free, and `createAccount` treats `dbpanel_repl@'10.0.0.%'` (or `@'<replica IP>'`) as a new account. A compromised panel can therefore create one with its own password and `REPLICATION SLAVE, BACKUP_ADMIN` (`replGrants`, users.go:31).
- MySQL authenticates against the most specific matching host row only, so every replica in that range fails with 1045 on reconnect. That is the availability effect of the PoC row.
- The attacker also holds BACKUP_ADMIN, which is enough to CLONE the whole master.

So the answer to "can a pinned master still be made to create any `dbpanel_*` account the panel chooses?" is yes: `dbpanel_repl` at any host not yet present, and `dbpanel_dump_<8hex>` at any literal IP.

**Rows 1–3 (deferred).** A compromised panel can mint `dbpanel_dump_<8hex>@'<its own IP>'` with RELOAD/SELECT/LOCK TABLES and use it from the panel host. That is SEC-42 item 4 (dump without a RELOAD network account), which is deferred to Plan 4. SEC-42 item 2 (the agent enforcing a maximum age on dump accounts, and expiring them) is in neither the brief nor its deferral list (see Out of scope).

### K (SEC-44)

- **Closed:** the per-row amplification.
  - The PoC's 60,000-character refused `server_create` name now stores `{"name_len":60000}`.
  - A 64 KiB refused `wipe_list` stores `{"wipe_list_count":N,…}`.
  - A detail is never more than 2 KiB.
  - An outcome is at most 500 runes (unchanged), and the refusal texts checked here (`msgWipeList`, the server name refusal) are fixed strings that do not echo input.
- **Not closed (deferred, SEC-44 items 4 and 5):** row count. About 200 refused POSTs per second still append roughly 0.3–1 KB each, with no rate limit or retention.

### L (SEC-47)

- **(a) Token over plain HTTP:** a warning is shown. This is mitigation, not closure; HTTPS is SEC-23, deferred.
- **(b) Silent redirection of chat id or base URL:** now reconstructible from the audit. One gap: the base URL value is cut at 128 runes while up to 200 are accepted, so the tail of a long phishing host can be hidden (New Issue M3).
- **(c) Public `@channel`:** only documented in the README, which was not in the brief.

## New Issues in the Fix Diff

### Critical

None.

### Important

**I-1. A pinned master still lets the panel create a replication account under a more specific host, which shadows `dbpanel_repl@'%'` and hands out BACKUP_ADMIN.**
- **Where:** `internal/agent/drivers/mysql/pins.go:551-556`, `users.go:58`.
- **Failure scenario:** a compromised panel sends `mysql.createReplUser{User:"dbpanel_repl", FromHost:"10.0.0.%", Password:P}`.
  1. It passes the name rule.
  2. `accountExists(user, '10.0.0.%')` is false, so the account is created with `REPLICATION SLAVE, BACKUP_ADMIN`.
  3. Every replica in 10.0.0.0/24 now matches this row before `'%'`, gets 1045 at its next reconnect, and replication breaks master-wide.
  4. The attacker can CLONE the whole master with P.
- **Fix (cheap, no flow uses anything else):**
  - On a pinned master accept `createReplUser` only with `FromHost == "%"`, the only host `masterConfigure` sends (prepare_master.go:111).
  - Or refuse when any `dbpanel_repl` row exists at any host.
  - Add the host-variant case to `TestMasterRefusesAccountTakeover`.

### Minor

**M1. The restart grant can be re-armed, and it is used up before the restart runs.**
- **Where:** configure.go:66, pins.go:581-594.
- **Re-arming:** `GrantRestart` is set from `restartReasons(master, st)`. On a master whose own my.cnf beats the dbpanel block (for example a later `bind-address=127.0.0.1` or `skip-log-bin`), every no-change configure re-arms a restart. A compromised panel can then loop configure→restart.
- **Used up early:** `AllowRestart` consumes the grant before `Restart`, so a restart that fails needs a manual one ("Sudah saya restart" then works).
- **Suggestion:** grant only when the configure changed the block (`Applied` or the block differs), and consume the grant only on success.

**M2. The `awaitFresh` hold also masks "Agent offline", for up to `2×CheckEvery + 30 s`.**
- **Where:** state.go:242,262-271.
- **Cause:** the early return comes before `agentOffline` is evaluated. The bound scales with the check interval (10–3600 s).
- **Scenario:** with defaults the hold lasts at most about 65 s. With CheckEvery=3600 s, an agent that disconnects right after Start, Stop or a resync becoming ready keeps its previous status (for example Sinkron), and no `agent_offline` alert opens, for about 2 h. During that time the background fetch fails at once.
- **Is it bounded?** Yes. It is re-armed only by real store changes, and the bound and fallback are tested. It cannot hold a stale status past the bound.
- **Suggestion:** do not hold while `!online` past `offlineGrace`, or cap the bound (for example `min(2×CheckEvery, 60 s) + configureTimeout`).

**M3. The audited base URL is cut at 128 runes, but 200 are accepted.**
- **Where:** audit.go:37 (`limit`) with handlers_settings.go:33,326.
- **Scenario:** a URL such as `https://panel-rs1.internal.lan.<~100 chars>.evil.example/` is recorded as `https://panel-rs1.internal.lan.aaaa…(dipotong)`, which hides the host the links really go to (SEC-47 b).
- **Fix:** exempt that value from `maxAuditString` (200 bytes fits easily in 2 KiB), or cap `maxBaseURL` at 128.

**M4. An empty base URL now means no links, and nothing falls back to the public URL.**
- **Where:** handlers_settings.go `renderStoredSettings`; notifier.go:82; monitor/settings.go:24.
- **Cause:** before the fix, the pre-fill made a first settings save store the public URL. Now a fresh panel whose owner saves the Telegram settings without typing an address sends every alert without a link, while the greyed placeholder looks like a value.
- **Also:** the comment at `monitor/settings.go:24` (`"" means the panel's public URL`) is now wrong.
- **Suggestion:** this is a product choice. Either fall back to `PublicURL` when the setting was never saved, distinguishing "unset" from "saved empty", or say plainly under the field that it is empty now.

**M5. A successful detail over 2 KiB is replaced entirely.**
- **Where:** audit.go:119.
- **Scenario:** a successful resync with a large wipe list (64 names of about 40 characters) becomes `{"dipotong":true,"panjang":N}`, and the record of which databases were dropped is lost.
- **Suggestion:** shrink further (fewer list items, shorter strings) before giving up.

**M6. Flap state lives in memory, so a "pulih" can be lost or wrongly sent.**
- **Where:** notifier.go:390-428.
- **Lost after a restart:** after a panel restart (`flapSent` is gone), the last alert of a flapping series is still held, and it was never announced itself (NotifyCount 0, the "berkedip" went with an earlier alert). It closes silently as recovered, so the owner never gets a "pulih" after the "berkedip".
- **Lost on a reasoned close:** the same happens when such a held alert is closed with a reason (no "ditutup" note).
- **Wrong "pulih":** a stale entry, for example one left when a kind was switched off, which is kept up to 24 h, can make a never-announced alert send a "pulih" whose duration is measured from the stale time.
- **Severity:** these are recovery messages lost or wrong after restarts or edge cases, not in normal operation.

**M7. A "berkedip" message for an alert that stays open does not say it is still open.**
- **Where:** notifier.go:375-379, message.go `FormatFlapping`.
- **Scenario:** `master_down` closes and reopens once within 5 min and then stays down. The only message is "⚠️ <server>: master diduga tumbang berkedip 2 kali …". It has a warning icon, and no open message follows until the 6-hour repeat.
- **Suggestion:** say "masih terbuka" when the latest alert is open, and keep the critical icon for critical kinds.

**M8. `dumpAccountHost` can resolve the panel's `--public-host` to a loopback address.**
- **Where:** snapshot.go:97-115.
- **Scenario:** on Debian and Ubuntu, `/etc/hosts` maps the machine's hostname to 127.0.1.1, and the IPv4-first pick returns it. The dump account is then `@'127.0.1.1'`, while the remote master sees the panel's LAN address, so the dump fails with 1045. This affects create and resync by dump of a replica on the panel host, when `--public-host` is a name.
- **Suggestion:** skip loopback results (fail with a clear message if only loopback remains).

### Checked and found sound

- **CLONE lift:**
  - restored on every failure before the data phase, including cancel, with a non-cancelled context;
  - never restored after the data phase;
  - re-applied after success by the restart and the panel's `setReadOnly`;
  - never lifted on paths that fail before it (plugin install, donor list).
- **`awaitFresh`:**
  - **locking:** all state is under `mu`; the fetch runs in `goBgLocked` (registered under `mu`, waited for by `Close`) and re-enters through `report`; `freshBound` reads settings under `mu`;
  - **no lock cycle:** the orchestrator calls into the Monitor, never the reverse;
  - **report ordering:** a late report taken before the change is not trusted, because the test uses taken-ago time;
  - **in-flight reports:** a report being ingested exactly as the change is detected only triggers an extra fetch;
  - **while held:** with a held "Belum siap", `evaluate` treats the replica as inactive, so no false alert opens.
- **Flap control and the recovery hold:** a real recovery is delayed, not lost, in steady operation (exceptions in M6). `LaterAlertExists` is bounded by `closedAt+1 min`, and store times are fixed-width RFC3339, so the lexical comparisons are valid.
- **Migration 0005:** `ADD COLUMN … NOT NULL DEFAULT ''` is valid on an existing SQLite table with rows. `alerts` has no triggers (the append-only triggers are on `audit_log`), and the partial unique index is unaffected. A pre-0005 binary refuses the newer schema, which is the existing policy.
- **E2E flips:**
  - both constants are true;
  - the (c2) step and the (e) false-stop check are strict;
  - only recovery waits were lengthened (`e2eRecoveryWait`, required by the 1-minute hold);
  - no assertion was weakened.

## Out-of-Scope Observations

1. **Deferral ledger:** SEC-42 item 2 (the agent enforcing a maximum age on `dbpanel_dump_%` accounts, and `PASSWORD EXPIRE` / `FAILED_LOGIN_ATTEMPTS`) is neither implemented nor listed among the brief's Plan 4 deferrals. The controller should add it to the ledger next to SEC-42 item 4, since rows 1–3 of the PoC stay open until then.
2. **Raw username in journald:** `handlers_auth.go` still logs the raw typed username with `a.Logger.Info("login failed", "username", username, …)`. A password typed into the username field reaches journald even though the audit row is now hashed. The line is unchanged by the diff.
3. **Hold and evaluation share a status code:** while the held status is `StatusNotReady`, `evaluate` (alerts.go `active`) treats the replica as inactive. Any server-level alert still open would close with "replika belum siap dipantau" and reopen after the fresh report. This is harmless today, because a resync start already closes them (M2 of the final review is deferred), but it couples the hold to the M2 design.

## Verdict

**RESIDUALS.** All 15 items A–O are addressed with tests, and the E2E flips are strict. Before merge:
- **I-1:** restrict `createReplUser` on a pinned master to `FromHost "%"`, or refuse any existing `dbpanel_repl` host row, and add the regression case. This closes the remaining SEC-42 takeover path.

Recommended, cheap:
- **M3:** record the full base URL.
- **M1:** grant a restart only when the configure changed something, and consume it on success.
- **M2:** do not hold the status while the agent is offline, or cap the bound.
- **M4:** decide the empty-base-URL default and fix the stale comment.

The others (M5–M8) can go to the ledger.
