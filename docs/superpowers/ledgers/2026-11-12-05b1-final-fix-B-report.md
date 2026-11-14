# Final fix wave, part B — report

BASE 7482857. Commits 0b11893..472fd35 (21 commits, list at the end).

## P-1 (REQUIRED) — 0b11893 (+ releaseRole part in 6570fd9)
- RebuildApprovalUsable: an answer that is not required, or is approved, must carry no code (any non-empty code -> unusable); a request still needs a six-ASCII-digit code.
- localApprovalErr shows a code only for Required && !Approved && validApprovalCode; else P12b.
- Agent text on these paths: the only agent text left that reached a job error/step/log was the mysql.releaseRole refusal (callWithin); it now goes through monitor.AgentText inside AgentQuote (6570fd9). Codes are only ever the validated six digits; names are the panel's.
- Tests: unusableApprovals gains "not required with code", "not required with digits", "approved with code", "approved with hostile code"; TestHostileApprovalCodeNeverReachesJob (two variants) checks job error, every step message, the job log and the replica's last error. RED: `go test ./internal/panel/orchestrator -run 'TestHostile…|TestRebuildApprovalUnusableAnswers'` -> "approved with code: usable"; job error contained "approve 000000\n\nCATATAN PANEL … curl … | sh". GREEN after the fix.

