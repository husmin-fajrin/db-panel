# SDD ledger — plan: docs/superpowers/plans/2026-10-30-01-fondasi-panel-agent.md
Spec: docs/superpowers/specs/2026-10-30-db-replica-panel-design.md (read)
Branch: claude/install-superpowers-plugin-ev2k9i (designated by harness; no main branch exists; Ruling: work directly on this branch instead of a worktree — the harness mandates this branch and the repo has no other history; cost if wrong: none, nothing to protect)

## Pre-flight scan
| Pair / task | Produces vs consumes | Finding |
|---|---|---|
| T2 proto ↔ T3 registry | Dispatch(proto.Request) proto.Response; ErrorResponse codes | consistent |
| T2 ProbeResult ↔ T3 Collect ↔ T4 UpdateProbe | same type proto.ProbeResult | consistent |
| T4 store ↔ T7 agentapi | ConsumePairToken, MarkPaired(id,serial,addr), GetServer.CertSerial | consistent |
| T4 store ↔ T9 web | CreateServer, CreatePairToken(id,now,ttl), ActivePairToken, ListServers, DeleteServer | consistent |
| T4 store ↔ T10 panel | CountUsers, CreateUser, Get/SetSetting, MarkPaired(id,"local","127.0.0.1") | consistent |
| T5 ca ↔ T7 | IssueAgentCert(csrPEM,id), AgentListenerTLS, AgentClientTLS, ServerIDFromCert, NewAgentKeyAndCSR | consistent |
| T6 hub ↔ T7 | NewWSConn(ws,onEvent), Wait(), Attach/Detach | consistent |
| T6 hub ↔ T10 | Listener iface, NewLocalConn(reg,onEvent), Call | consistent |
| T7 agentapi.Deps ↔ T10/T11 | PairHandler, WSHandler, InstallScriptHandler(d,publicURL), BinaryHandler(d,binDir); Deps.AgentAddr func() string | consistent |
| T8 auth ↔ T9 web | Sessions, RateLimiter, RequireLogin, cookie helpers | consistent |
| T9 web.Broker ↔ T10 | Publish("server_status"/"server_probe") | consistent |
| T1 main ↔ T7/T10 | agent.Main / panel.Main (args, stdout, stderr) int | consistent |
| T9 routes vs T10 mounting /agent/* | T9 router is behind RequireLogin; /agent/pair,/agent/install,/agent/bin must be unauthenticated | Ruling: panel.go builds top-level mux: /agent/ → agentapi handlers (no auth), everything else → web.NewRouter. Cost if wrong: small refactor in T10. |
| Each task self-consistency | tests vs code vs files | T1–T12 agree; no plan-mandated rubric defects found |

## Model plan
T1,T2,T3,T4,T5,T8,T12: sonnet implementer, sonnet reviewer. T6,T7,T9,T10,T11: opus implementer, opus reviewer. Final review: opus (most capable available).

## Progress
Task 1: complete (commits 2f314f4..42fc96c, review clean; minors not applied)
Task 2: complete (commits 42fc96c..a6c3a5f, review clean; minor: Disks nil→null — carry to T3 implementer: initialise Disks as empty slice)
Task 3: review approved with minors. Ruling: plan-mandated systemctl rule ("active|inactive only") conflicts with spec 7.5 (panel must show MySQL service health, a crashed service must not vanish) — `failed`, `activating`, `deactivating` now count as service exists (ServiceActive=false). Cost if wrong: none material. Dispatched as fix round 1 together with two cheap minors (errors.As for ParamError; port parsing test with non-default port).
Task 3: minor (deferred): recovered panic not logged with stack; data-dir disk entry keyed by path not mount (duplicate entry when same fs); LookPath("mysqld") relies on PATH (RHEL /usr/libexec/mysqld); no per-command timeout in Collect (dispatcher should apply deadline — carry to T7); syscall.Statfs has no build tag (linux-only target, acceptable).
Task 3: fix round 1/5 (3 addressed, 0 open; commits f3e3549..0d46a30)
Task 3: complete (commits a6c3a5f..0d46a30, review clean after round 1)
Task 4: Ruling: Go floor raised 1.24 → 1.26.0 because modernc.org/sqlite v1.61.0 (pinned by plan) requires go 1.26; toolchain auto-download handles it; plan Global Constraints and spec 9.1 updated. Cost if wrong: build hosts need Go 1.26 (GOTOOLCHAIN=auto fetches it). Concern 2 (UNIQUE detection by error text) left to the task reviewer.
Task 4: review approved with minors. Fix round 1 dispatched for three cheap minors that downstream tasks touch: column-scoped UNIQUE detection via helper; CreatePairToken on missing server → ErrNotFound; ExpiresAt truncated to seconds before return.
Task 4: minor (deferred): shared helpers execOne/rowScanner live in servers.go (move to store.go); migration applied-check outside tx (startup race across processes); DSN path not URL-escaped; tests discard some setup errors; no concurrent-consume test.
Task 4: fix round 1/5 (3 addressed, 0 open; commits 06e3661..1981cec)
Task 4: complete (commits 0d46a30..1981cec, review clean after round 1)
Task 5: review approved (security probes passed: no-cert, other-CA, expired, non-ClientAuth all rejected). Fix round 1 dispatched for cheap minors: serverID<=0 rejected; partial file removed on write failure; exported sentinel ErrNotAgent.
Task 5: minor (deferred): no permission check on existing ca.key / dir on load; loaded CA not validated (IsCA, expiry); CSR key type not constrained to P-256; leaf NotAfter may exceed CA NotAfter; coverage gaps (expired cert, non-ClientAuth, CSR with SANs, only-key branch, mismatched pair); rejection assertions only err != nil; ca.go 321 lines (agent helpers could move to ca/agent.go).
Task 5: fix round 1/5 (3 addressed, 0 open; commits 0922126..9280bbe)
Task 5: complete (commits 1981cec..9280bbe, review clean after round 1)
Task 6: implementer kept Co-Authored-By: Claude Opus 5.5 on its own commits (the model that wrote them) — accepted, honest attribution; Attach made non-blocking before review.
Task 6: review Needs fixes — Important: (1) caller ctx passed to ws.Write tears down the shared conn when cancelled (reproduced 9/20); (2) OnOnline/OnOffline can interleave out of order across reconnect. Fix round 1 dispatched to original implementer, plus two cheap minors (fail pendings before close handshake; reuse offlineError()).
Task 6: minor (deferred): writeMu ignores ctx; onEvent runs on read goroutine (doc note needed — carry to T7 dispatch); no out-of-order multi-Call test; no late-response-after-timeout test; no read-limit test; proto.Error.Error() drops Code; call plumbing helpers could live in call.go.
Task 6: fix round 1/5 (4 addressed, 0 open; commits 6759ab1..a5d7b75)
Task 6: minor (deferred): Wait() may close just before readLoop exits on explicit Close (one trailing event possible) — T7 handler must tolerate; lifeMu is hub-wide so slow listener callbacks block all Attach/Detach — T10 listener must keep callbacks short (do probe in goroutine); no mid-write ctx-expiry test.
Task 6: complete (commits 9280bbe..a5d7b75, review clean after round 1)
Task 7: review Needs fixes — Important: ServeAgents returns before hijacked WS handlers finish (Detach/OnOffline may run against closed store). Fix round 1 dispatched + cheap minors: validate CSR before consuming token and fixed error message; stop waiting for request goroutines once ctx cancelled (reconnect not blocked by a slow command); widen TestWSRejectsDeletedServer margin.
Task 7: minor (deferred): config dir mode not tightened if pre-existing; re-pair failure can leave new key beside old cert; no tests for malformed CSR / backoff cancel / backoff reset; ping/close bypass writeMu (safe, comment). Plan-level: pairing over plain HTTP exposes CA swap on-path (spec 9.x accepted LAN HTTP; revisit when HTTPS UI lands in plan 6) — for final review.
Task 7: fix round 1/5 (4 addressed, 0 open; commits d025079..5111237)
Task 7: minor (deferred): ServeAgents doc overstates — early-return on ServeTLS error skips handler wait.
Task 7: complete (commits a5d7b75..5111237, review clean after round 1)
Task 8: complete (commits 5111237..c6216e7, review clean)
Task 8: minor (deferred): no bcrypt cost assertion; Allow sweeps whole map each call; RandomPassword sampling untested. Carry to T9: login handler must validate next (single leading "/", not "//" or "/\"), key limiter by RemoteAddr host only, compare against dummy hash when user unknown.
Task 9: implementer DONE; carry to T10: UI http.Server needs BaseContext cancelled on shutdown and no WriteTimeout (SSE); CSRF compares Origin/Referer to r.Host (reverse proxy caveat — document); deleting a server does not close its live agent conn (hub lacks detach-by-id) — T10 should close via hub or accept until next reconnect; default English 404 for unknown paths; local server deletion policy.
Task 9: review Needs fixes — Important: open redirect via dot-segments + backslash (`/./\evil` → Location `/\evil`). Fix round 1 dispatched + cheap minors: duplicate-name 400 test; broker unsubscribe assertion; cross-origin test for login/logout/create/token; "Service" → "Layanan".
Task 9: minor (deferred): store error classification by errors.Unwrap (typed validation error would be better); SSE flush failure silent & Publish logs to slog.Default; plain-text 403/404/409 pages and English default 404; delete-modal wording vs lingering hub conn; EventSource per tab (HTTP/1.1 6-conn limit); static dir listing; no download checksums in scripts.
Task 9: fix round 1/5 (5 addressed, 0 open; commits e9c075d..2b6afd0)
Task 9: complete (commits c6216e7..2b6afd0, review clean after round 1)
Task 10: review approved with minors. Ruling: public_host precedence flag > saved setting > auto-detect accepted; must be documented in README (T11). Fix round 1 dispatched for cheap minors: reset-password uses --config-dir to remove stale admin-initial-password; ensureLocalServer adopts an existing row on name collision instead of failing forever; probeLoop test with short interval + broker publish assertions.
Task 10: minor (deferred): no srv.Close() fallback after Shutdown deadline (SIGTERM may exit 1); UI server lacks IdleTimeout/ReadTimeout; deleting local server while running leaves hub attached until restart; Main arg convention differs from agent.Main; servers.go name ambiguous (http.go); reset-password cannot revoke live sessions in the running process (docs).
Task 10: fix round 1/5 (3 addressed, 0 open; commits aa2ad2a..cc1cd5d). Round 2 dispatched: adoption guard (!Paired || CertSerial==local), fallback name <hostname>-panel.
Task 10: fix round 2/5 (1 addressed, 0 open; commits cc1cd5d..ef6f7d8)
Task 10: complete (commits 2b6afd0..ef6f7d8, review clean after round 2)
Task 11: implementer DONE; concern: UI pairing command uses curl -sSL without -f (fix in T11 round if reviewer agrees, it is one line in web/handlers_servers.go + README).
Task 11: review Needs fixes — Important: (1) agent installer can print success when daemon-reload fails (`a && b` under set -e) and never checks is-active; (2) arch-404 message hidden by curl -f. Fix round 1 dispatched + minors: curl -fsSL in UI pairing command/README/template header (cross-task one-liner, ruled in-scope: it is the command this task's script is fetched by); settle delay before is-active on re-run; atomic copy in copy_agent_binaries; README cross-arch example marked as needing `make release` (T12).
Task 11: minor (deferred): PanelURL unescaped in template (admin-controlled; validate public host later); token visible in argv (plan-mandated); unchecked w.Write; set -x guard misses `set -o xtrace`; hostname -I may differ from PublicURL IP; README raw URL assumes default branch `main` — confirm before release or ship install.sh as release asset; shellcheck not run.
Task 11: fix round 1/5 (6 addressed, 0 open; commits a5a72be..efc1918)
Task 11: minor (deferred → carried to T12 since it edits install.sh): settle sleep equals RestartSec=3 (use 5); no ELF check on downloaded binary before mv; shell-test `&&` guard may flag legit `if systemctl … &&`.
Task 11: complete (commits ef6f7d8..efc1918, review clean after round 1)
Task 12: complete (commits efc1918..2fba6b1, review clean)
Task 12: minor (deferred): ELF check depends on `cmp` (diffutils; use od/hex compare to be dependency-free); .new left on ELF rejection; README DBPANEL_BIN example hard-codes amd64.
All tasks complete. Final whole-branch review dispatched (base 2f314f4 = plan commit).
Final review: With fixes. C1 panel-side liveness missing (Review Focus 3 unmet on partition); I1 SIGTERM exits 1 with unresponsive agent; I2 delete does not disconnect agent; I3 no re-pair path for paired server; I4 mysqld path on RHEL; I5 online install stores only own arch; I6 plain-HTTP pairing exposure.
Ruling: I6 (plain-HTTP bootstrap exposure) — accepted for Plan 1 per spec 7.1/7.2 (LAN, HTTP); hash-pinned pairing command + binary sha256 + `--ca-sha256` becomes the FIRST task of Plan 2 before any MySQL command is registered. Cost if wrong: an on-path LAN attacker at install time can gain root on a DB server; mitigated by scheduling, not by code, until Plan 2.
Ruling: fix wave scope = C1, I1, I2, I3, I4, I5, M5, M6, M7, M8, M9, M11, cmp dependency, ServeAgents doc, README raw URL, plus restart/delete/partition tests. Deferred to Plan 2 (ledger): M1 package boundaries (proto wire types, internal/pki), M2 single event path, M3 unified offline error, M4 serve/shutdown dedupe + servers.go rename, M10 migration downgrade guard, M12 SSE expired-session/probe-card refresh, reviewer's PLAN 2 triage list.
Final fix wave: all findings addressed (commits 2fba6b1..82fe728), re-review clean. Residual minors ledgered for Plan 2: orphan reconnect window on delete; re-pair card lacks EventSource reload and old live agent not disconnected until it reconnects; panel's own binary unverified in online install (double download); agent stop behind dead panel ≤10 s; agent-install writes unit before pair refuses on panel host.
Security audit (user-requested) dispatched on HEAD 82fe728; report to docs/superpowers/security/2026-10-30-audit-plan-1.md.
Security audit result: Medium overall, SEC-01 High (Go 1.26.0 stdlib CVEs). Ruling: security fix wave now covers SEC-01,02,03,04,05,07(verify panel binary; signing deferred),08,09,10,12,13,14,16,18,20 + unit hardening from SEC-06 where compatible with root agent. Deferred to Plan 2 with rulings: SEC-06 full (non-root panel needs design), SEC-11 (basic caps now, rate limits Plan 2), SEC-15 (change-password page is spec §8 Pengaturan, Plan 2), SEC-17 audit log, SEC-19 (ms window), SEC-21, SEC-22 (blast-radius design), SEC-23/24/25 accepted.
Security re-review: open — SEC-02 via UI command (token in curl argv), R1 regression (unbounded limiter keyed by raw username), SEC-05 tuning (username 20/15m, count after bcrypt, cap 4 → min(4,max(1,NumCPU/2))), SEC-10 slow-body on non-form routes, SEC-07 other-arch binaries must be listed in SHA256SUMS. Ruling: Referrer-Policy same-origin kept (verified in Chromium: no-referrer → Origin: null breaks CSRF check). Final security round dispatched for these five; SEC-03 residual race = SEC-19 class (Plan 2); SEC-06/11 partial as planned.
Security round 2: all addressed; posture fit for LAN pilot. Deferred to Plan 2 (ledger): `echo … | curl -K -` POSIX form for pairing command; IPv6 /64 keying for IP limiter (full-map refusal of new clients, Low); GOMAXPROCS(0) instead of NumCPU; flaky TestWSLogsReplacedConnection (log before Attach returns).
