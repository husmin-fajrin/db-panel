# Re-review of the final fix wave: Plan 4a (8195826..d6ba8f4)

Reviewer: opus, read-only. Security lens throughout. I ran `graphify query` first, then read the diff package in four passes:
1. Agent: `drivers/mysql/{driver,tls,users}.go`, the tests, `agent/monitor`.
2. Panel monitor and orchestrator, `store` with migration 0007, `e2e_https_test.go`.
3. `tlsui.go` and the `web/` handlers.
4. `install.sh`, `install_test.sh`, the templates and README.

I checked the tree at d6ba8f4 for anything the diff did not show: `auth/ratelimit.go`, the `migrate.go` transaction, the `detectPublicHost`/`resolvePublicHost` code, the agent `--dir` default, and the `trustCA` callers. I re-ran no tests. I only checked the log tails: vet 0, test 0, e2e ok in 293 s, install_test ok. `setpriv` is present on this host, so TestE2EHTTPS took the unprivileged path.

HEAD is now 321d529, one commit past d6ba8f4. It changes docs only: Plan 4b and 4c migrations are renumbered to 0008 and 0009.

## Finding Verdicts

| Item | Verdict | Where / note |
|---|---|---|
| **I1** web CA vs new interfaces | ADDRESSED | `tlsui.go:657` `webNames` splits names into chosen (public host, host name, extra names) and found. `:166` `ensureCA` and `:350` `renew` regenerate the CA only on `!Covers(chosen)`. A new CA uses `mergeNames(chosen, found)`, so subnets are constrained only at creation. `:215` `leafNames` drops found IPs outside the CA and logs them. Rulings met. (d) Renewal ignores new subnets; `TestNewInterfaceKeepsWebCA` covers it. `.old` rotation is correct (`:233`): `.old`→`.old2`, then current→`.old`, key before cert, and nothing moves when no current file exists. `.old2` itself is overwritten on a third regeneration (Minor N6). |
| **I2** HSTS way out | ADDRESSED | `web/handlers_cert.go:92` `certIPURL`. The URL is shown in the `layout.html` notice, on `cert.html` and in the README. HSTS stays at 1 year. |
| **I3** web cert expiry and silent fall-back | ADDRESSED | `monitor/alerts.go:642` `checkWebCertExpiry` uses a new kind `web_tls_expiring`. I accept the reason: the store allows one open alert per `(kind, server_id)`. A kind missing from the toggles counts as on (`handlers_settings.go:101`), so Telegram sends it by default. `CustomFallback` shows a persistent notice. The comment at `panel.go:209-210` still says "opens tls_expiring" (stale). |
| **I4** pre-plan agents | ADDRESSED | `orchestrator/tls.go:160` `tlsCall` maps `unknown_command` to the clear message and calls `AgentNeedsUpgrade`. `:184` `installCertOrSkip` is used only in `prepare_master.go` step 2 and in `replicaCert` when `!TLSVerify` (`create_replica.go`). `trustCA` (`:339`, callers `create_replica.go:769`, `tls.go:359,535`) is never wrapped. Ruling met. |
| **I5** Perketat START failure | ADDRESSED | `tls.go:571` `revertUnverified` runs STOP, then CHANGE with the previous unverified parameters (`SSL:true`, no CA, no verify, same databases), then `restartReplica` and unpause. The step fails with "verifikasi gagal …; replikasi dikembalikan tanpa verifikasi". `tls_verified` is never set true on this path. Ruling met. |
| **I6** restart during redirect | ADDRESSED | `runner.go:709` `resumeTighten` calls `restartReplica` when the agent is online and otherwise unpauses, so `replica_stopped` can fire. Covered by a test for both cases. |
| **I7** `ssl_ca` vs client-cert accounts | ADDRESSED | Agent `tls.go:600` `refuseClientCertAccounts` runs before any file is written. It refuses when `ssl_type IN ('X509','SPECIFIED')` accounts exist and `ssl_ca` is set and differs from the panel's path. Ruling met; `TestTLSInstallRefusesClientCertAccounts` runs on real mysqld. |
| **I8** master address change | ADDRESSED | `tls.go:311` `checkMasterCertHost` runs in `replicaCheckMaster` at step 0. `tlsInstall` records `certNames`, and `setMasterSource` shows the `sumber-sertifikat` hint. Certificates from before 0007 have `Hosts=nil` and are never refused. Acceptable: this branch was never released. |
| **I9** `confirm_rejected` and limiter | ADDRESSED (see N1) | `handlers_confirm.go:165` writes the audit row: action, target kind and id, outcome `token_invalid`, never the token; the test checks this. Limiter semantics (`:100-121`): only wrong passwords count, success resets, and the budget is shared with the login `UserLimiter` under the same `usernameKey`. The semantics match the ruling, but the new check-then-fail flow is not atomic (N1). |
| **I10** fake sudo | ADDRESSED (see N2, N3) | Template variables `BIN`, `UNIT_DIR` and `CONFIG_DIR` (`agent-install.sh.tmpl:22-24`); the agent's default `--dir` is `/etc/dbpanel`, so production behaviour does not change. All four points are in place: the stub rewrites only the assignments and requires all three, an allowlist grep (`e2e_https_test.go:229`), `setpriv` when euid is 0 (`:234`), and a skip without setpriv. The stub refuses an empty `ROOT`. `install_test.sh:21` runs `unset ROOT`, and `need_root` (`:27`) precedes every `source` of install.sh (lines 193, 233, 271, 296, 513). Ruling met. On (c): under setpriv as uid 65534 the script can still write only to world-writable places (`/tmp`, `/dev/shm`, `/var/tmp`) and nobody-owned paths. `/root` and `~` (HOME stays `/root`) fail with EACCES. The allowlist itself does not cover `/root`, `~`/`$HOME`, `/home`, `/tmp`, `/srv`, `/boot`, `/bin`, `/sbin`, `/lib` or bare `/usr`, `/var` and `/opt`; see N3. |
| **M5** CA fingerprint | ADDRESSED | `tlsui.go:454` `caFingerprint`, shown on `cert.html` and `settings.html`. |
| **M6** upload too big | ADDRESSED | `handlers_cert.go:132` reads limit+1 and returns `errUploadTooBig`. The 413 now renders "Berkas terlalu besar" and is audited. Imports are grouped. |
| password `tr -cd` | ADDRESSED | `install.sh:460`. Tested by the `password-esc` case. |
| **SEC-54** points 1–2 | ADDRESSED | The fingerprint is printed by `install.sh:463-464`, read as the panel user and filtered to hex digits and colons. It is also on Settings and `/sertifikat`, with per-system matching steps and a warning when the page came over HTTP. `install.sh` and the notice link point to `https://…/sertifikat`. Point 3 (GPO from the file on the host) is also in `cert.html` and the README. |
| **SEC-55** points 2–3 | ADDRESSED | `tlsui.go:564` `tooBroadName` requires at least 3 labels, except for `.local`, `.lan`, `.internal`, `.home` and `.corp`, and refuses names directly under the built-in public suffixes. `webNames` also drops stored names that are too broad. The README claim is corrected. Point 1 (/32 constraints) remains scheduled for "before release". |
| **SEC-56** points 1–3 | **PARTIAL** | Point 1: `users.go:431` `sweepDumpUsers` uses `lock_wait_timeout=5` and KILLs before DROP, only when `kill` is set, which only the agent's own sweep sets (the panel command keeps `kill=false`). Point 2: `users.go:581` `KillDumpLocks` and `monitor.go:265`. Point 3 is only half done: `MAX_USER_CONNECTIONS 2` is set (`users.go:384`), but `PASSWORD EXPIRE INTERVAL 1 DAY` and a sweep age of 6 h or less were not done. The report says only "Age stays 24 h", with no reason or ruling. (a) The KILL never hits a non-dump account. `killSessions` (`:390`) is gated by the anchored `^dbpanel_dump_[0-9a-f]{8}$` (Go `$` is end-of-text). It matches `information_schema.PROCESSLIST.USER` (the login user) by exact parameterised equality, never by host. The guard filters `threads.PROCESSLIST_USER` by LIKE and then by the same regex. A legitimate dump is not at risk: `dump.go` runs `FLUSH TABLES` before FTWRL with `--single-transaction`, so the GLOBAL S lock lasts only until `UNLOCK TABLES`. A hold of 60 s or more means FTWRL is stuck behind a long statement, and every write is already blocked. Killing it (a failed copy, retried) is the right trade-off. See N4 for the cadence. |
| **SEC-57** README/Settings | ADDRESSED | Settings info box, `install.sh:476`, README install section and the Keamanan bullet. |
| **SEC-58** | ADDRESSED (see N5) | (b) Agent `tls.go:266` `openTLSDir` uses `os.OpenRoot(datadir)`, `Mkdir`/`Lstat`/`OpenRoot` through it, `fixDir` (`:306`) does fchmod/fchown on the fd, and `write` (`:333`) uses `O_EXCL` through the root plus fchmod/fchown on the fd and `Root.Rename`. The rotation swap/undo and rollback removals go through the root. No path-based `Chmod`, `Chown`, `Lchown`, `Rename`, `Create` or `ReadFile` on the directory remains; grep confirms. The test (`tls_swap_linux_test.go`) uses `renameat2(RENAME_EXCHANGE)` against a victim outside the datadir, 1500 iterations of `trustTLS`/`requestTLS`. That proves the reported escape is closed; the implementer reports it fails on the old code. It does not exercise `installTLS` rotation (that needs mysqld), the chown path (the test runs as owner), or an in-datadir symlink target (N5). |
| **SEC-59** | **PARTIAL** | Upload, revert and extra names now require `requireConfirm` (`handlers_cert.go:167,202,217`), and `issueConfirm` binds them to kind `settings`, id 0. The audit's pre-merge list also names "tutup pemberitahuan". `POST /settings/cert-notice-close` (`router.go:227`) is still tokenless, with no ruling or reason given. Impact is now low: a session can no longer trigger a new CA by itself, so closing the notice only hides a legitimate one. |
| **SEC-60** (audit and limiter) | ADDRESSED (see N1) | As I9. |
| Migration 0007 (f) | OK | `0007_server_tls_hosts.sql`: `ALTER TABLE server_tls ADD COLUMN hosts TEXT NOT NULL DEFAULT ''`. It is idempotent through `applyMigration` (checks the version and applies in one transaction with the version insert). Existing rows get `''`, which scans as `Hosts=nil` ("unknown", never refused). The Plan 4b/4c plan text needed renumbering; that is already done in 321d529 (4b `0008_promote.sql`, 4c `0009_audit_chain.sql`, plus one line in the spec). |