## P-2 — 6570fd9
- orchestrator.InvalidReason words the reason from InvalidCode with the exact sentences; unknown code, or empty code with Invalid set -> "alasannya tidak dikenali panel ini"; nothing when both empty. The handler clears Invalid/InvalidCode before the page sees them; the template shows the panel's reason (rebuildInfo.InvalidReason).
- orchestrator.AgentQuote(server, text) = "Pesan dari agent <server> (bukan dari panel): " + AgentText(text, 300). Used for: bad_params refusal of mysql.rebuildApproval on the form (separate <blockquote> after the panel's own line; in that branch there is no command box), shownError (agent *proto.Error on the replica form flash and on the job page for Ulangi/Setujui/Batalkan; shownError now takes the server name), and the mysql.releaseRole refusal in a job (agentQuoteError wraps the agent error, so errors.Is still matches; fail prefixes the step title).
- Tests: TestRebuildFormWordsInvalidReason (each code, "" and unknown -> fallback; the auditor's forged sentence never rendered), TestAgentRefusalLabelled (form POST and job retry), TestRebuildFormShowsApprovalBox updated (old free-text reason now the fallback; bad_params case shows panel line + label + agent text), TestCallWithinLocalApprovalMapping updated (label; newline/U+202E removed, cut). RED: body lacked "…tidak berlaku lagi: alasannya tidak dikenali panel ini." etc.

## P-3 — 62c89e1
validApprovalSource: host must ParseIP or be an RFC 1123 name (ASCII letters/digits/hyphen, labels 1–63, total ≤ 253), port 1–65535. Tests: bracket form (request and approved), spaces, newline, U+202E, port 0/65536, 64-char label, 254-char name, empty label, underscore; usable 63-char label and port 65535/1. RED: "source with spaces: usable", "approved bracket form: usable". Web case "approved with a forged source".

## P-8 — ce6efa0
Request with RemainingSec ≤ 0 is unusable (in RebuildApprovalUsable, so form, start and mid-job all treat it as unreadable). Handler clamps RemainingSec to [0, 31 days] before minutes/hours. Tests: MaxInt64 approved -> "744 jam 0 menit", MaxInt64 code -> "44640 menit", -1 -> unreadable; orchestrator cases 0 and -60. RED shown for all.

## F-9 (a) — 53d64aa
The "rebuild approval answer not usable" warning carries answer=code=%q source=%q addr=%q remaining_sec invalid_code=%q invalid=%q, each cut to 100 bytes. TestUnusableApprovalLogged (one line, quoted, cut). RED: fields missing.

## P-4 — ad9230b
orchestrator.ApprovalSeen{Read, Result}; CreateReplicaParams.Approval and JobAck.Approval (pointers) receive the answer requireRebuildApproval decided on. Handler: localApprovalDetail(seen, detail) (no agent read; Read false -> local_approval:false). Only a rebuild passes the pointer on create. Tests: TestRebuildDecisionHandedBack (orchestrator), TestRebuildAuditHasLocalApproval now changes the agent answer right after the decision and asserts the audit shows the decision's values and that the handler did no read of its own. RED: audit showed local_approval:false.

## P-7 — 90acca5
rebuildApprovalForJob returns GetJob's error, and for a create_replica job with unparsable params refuses with "parameter pekerjaan ini tidak bisa dibaca, jadi persetujuan bangun ulang di server tidak bisa diperiksa; pekerjaan ini tidak bisa diulang atau disetujui". TestRetryRefusesUnreadableCreateReplicaParams (retry and approve; job stays failed; only mysql.connections reached the agent). RED: retry answered a JSON error from later code.

## P-6 — c0993d9
askApproval(r): any non-GET/HEAD; GET/HEAD only when Sec-Fetch-Site is absent, same-origin or none. Otherwise rebuildInfo.ApprovalNotAsked: the box shows only "Klik Periksa lagi untuk melihat persetujuan di server." and the Periksa lagi link; button disabled (ApprovalOK false). TestRebuildFormAsksAgentOnlySameOrigin (5 header values). RED: agent asked once for same-site/cross-site. (rebuildTarget still reads mysql.connections on a cross-site GET — the item covers the approval call only.)

## P-9 — 54892a6
Permissions-Policy as given (clipboard-write not listed), Cross-Origin-Embedder-Policy: require-corp, CSP + "worker-src 'none'; manifest-src 'self'". Header tests updated (RED: old values). Verified: TestE2EBrowserConfirm (real Chromium via playwright, `-tags e2e_browser`) passes with the new headers; the copy button uses navigator.clipboard.writeText (needs clipboard-write, left allowed); no browser-less test exercises it. Nothing left out.

## F-2 (a) — f8bc6d5
Verified in the agent: Pins.Bootstrap with hint "" hits `default: return nil` (pins nothing) and never changes an existing pin (settled/role file seen); the hint is used only for Bootstrap (monitor.LastRoleHint). Panel maps RoleQuarantined to "" like unassigned. TestConfigureQuarantinedSendsNoHint (RED: RoleHint "quarantined"). Intervals applied on the agent side are part A's TestConfigureAcceptsQuarantinedHint.

## P-10 — d28c1a5
TestStartStateOlderIDLowerSeqIgnored: L2 open then checked/closed; a Seq-1 report with L1 opens nothing, mark stays L2. Passes on current code; proven to guard: with Seq 1000 the same report opens a second alert.

## P-5 — e7fd95a (+ f7bab82 gofmt of the test file, which I committed unformatted)
Per server, per kind token bucket in serverState.startOpens: burst 5 (startOpenBurst), one per 10 min (startOpenRefill); checked just before reconcileStart opens (step 7); when empty: no open, no mark, Warn logged at most once per refill period. First alert of a kind always finds a full bucket. TestStartAlertOpensRateLimited: 100 alternating bypassed reports -> 5 alerts; a late start still opens at once; held-back bypass opens after the refill and is then marked. RED (limit disabled): 50 alerts.

## F-7 panel — c96a3eb
deleteWarnings(…, lockHolds) — D1 only when the page's latest report has StartLock.Locked; otherwise D1b exactly (no report, no lock state, not locked). Tests: TestDeleteQuarantinedDialogSaysLockStays (4 cases), TestDeleteWarningsQuarantinedWithoutBox. RED: compile.

## F-8 — 514cae2
StartTightenTLS runs refuseBusy before leaving a quarantined replica out. TestTightenTLSWaitsForQuarantinedReplicaJob (RED: StartTightenTLS = <nil>).
Retry question, checked from the code: a tighten job cannot be retried with a quarantined server in it. Its own server (the master, step 0) becomes quarantined only through a promote job, which is created on that same server (createJob(kind, p.MasterServerID…)), so supersededBy refuses the retry ("server ini sudah punya pekerjaan yang lebih baru"). A replica in TightenReplicas can only become quarantined after first becoming a master, i.e. after a promote job on the tighten job's server — same refusal. A quarantined rebuild target's replica row is creating/failed (create_replica sets ReplicaFailed on any failure), and tightenSkip skips any row not ready/attention. So no change to refuseQuarantinedTLS.

## F-7 CLI (addendum 1) — 401b0b3
Verified first: GuardTick runs guardQuarantined only for role quarantined; for other roles only guardStartLock (keeps the lock until read-only proved persisted, then releases; never sets read-only); master-lock handling only for master; mysql.setReadOnly{on:false} allowed on unassigned. The new sentence is true. setRoleNextUnassigned = the given text; printed when role is unassigned and (was quarantined or the pins were unreadable), with or without --ya. Tests: TestSetRoleOnQuarantinedAsks (exact text), TestSetRoleUnreadablePinsUnassignedSaysNext (prompt and --ya; replica->unassigned says nothing). RED: old text / nothing printed.

## Addendum 2 — e2e must not touch this machine — acd6da1, 3dda523
- agent.RunCLIWith(args, in, stdout, stderr, CLISeams{LayoutHost, Syslog}); RunCLI = RunCLIWith with zero seams; nil fields resolve to the package's cliLayoutHost / cliSyslog at call time (in-package tests that swap those still work). The seams are threaded as parameters (mainSetRole(…, layout), cliReleaseStartLock(…, layout), mainApprove(…, writeSyslog), approvalSyslog(…, writeSyslog)); no exported mutable variable, no env/flag/file. Main unchanged. TestRunCLIWithSeams (package vars fail the test if used). RED: compile.
- FINDING contradicting part A: `set-role unassigned` never touched the machine's layout. cliReleaseStartLock runs only for `role == "master"` (true in every version: efeee3f, 2149666, 57da63f, HEAD). /etc/dbpanel-config.lock (2026-11-13 07:07) matches the install test that ran set-role master before 57da63f (07:24, "install test no longer runs set-role master"). Left in place as instructed.
- e2e set-role unassigned (TestE2EDeleteQuarantinedOldMasterStaysLocked): expected from the code: the CLI only rewrites the role pin, uses no layout and no syslog, and leaves the test instance's init_file line and lock file as they are. The agent is stopped first (its guard would otherwise release the lock once read-only is persisted, racing the assertion). Asserted: layout calls 0, no syslog line, line and file both present, C12 printed. Passed (58 s).
- e2e approve (approveRebuild): RunCLIWith with the node's layout and a recording writer; asserted exactly one line starting "rebuild approval written: source=", not containing the code, no layout use. TestE2EPromotePlanned passed (115 s).
- Checked for other machine paths under internal/panel: e2e agents build mysql.Driver with PinDir, SeenPath, SalvageDir in the agent dir (start-result and seen-ports derive from SeenPath; layout is the test instance's host); localpair_test runs `agent.Main pair … --dir <tmp>` (writes only its dir); no panel test uses RegisterMySQL, config.DefaultDir, the agent's syslog or /var/lib/dbpanel. Panel defaults /var/lib/dbpanel/panel and /etc/dbpanel/panel: panel CLI tests pass --data-dir/--config-dir; the one reset-password test without --config-dir fails before the os.Remove (unknown user). probe.RealSystem reads /etc/os-release (read only).

## README — e51ca87
T12-1/2/3/4 (manual release rewritten: stop agent; MySQL running, `SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;` = 1 1 and persisted_variables both ON — the agent's own readOnlyProvedNow/persistedReadOnlyOn rule — else `SET PERSIST read_only=ON; SET PERSIST super_read_only=ON;` and check again; MySQL down -> start it, lock file missing -> rewrite the three documented lines; find the line with `mysqld --verbose --help | grep -A1 '^Default options are read'` and `grep -rn 'dbpanel-start-lock.sql' /etc/my.cnf /etc/my.cnf.d /etc/mysql /usr/etc/my.cnf` (layout.go: ConfigFile = first existing of mysqld's default list, managed file in its last !includedir or the BEGIN/END block); then line, file, pins; step 7 conditional on step 2 and quoting setRoleNextMaster's condition; `sudo mysql` / `mysql -u root -p`), T12-5 ("data MySQL"), T12-6 ("Mode ketat (strict mode)", "dibangun ulang (rebuild…" in Peran section and Persetujuan section).
Part A / own changes: Hubungkan MySQL locality text and repair; mysql.connect grant refusal on quarantined; install refused and dropUser limited under an approval (roleRefusal text); F-4 rebuildApproval with unreadable pins; Perketat busy; C16; C12 (both places) and unassigned no longer guarded; A-3 two runs, bounds, reasons (with the "konfigurasi efektif MySQL tidak bisa dibaca: " prefix the code adds), 20 entries/300 bytes; start-state change reported at the next check (replaces the summary-report sentence) and the P-5 limit; D1/D1b in both delete bullets and the reuse section; approve: always resolves, screen line A-5, B16, B15 and the set-source way out (verified: same host:port keeps the code, other host:port gives a new code), approved address rule, syslog line (facility auth, tag dbpanel-agent, fields, no code, warning text) and the agent's Info line; panel-worded reasons; unreadable answers; agent-text label; P12b; unreadable params; audit fields; what an approval allows; A6 and the listing-failure text and what the operator does; P-6 text; security section sentence.
Statements I could not verify and so did not write: where exactly MySQLError of a stored remote account appears in the UI (wrote "laporan pemantauan server itu membawanya"); per-distribution MySQL service names (given as examples).

## F-9 (d) — 472fd35 (last)
Pure move of startResultSlack consts … closeStartAlertsLeftQuarantine (incl. the P-5 bucket) into internal/panel/monitor/start_state.go; diff of removed vs added lines identical apart from a trailing blank line. promote.go 1084 -> 773 lines.

## Gates (one at a time, on 472fd35)
0. `go test ./internal/agent -race -short -timeout 60m` — exit 0, 70 s
1. `go test ./internal/panel/... -race -short -timeout 60m` — exit 0, 171 s
2. `make test` — exit 0, 688 s (15 packages from Go's test cache: unchanged since an earlier run on this machine; vet and install_test.sh ran)
3. `make test-e2e` — exit 0, 623 s
Also: gofmt -l clean, go vet ./... clean, graphify update . run; TestE2EBrowserConfirm (e2e_browser tag) passed with the new headers.

## Existing tests changed
- TestRebuildFormShowsApprovalBox: the free-text Invalid reason is no longer shown (now the fallback); bad_params case shows the label block; new cases.
- TestCallWithinLocalApprovalMapping: releaseRole refusal now labelled.
- TestRebuildAuditHasLocalApproval: audit comes from the decision, no second read.
- TestDeleteQuarantinedDialogSaysLockStays / TestDeleteWarningsQuarantinedWithoutBox: D1 only with a held lock, D1b otherwise.
- baseHeaders and the layout CSP check: new header values.
- TestSetRoleOnQuarantinedAsks: exact new C12.
- e2e approveRebuild and the set-role unassigned step: RunCLIWith with seams, plus assertions.
- fakeOrch: fills ApprovalSeen like the orchestrator.

## Operator-facing strings
Given verbatim: InvalidReason sentences and fallback (orchestrator/approval.go); label "Pesan dari agent %s (bukan dari panel):" (approval.go AgentQuote); "Klik Periksa lagi untuk melihat persetujuan di server." (replica_new.html); D1b (web/handlers_oldmaster.go); C12 (agent/cli.go).
My wording: "Persetujuan di server tidak bisa diperiksa: agent %s menolak permintaannya." (handlers_oldmaster.go, bad_params); "parameter pekerjaan ini tidak bisa dibaca, jadi persetujuan bangun ulang di server tidak bisa diperiksa; pekerjaan ini tidak bisa diulang atau disetujui" (approval.go, P-7). Log lines (English): "start alert not opened: over the rate limit; …", "create replica job params not readable", answer field on "rebuild approval answer not usable".

## Choices the brief does not fix
- P-2: the label is applied in shownError to every agent *proto.Error shown on the replica form and job page (not only rebuild ones); other agent errors stored in ordinary job failures are unchanged (outside "these paths").
- P-2: InvalidReason also when InvalidCode is set but Invalid empty (code's sentence / fallback).
- P-8 lives in RebuildApprovalUsable (all paths treat a spent request as unreadable); the clamp in the handler.
- P-4 via pointer out-fields on the params/ack structs rather than new return values (keeps the web Orchestrator interface).
- P-5 bucket in memory (reset on panel restart) per serverState; log once per refill period.
- P-6: HEAD treated like GET.
- F-7 CLI: C12 also printed with --ya on unreadable pins.
- Addendum 2: e2e stops the old master's agent before set-role unassigned to make the lock assertion deterministic.

## Self-review / concerns
- Part A's F-3 claim about set-role unassigned was wrong (see above); the seam is still in place and asserts nothing is reached.
- One commit (e7fd95a) went in with an unformatted test file; fixed in f7bab82 (no amend).
