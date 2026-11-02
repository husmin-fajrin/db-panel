# Final fix wave report — Plan 3

Implementer: Opus 5.5. Range: 05a88f7..db6af8c (7 commits). Not pushed.

## Commits

| SHA | Subject | Items |
|---|---|---|
| c3eece8 | fix(agent): CLONE resync lifts read-only; pinned master refuses account takeover and unguarded restart | A, J, I, F (comment), O-M9 |
| cb19a23 | fix(panel): judge replicas only on reports taken after a state change | B |
| 17402c0 | fix(panel): Telegram error wording, flap control, no false recovery | E, M, O-M1 |
| bf9733f | fix(web): durable unlock instructions, set-source guidance, audit limits, settings | C, D, F (modal), G, H, K, L |
| 01cb1c1 | docs: README corrections | N, O-M7, README parts of C/M/B/J |
| 72ea276 | test(e2e): CLONE resync and quiet resync are fixed; Stop→Start sends nothing | B/A E2E flips |
| db6af8c | test(panel): the wired notifier's recovery waits out the one-minute hold | M (TestNotifierWiredToMonitor) |

## Per item

### A (F1) — CLONE resync into a locked replica
- `internal/agent/drivers/mysql/clone.go`: `CloneFrom` calls `liftReadOnly` after `installClonePlugin`/donor list and before the CLONE session. A deferred `restore()` runs unless `dataReplaced` was set (set right when the data phase is over: after `waitClone` returned a nil error or a 3707/lost-connection error). So restore runs on every pre-data-phase failure: `db.Conn` / session read errors, `waitClone` cancel errors (incl. KILL QUERY paths), and a non-restart CLONE error. After the data phase the restart re-applies the persisted/my.cnf lock.
- Panel defence: `internal/panel/orchestrator/create_replica.go` `replicaSnapshot` calls `mysql.setReadOnly{on:true}` after a successful CLONE with `AdminReplaced=false`, before "Sambungkan replikasi" (the AdminReplaced path already relocks in `verifyCopyAfterReconnect`).
- Tests: new `TestCloneIntoLockedReplicaWithData` (snapshot_test.go): recipient holds schema `a` with `SET PERSIST read_only/super_read_only=ON`; CLONE from an unreachable donor fails ("CLONE gagal", "Paksa dump") and leaves both ON; a real CLONE then succeeds, replaces `a.old` with the master's `a.t`, and ends with both ON. Verified to FAIL on the old clone.go with `1290 … --super-read-only` (reproduces F1), PASS with the fix. Orchestrator command lists updated (`replicaCmds` adds `mysql.setReadOnly` after `mysql.cloneFrom`).
- `e2eCloneResyncFixed = true`; TestE2ECreateReplica now runs "resync_replica with CLONE succeeded in 10s".

### B (F2 + I1) — no judgement on reports older than a state change
- `internal/panel/monitor/state.go`: `serverState.awaitFresh`. `resolve(id, s, reportAt)` detects, for a replica already resolved, a change of `repState`/`repPaused` whose new state is judged (ready/attention); it sets `awaitFresh = now` and, unless the report being ingested is itself at/after the change, queues `fetchFreshLocked` (background `monitor.snapshot`, `configureTimeout`, not rate-limited, via new `goBgLocked`). A change into a non-judged state (resyncing, creating, failed) clears the hold: Belum siap is immediate.
- `derive`: while `awaitFresh` is set and the stored report was taken before it, the previous status is kept (no re-derive, no gap clock); cleared by a report with `at >= awaitFresh` or after `freshBound() = 2×CheckEvery + configureTimeout` (60 s with defaults), after which the usual rules apply.
- `monitor.go`: `Monitor.ReplicaChanged(serverID)` (resolve → derive → commit → evaluate).
- Orchestrator: `Alerts.ReplicaChanged` added to the interface; `Runner.replicaChanged` called after `SetReplicaState` in `setReplicaState` (all jobs), the resync start (resyncing, deferred so a failed create is covered), `replicaVerify`'s `SetReplicaPaused(false)`, the startup-resume state write, `StartReplica` and `StopReplica`. `StopReplica` writes `paused=true` before `mysql.stopReplica` and clears it on failure.
- Tests (monitor, fake clock, gated fake-agent `monitor.snapshot`): `TestResyncReadyHoldsUntilFreshReport` (mid-job report with no replication and the lock lifted; ready → Belum siap kept through 3 ticks, no replica_stopped/replica_unlocked, no lock event; fresh report → Sinkron), `TestStartFromPausedOpensNoAlert` (also a pre-change report arriving late with TakenAgoMS=5000 is not trusted), `TestFreshReportBoundFallsBack` (within the bound Dihentikan kept; after it "Berhenti (di luar panel)" and replica_stopped open). All three verified to fail with the hold disabled. Orchestrator: `TestStopStartSetsPaused` asserts paused is set when STOP REPLICA runs, reverted on failure, and ReplicaChanged calls; resync happy path asserts ≥3 ReplicaChanged.
- `e2eResyncQuietFixed = true`; new E2E step (c2): Start replika from Dihentikan → Sinkron, no replica_stopped open and no "berhenti" (no message naming the replica at all) since the Stop.