Counts: 18 ADDRESSED, 2 PARTIAL (SEC-56 point 3, SEC-59 notice close), 0 NOT ADDRESSED.

## New Issues in the Fix Diff

### Critical
None.

### Important

**N1. `/confirm` limiter is check-then-act, so concurrent guesses bypass the budget.** `internal/panel/web/handlers_confirm.go:100-121`.
- **What changed:** before the fix, `ConfirmLimiter.Allow` checked and counted atomically under the limiter mutex. Now the request calls `Blocked()` on entry, runs the slow `CheckPassword`, and only then calls `Fail()` (`auth/ratelimit.go` `Blocked` records nothing).
- **Exploit:** a stolen session sends N concurrent `POST /confirm` requests (each needs only the session cookie and CSRF token). All of them pass `Blocked` before any `Fail` lands, so N passwords are tried per window. N is bounded only by the attacker's concurrency and the hash cost, not by the stated 5/min or 20/15 min.
- **Why the login page is different:** `handlers_auth.go:63` also has the atomic per-IP `Limiter.Allow`. `/confirm` now has no atomic gate.
- **Effect:** this undoes the purpose of SEC-60 point 1 ("cannot guess faster than at the login page").
- **Fix, either of:**
  - Reserve a slot atomically (`Allow` on both limiters, or a new `Reserve`), then refund on success (`Reset`, as ruled), so only wrong passwords end up counted.
  - Serialise `issueConfirm` per username (a keyed mutex or a single-flight per user).