### C (I2) — SET PERSIST unlock
`web/templates/_status.html` (emergency box), `web/templates/replica_detail.html` (Lepas dan hentikan), `handlers_mysql.go` notice `dilepas-henti`, README: `SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF`, each saying it survives a restart. Tests: `TestMasterPageDownShowsManualGuidance`, `TestDetachModes` (modal text, no `SET GLOBAL super_read_only`, and the notice).

### D (I3, spec 5.5) — set-source guidance
`handlers_mysql.go` `renderMaster` builds `SetSource` rows: every replica right after a save (`?pesan=sumber`), otherwise each replica whose last health report shows `Source_Host:Port` different from the topology's address (cheaply known from the monitor view; replicas without a report are not listed). `master_detail.html` shows an alert block with `sudo dbpanel agent set-source <host:port>` per replica (linked, with "sekarang mengikuti …"), plus that running replication keeps the old address until Resync ulang. The "sumber" notice points to it. Test: `TestMasterPageSetSourceGuidance`.

### E — Telegram errors in Indonesian
`notify/telegram.go` `Send`: transport errors "Telegram tidak terjangkau: <scrubbed url error>", API refusals "Telegram menolak: HTTP <code>: <description>". This one place feeds telegram_error, the SendTest flash (via `SendTest`) and the Peringatan page. Tests: `TestSendErrorHidesToken`, `TestSendFailureIsVisibleAndRetried`, `TestSendTestRateLimited` (unreachable API → prefix, no token).

### F — resync restart
Comment in `replicaPrepare` corrected (a resynced replica may serve reads but is rebuilt; restart is unasked and announced in the modal). Resync modal: "Selama resync, data di replika ini belum lengkap untuk dibaca, dan MySQL di replika bisa dimulai ulang (restart) bila pengaturannya perlu diubah." Test in `TestResync…` modal assertions (web_controls_test).

### G — "belum dihitung"
`monitor.Status.GTIDGapKnown` set in `DeriveReplica` when the gap is computed against a known master GTID set (only reached for a judged replica whose MySQL answered with a replication row). Replica page shows "belum dihitung" otherwise. Tests: `TestGTIDGapKnownOnlyWhenComputed` (monitor), replica page test asserts "belum dihitung" and no "0 transaksi", then "3 transaksi" when known.

### H — base URL placeholder-only
`renderStoredSettings` no longer pre-fills the public URL; it is only the placeholder, and the help text says empty means no links. Tests: defaults page shows `value="" placeholder="<public>"`; saving, then clearing, persists empty and shows it empty.

### I — proto comment
`proto.PreflightParams`: "Without Resync, WipeList and ReclaimBytes change no check (WipeList's names are still validated)."

### J (SEC-42, SEC-51a) — pinned master
- `pins.go`: `refusedByRole` rules on a pinned master: `mysql.createDumpUser` only `^dbpanel_dump_[0-9a-f]{8}$` from a literal IP (`net.ParseIP`, no `%`/`_`); `mysql.createReplUser` only `dbpanel_repl`. `Pins.GrantRestart(needed)` (set by every successful `mysql.configure`) and `Pins.AllowRestart()` (on a pinned master one restart per configure that reported `NeedsRestart`, consumed at grant time; otherwise refused with a pin error).
- `users.go`: `createDumpUser` now goes through `d.allow`; `createAccount(..., keepExisting)` refuses an existing account on a pinned master (never ALTER) with "akun … sudah ada di master yang dipin; …". The exported `CreateReplUser` (tests/CLI) keeps the old behaviour.
- `configure.go`: on a pinned master a requested `server_id` other than the current one is ignored (`configure(..., keepServerID)`).
- Orchestrator: `dumpAccountHost` keeps the account host an IP — `srv.Address` (the pairing RemoteAddr) is already a literal IP and is normalised; a name (the panel host's `--public-host`) is resolved (IPv4 first) through `lookupIP`. prepare_master: `configure` pins the master before `createReplUser` runs, so a re-run of "Siapkan master" would hit the existing-account refusal; `ensureTopology` now reports whether it created the topology: pre-existing topology → the existing account is kept ("sandinya tidak diubah"), new topology → the job fails with the `DROP USER 'dbpanel_repl'@'%'` instruction (see Deviations).
- Tests: `TestMasterRefusesAccountTakeover` (no MySQL: createDumpUser{dbpanel_repl}, {…@'%'}, {…@'10.0.0.%'}, {…@hostname}, bad names, createReplUser{dbpanel_x}, restart without pending configure — refused; valid dump account IPv4/IPv6 and createReplUser{dbpanel_repl} pass; restart on replica/unassigned not the pins' business). `TestPinnedMasterKeepsAccountsAndRestartsOnce` (real mysqld: first configure pins master with NeedsRestart; fresh createReplUser OK; again → refused, grants and password unchanged (login check); createDumpUser{dbpanel_repl} refused; per-job dump account OK, again → refused, password unchanged; restart OK once, second refused; configure{ServerID:4242} keeps server_id; restart then refused). Both verified to fail on the old pins/users/configure. Orchestrator: `TestDumpAccountHostIsAnIP`, `TestPrepareMasterKeepsExistingReplAccount`. Legitimate flows proven by the orchestrator tests and the E2E (prepare_master with restart approval, create_replica dump/clone, add databases, resync dump and clone) on real pinned agents.

### K (SEC-44) — audit limits
`web/audit.go`: `scrubDetail(detail, lengthsOnly)`; every string ≤128 runes and list ≤64 items with "…(dipotong)" / "…(N lagi)" markers; a detail over 2 KiB becomes `{"dipotong":true,"panjang":N}`. Any outcome other than `ok` (refused input and failed actions) stores names and lengths only (`<name>_len`, `<name>_count`; numbers/booleans kept). `login_failed` stores the username only when the account exists, else `(tidak dikenal) <8 hex of SHA-256>` in the username column. Tests: `TestScrubDetail` (cuts, 2 KiB, lengths-only form `{"confirm_name_len":7,"force_dump":true,"wipe_list_count":3}`), `TestAuditRejectedInputIsBounded` (a ~63 KiB form — the body limit is 64 KiB — of unknown wipe-list names → detail ≤2 KiB, no name, `wipe_list_count`), `TestAuditLoginFailed` (unknown user hashed, no "maling" anywhere).

### L (SEC-47)
Settings page shows the plain-HTTP warning (same wording as the MySQL form, about the bot token) when `r.TLS == nil`. `settings_update` detail records the new `telegram_chat_id` and `panel_base_url` values when they change; never the token. Tests: defaults page warning; `TestSettingsUpdateAuditHasNoToken` now asserts the chat id value is recorded and the base URL value on change, token absent (the old assertion that the chat id is absent was the opposite of L and was changed).

### M (SEC-46) — flap control
`notify/notifier.go`: a first send of an alert reopened <5 min after the previous one of its kind/server closed is flapping: one "⚠️ <server>: <kind awam> berkedip N kali dalam 5 menit terakhir" (+link) per (server, kind) per 5 min (N = alerts of that kind opened in the last 5 min), otherwise the alert is held; once 5 min pass and no reopen counts, the ordinary message goes out. Every "pulih" waits until the alert stayed closed ≥1 min and is dropped if the kind reopened within that minute; the last alert of a flapping series gets one "pulih" once quiet. Held messages wake `Run` through a timer (`untilDue`, ≥1 s). Store helpers `PreviousAlertClosedAt`, `LaterAlertExists`, `CountAlertsOpenedSince`. Tests (fake clock): `TestFlappingSendsAtMostTwoMessages` — 100 open/close cycles in 2 min with a pass after every change → exactly 2 messages (first alert + one "berkedip"), then one "pulih" after a quiet minute; after 10 quiet minutes an ordinary alert, a reopen 1 s after its close → "berkedip 2 kali", no "pulih" for the reopened one. `TestRecoveredMessageOnce` updated for the 1-min hold.

### N — README
Pins: role pin on Siapkan master/Buat replika, source pin on the first replication/dump/clone; pinned master's new refusals (accounts, server_id, restart); the panel can release a replica's role and source pins (Lepas dan hentikan, Hapus server, cancelled creation) and what that means; only the master pin is immutable from the panel; upgrade bootstrap needs the panel's role hint (master on Siapkan master, replica on Buat replika; a master with a stale replica row is not pinned). Audit: values recorded for chat id/base URL, limits, lengths-only, unknown users hashed; tamper-resistant against the application, not root on the panel host. Security: do not place a production master on the panel host (embedded agent's pins are no boundary against the panel). Telegram: what data leaves (server names, alert kinds/statuses, errno + plain explanation, times, links with internal panel address and server ids, weekly figures; never patient data/table contents/MySQL text), private groups not public channels; flap control and closing notes; error wording. Monitoring note on fresh reports after changes; resync may restart MySQL; "Paksa dump" when CLONE keeps failing.

### O
- M1: migration `0005_alert_close_reason.sql` (`alerts.close_reason`, additive); `evaluate` closes alerts of a server no longer judged with a reason ("Resync ulang dimulai", "replika sedang dibuat ulang", "replika dilepas dari panel", "replika belum siap dipantau"); the notifier sends "ℹ️ <server>: peringatan <kind> ditutup: <reason>" (at once, no "pulih"; nothing if it never went out); the Peringatan page shows "Ditutup: <reason>". Tests: monitor `TestStatusAlertsOpenAndClose` (resync and Lepas reasons; a recovery has none), notifier `TestClosedWithReasonIsNoRecovery`, store `TestAlertCloseReasonAndNeighbours`, web alerts page.
- M7: README (see N).
- M9: `cloneFailure` appends "Bila CLONE terus gagal, ulangi dengan pilihan "Paksa dump"" (asserted in the new clone test).

## Deviations and judgement calls
1. J / prepare_master: the brief says prepare_master creates `dbpanel_repl` on a not-yet-pinned server; in fact `masterConfigure` runs `mysql.configure` (which pins the master) before `createReplUser`. A fresh account is still created (only existing accounts are refused), so the first prepare works; a re-run with the existing topology keeps the account (log line), and a re-run that created a new topology fails with a `DROP USER` instruction instead of silently mismatching passwords. No rotation path exists (Plan 4/CLI as ruled).
2. J / restart grant: in-process only, consumed when allowed. If the agent restarts between configure and the user's "Restart MySQL di master sekarang", the restart is refused with a message to restart MySQL on the server; "Sudah saya restart — periksa lagi" then completes the step. A configure that still needs a restart re-grants one, so the protection holds.
3. K: lengths-only applies to every non-`ok` outcome (refused input and failed actions), not only validation refusals: simpler and no call site can be missed; failed-but-valid actions lose their detail values (target id, action and outcome remain).
4. M: the 1-minute recovery hold applies to every "pulih" (needed to keep 100 cycles at ≤2 messages when a pass follows every change), so "pulih" now arrives 1–2 min after recovery; E2E waits for it with `e2eRecoveryWait = e2eWait + 1 min`. Flap memory (`flapSent`) is in memory only: after a panel restart at most one extra message.
5. M1 needed a schema column (migration 0005, additive) to record the reason; store/migration count tests updated to 5.
6. D: replicas without a health report are listed only right after a save; otherwise the permanent block shows only replicas whose reported source differs.

## Verification (final, foreground)
- `gofmt -l ./internal ./cmd` → empty.
- `go vet ./...` → ok; `go build ./...` → ok; `GOOS=darwin go build ./internal/...` → ok.
- `go test -race -count=1 ./internal/panel/monitor/ ./internal/panel/notify/ ./internal/panel/orchestrator/ ./internal/panel/web/ ./internal/agent/monitor/`:
  `ok monitor 21.126s`, `ok notify 17.294s`, `ok orchestrator 27.847s`, `ok web 40.684s`, `ok agent/monitor 1.163s`.
- `make test` first run: every package ok incl. `ok internal/agent/drivers/mysql 1023.901s`, except `--- FAIL: TestNotifierWiredToMonitor` (internal/panel: it expected an immediate "pulih"; M adds a 1-minute hold). Fixed in db6af8c (the test moves the panel clock 61 s ahead after the close; passes `-race -count=3`).
  `make test` second run: `exit=0`, 22 `ok`, 0 FAIL, `ok internal/panel 325.132s`, `ok scripts/install_test.sh` (drivers/mysql cached from the first run; its code did not change). shellcheck not installed (skipped by the script, as before).
- `make test-e2e`: `ok github.com/husmin-fajrin/db-panel/internal/panel 309.062s` (final code). A `-v` run on the same code minus a log string: TestE2ECreateReplica PASS ("create_replica with CLONE succeeded in 9s", "resync_replica with CLONE succeeded in 10s"), TestE2EMonitoring PASS ((a) 0s, (b) 1m10s, (c) 12s, (c2) 12s, (d) 1m16s, (e) 5s, 0 false stop messages), TestE2EPinnedRole PASS; no test skipped.

## Concerns
- "pulih" latency is now ≥1 min by design (M); the README says so.
- The CLONE lift lets writes reach the replica for the seconds before the CLONE drop phase (accepted in the review: the data is replaced anyway).
- Deferred items (SEC-42 item 4, SEC-43 item 1, SEC-45 hash chain, SEC-46 ingest limit/retention, SEC-48 pin status/alert, SEC-49, SEC-50, SEC-34, SEC-23) were not touched.