- **Test:** fire 20 concurrent wrong `/confirm` requests and expect no more than the budget to reach `CheckPassword`.

### Minor

- **N2. The e2e helper permanently changes host directory modes.** `e2e_https_test.go:254-288` (`writeInstallStubs`) adds `g+x,o+x` (`|0o011`) to every ancestor of `t.TempDir()` that lacks o+x. It never restores them. With the default `TMPDIR=/tmp` this is harmless (`/tmp` is 1777). With `TMPDIR` under `/root`, a home directory, or a 0700 scratch directory (for example `/tmp/claude-0`), the test permanently makes `/root` or `$HOME` traversable by every local user, so world-readable files under it become reachable. That is a write outside the temp root, which is exactly what I10 set out to prevent. Fix, either of:
  - restore the original modes in `t.Cleanup`;
  - make a dedicated 0711 directory under `/tmp` for the root, and skip when an ancestor is not already traversable.
- **N3. The allowlist is narrower than its name.** `e2e_https_test.go:227-229`.
  - It catches only `/etc/`, `/usr/`, `/var/`, `/opt/` and bare `/etc`.
  - It misses `/root`, `~` and `$HOME`, `/home`, `/tmp`, `/srv`, `/boot`, `/bin`, `/sbin`, `/lib`, bare `/usr`, `/var` and `/opt`, and paths built from variables.
  - `grep -v '^BIN='` (and the same for `UNIT_DIR=` and `CONFIG_DIR=`) drops any line that merely starts with the variable name. A line like `BIN=/usr/x; cp a /etc/b` would pass.
  - As root, setpriv backstops all of these except world-writable directories. As a non-root developer there is no backstop: a future `~/.x` or `$HOME/...` line would run for real as that user.
  - Fix: refuse any absolute path literal or `~` outside the variable block, and match the three assignment lines exactly (`grep -vxF`).
- **N4. The FTWRL guard's cadence is set by the panel.** `agent/monitor/monitor.go:233,265`. The guard runs after each tick, and `monitor.configure` accepts `CheckEvery` up to 3600 s (`maxIntervalSec`). A compromised panel (SEC-56's attacker) can set 3600 s and hold FTWRL for up to about 2 h before the second sighting kills it. That is far better than "indefinite", but not the "1 minute" the README states. Fix: run the guard on its own fixed 15 s timer, independent of the configured interval, or cap `CheckEvery` for the guard.
- **N5. SEC-58: `Root.OpenRoot` follows an in-datadir symlink.** `tls.go:288-298`. Between `data.Lstat(tlsDirName)` and `data.OpenRoot(tlsDirName)`, the datadir owner can swap in a symlink to another directory inside the datadir, which `os.Root` allows. `fixDir` then fchmods it to 0700 and fchowns it to the mysql user. Almost everything in the datadir is already mysql's. The exception is a root-owned `lost+found` when the datadir is its own filesystem; it would be handed to mysql. Impact is negligible. Fix: after opening, `Stat` the opened directory and compare dev/ino with the `Lstat` result, or refuse when its owner is neither the data owner nor euid.
- **N6. `.old2` is dropped silently, and an extra CA key is kept.** `tlsui.go:233-255`. A third regeneration overwrites `.old2` without a log line. Separately, keeping two old, still browser-trusted CA keys readable by `dbpanel` slightly widens what a panel compromise yields, against the direction of SEC-63e (scheduled deletion of `.old`). Log the drop. Consider whether `.old2` should keep the key at all.
- **N7. Stale comment.** `panel.go:209-210`: "opens tls_expiring" should be `web_tls_expiring`.

## Out-of-Scope Observations (non-blocking)

- **SEC-56 remainder:** the guard watches only the GLOBAL lock. A dump account can still use `LOCK TABLES <t> READ` (it holds LOCK TABLES and SELECT) to block writes to one table. Only the 24 h sweep plus its 1 h interval bounds that. This is relevant to the open SEC-56 point 3 (sweep age of 6 h or less) and to point 4 (Plan 5).
- **I1 and auto-detected public host:** an auto-detected `PublicHost` is never persisted (`panel.go:331-355`, `detectPublicHost` takes the first up, non-loopback interface). It counts as "chosen", so if interface order or link state at boot changes (the physical NIC not up yet, a bridge with a lower index), the panel can pick a bridge IP and create a new CA. Consider persisting the detected value on first start, or treating it as found.
- **Browser test:** the Playwright modal test (`e2e_browser`) was not re-run after the upload form became multipart, as the implementer says. Run it before merge.
- **I4 timing:** a replica job whose master has a certificate but whose target agent is old still fails at `replicaCert`, after the data copy. The message is clear, but a step-0 capability probe would fail it before the copy.
- **`confirm_rejected` volume:** each rejected POST writes an audit row, so a session can grow the audit log. This is the same class as the ledger's "throttled requests each write an audit row" (Plan 4c).
- **README wording:** "setiap pemeriksaan (15 detik)" in the SEC-56 bullet assumes the default interval (see N4).

## Verdict

**Findings remain.** All ten Important items and the minors are fixed as ruled. Two audit items are partial:
- SEC-56 point 3: no password expiry, and the sweep age is still 24 h.
- SEC-59: closing the CA notice is still tokenless.

The fix introduced one Important issue, N1: the `/confirm` limiter is no longer atomic, so concurrent guesses bypass the shared budget. N1 is a small fix and should land before merge. Close the two partials or record controller rulings for them. N2–N7 can go with N1 or into the ledger.
