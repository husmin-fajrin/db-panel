### Finding Verdicts

How I read it: I read `final-fix-B-code.diff` in five passes (lines 1-600, 600-1160, 1160-1540, 1540-1990, 1990-2944). For code outside the diff I read the committed version with `git show 472fd35:<path>`. I ran no tests. Line numbers are at 472fd35 unless they say "diff:".

**P-1 (REQUIRED): the approval code on the mid-job path; unusable answers; agent text into job errors** — ADDRESSED.
- `RebuildApprovalUsable` (orchestrator/approval.go, diff:1052-1071) rejects any non-empty code when the answer is not required or is approved. A request needs a code of exactly six ASCII digits (`validApprovalCode`) and time left.
- `localApprovalErr` (diff:1020) shows a code only for `Required && !Approved && validApprovalCode`; otherwise it shows P12b.
- On these paths the only agent text that still reached a job was the `mysql.releaseRole` refusal. It now goes through `AgentQuote`, which applies `monitor.AgentText` (control, format and bidi characters become spaces; cut to 300) (runner.go `callWithin`, diff:1707-1710).
- Attack walked:
  - I answered `mysql.rebuildApproval` with `{"required":false,"code":"000000\n\nCATATAN PANEL …"}`, and also with `required:true, approved:true` plus a code: both are unusable, `RebuildApproval` returns `errApprovalUnreadable`, and P12b is shown.
  - The code is only ever printed after the digit check. `fail()` stores it in the step message, the job log, `jobs.error`, the replica's last error and the alert (runner.go:1538-1572).
  - `TestHostileApprovalCodeNeverReachesJob` checks all four stores for both variants.
- Not covered by this item (see Residual Risks): any *other* agent error in a job still reaches these stores raw.

**P-2 (panel half): panel-worded reasons, labelled agent quotations** — ADDRESSED.
- `InvalidReason` (approval.go:266-273) maps the eight codes to the given sentences, character for character (I checked each string with grep against the findings file). An unknown code, or an empty code next to a non-empty `Invalid`, gives "alasannya tidak dikenali panel ini".
- The handler then clears `Invalid` and `InvalidCode` (handlers_oldmaster.go:793). The template prints only `$rb.InvalidReason` inside P5 (replica_new.html:28). Nothing in panel code or templates reads `.Invalid` any more (git grep).
- The label "Pesan dari agent %s (bukan dari panel): " is used in three places:
  - the `bad_params` refusal on the form: a separate `<blockquote>` after the panel's own line, in a branch with no command box;
  - `shownError` (flash on the replica form and on the job page);
  - the `releaseRole` refusal in a job (title-prefixed by `fail`, still one line).
- Attack walked: `invalid` set to the auditor's forged sentence with any `invalid_code` is never rendered (`TestRebuildFormWordsInvalidReason`). A `bad_params` text with newlines or U+202E becomes one cut line after the label.

**P-3: source host must be an IP or RFC 1123 name, port 1-65535** — ADDRESSED.
- `validApprovalSource` and `validHostName` (approval.go:206-237) are in place. The bracket form, spaces, newline, U+202E, an empty label, a 64-character label, a 254-character name and ports 0 and 65536 are all unusable (unusableApprovals). This holds for both request and approved answers, since the source is checked before `Approved`.
- **But the rule is stricter than what the agent and the panel accept for this same value.** See N-1.

**P-4: the audit records the approval the decision used** — ADDRESSED.
- `requireRebuildApproval(ctx, id, seen *ApprovalSeen)` fills `seen` with exactly the answer it decided on (diff:941-945). `CreateReplicaParams.Approval` and `JobAck.Approval` carry it out.
- `localApprovalDetail(seen, detail)` no longer calls the agent (handlers_oldmaster.go, diff:2222-2231). `jobAction` uses it only when `seen != nil` (cancel passes nil).
- The test changes the agent's answer right after the decision and asserts the audit shows the decision's values and that the handler made no extra read.
- Calls are synchronous in the request goroutine, so there is no race on `seen`.

**P-5: per-server, per-kind limit on start-alert opens** — ADDRESSED.
- `allowStartOpen` (start_state.go; diff:816-841) is a bucket of 5, refilled one per 10 minutes, checked just before `raiseOpenLocked`.
- When the bucket is empty: no open, no mark, and one Warn per refill period.
- The bucket is created full per kind, so the first alert of a kind always opens. It lives in `serverState`, which is deleted only when the server is gone from the store (monitor.go:306, :612). A hostile agent cannot reset it by reconnecting. A panel restart does reset it (by design).
- `TestStartAlertOpensRateLimited`: 100 alternating reports open 5 alerts, a late start still opens at once, and a held-back start opens after the refill.

**P-6: the form GET asks the agent only for same-origin requests** — ADDRESSED.
- `askApproval` (handlers_oldmaster.go, diff:2129-2138): any POST asks; GET or HEAD asks only when `Sec-Fetch-Site` is absent, `same-origin` or `none`.
- Otherwise `ApprovalNotAsked` is set: the box shows only the exact text and the Periksa lagi link, and the button stays disabled (`ApprovalOK` is false).
- `rebuildTarget` is the only GET path that reaches `rebuildApproval` (git grep: handlers_mysql.go:567, :746, the latter on POST).
- Attack walked: a logged-in browser following a cross-site link to `/masters/2/replicas/new?bangun-ulang=1` makes no `mysql.rebuildApproval` call. The read-only `mysql.connections` read still happens (Residual Risks).

**P-7: an unreadable create_replica job is refused, not skipped** — ADDRESSED.
- `rebuildApprovalForJob` (diff:975-1005) returns `GetJob`'s error. For a failed, partial or waiting create_replica job whose params do not parse, it returns a refusal before `startMu`.
- Nothing is changed: the job stays failed (`TestRetryRefusesUnreadableCreateReplicaParams`, retry and approve).

**P-8: RemainingSec clamped; a request with no time left is "cannot be read"** — ADDRESSED.
- `res.RemainingSec = min(max(res.RemainingSec, 0), maxRemainingSec)` in the handler (diff:2182) comes before `ApprovalMinutes` and `ApprovalHours`. With the clamp there is no overflow: MaxInt64 gives "744 jam 0 menit" and "44640 menit".
- A request with RemainingSec <= 0 is unusable in `RebuildApprovalUsable`, so the form, the start and the mid-job path all treat it as unreadable.

**P-9: security headers** — ADDRESSED. See the section below.
- clipboard-write is not listed, so the copy button keeps its default `self` allowlist.
- What the browser test covers: `TestE2EBrowserConfirm` (tag e2e_browser) logs in, opens dialogs, and runs Alpine (CSP build) and form posts in Chromium under the new headers. It fails only on JS exceptions (`pageerror`).
  - It does not check CSP or COEP console violations, blocked fonts or styles, or the copy button.
  - Every subresource is same-origin, so COEP `require-corp` together with CORP `same-origin` does not block anything.
  - The screenshot tour (`make screens`) was not reported as run.

**P-10: monitor test for a lower-Seq report with an older id** — ADDRESSED. `TestStartStateOlderIDLowerSeqIgnored` (alerts_test.go, diff:670-687) asserts no new alert and that the mark stays L2.

**F-2 (a): send "" as role hint for a quarantined server** — ADDRESSED.
- monitor.go `configure` (diff:714-721) maps `RoleQuarantined` to "".
- Verified in the agent: `Pins.Bootstrap` (pins.go:634-670) returns nil on the `default` branch for "", and on `settled` it never rewrites an existing pin.
- The hint is used only through `LastRoleHint` to reach `Bootstrap` (agent monitor.go:542). Older agents accept "".
- Test: `TestConfigureQuarantinedSendsNoHint`.

**F-7 (panel D1/D1b)** — ADDRESSED.
- `deleteWarnings(..., lockHolds)` (diff:2062-2092) gives D1 only when `p.StartLock != nil && p.StartLock.Locked`, and D1b otherwise. D1b is verbatim.
- `p.StartLock` is filled from the monitor view before `oldMasterViews` runs (handlers_servers.go:742-751), so the order is correct.
- All three call sites pass it: no report, no lock state and not locked all give D1b (4-case test).

**F-7 (CLI part, addendum section 1)** — ADDRESSED.
- `setRoleNextUnassigned` is exactly the ruled text (cli.go, diff:125).
- It is printed when `role == "unassigned" && (was == "quarantined" || readErr != nil)` (diff:193), with or without `--ya`.
- Tests: `TestSetRoleOnQuarantinedAsks` (exact text) and `TestSetRoleUnreadablePinsUnassignedSaysNext`. The latter also checks that replica -> unassigned says nothing.

**F-8: busy check before the quarantine skip; retried tighten job** — ADDRESSED.
- In `StartTightenTLS` (tls.go, diff:1736-1746), `refuseBusy` now runs before the quarantined `continue`. Test: `TestTightenTLSWaitsForQuarantinedReplicaJob`.
- The retry question is answered from the code, and I checked the reasoning:
  - `supersededBy` refuses through `NewerJobExists(j.ID, j.ServerID)` (runner.go:935-940).
  - The only way a server in a tighten job becomes quarantined is a promote job on the tighten job's own server.
  - At run time `tightenSkip` (tls.go:560-586) skips any row that is not ready or attention.
  - `fail()` sets `ReplicaFailed` for every failure of a seeding job (runner.go:1563).

**F-9 (a): log the rejected fields** — ADDRESSED.
- `rejectedFields` (diff:1032-1037) logs each field with `%q`, which escapes newlines and bidi characters, and cuts each to 100 bytes. It goes on the one Warn line, and the code is quoted rather than executed or shown.
- Test: `TestUnusableApprovalLogged`.

**F-9 (d): move the start-state reconciliation out of promote.go** — ADDRESSED.
- 472fd35 touches only promote.go (-311) and start_state.go (+324).
- The removed lines and the added body are identical: `diff` of the two line sets shows only the new file's 13-line header (package, imports, a 3-line comment) and one trailing blank line. No import of promote.go was removed.

**Addendum section 2: panel e2e must not touch this machine; the `RunCLIWith` seam** — ADDRESSED.
- **Production unchanged.**
  - `Main` -> `RunCLI` -> `RunCLIWith(..., CLISeams{})`, and nil fields resolve to the package's `cliLayoutHost` and `cliSyslog` (cli.go, diff:84-92).
  - Seams are passed as parameters, not as exported mutable variables. There is no env, flag or file.
  - The only non-test caller is `cmd/dbpanel/main.go:35` -> `agent.Main`. Nothing an operator or attacker can set redirects the root CLI.
- **Who was right about part A's claim: the second fixer.**
  - At the base 7482857, `cliReleaseStartLock` runs only inside `if role == "master"` (diff:171-178, unchanged context lines).
  - `set-role unassigned` only rewrites the role pin under `--dir`. It never reached the machine's layout, so part A's statement was wrong.
- **The two e2e steps.**
  - The `set-role unassigned` step stops the old master's agent first, then asserts four things: layout calls 0, no syslog line, the instance's init_file line and lock file both still present, and the C12 text printed (e2e_replica_test.go, diff:568-594). That matches what the code does.
  - The `approve` step uses a recording writer and asserts exactly one line with prefix "rebuild approval written: source=", that the line does not contain the code, and layout calls 0 (e2e_helpers_test.go, diff:494-504).
- **Other panel tests.**
  - My git grep found no other `agent.RunCLI` or `agent.Main` call except `localpair_test.go:89` (`pair --dir <tmp>`).
  - The e2e `mysql.Driver` sets `PinDir`, `SeenPath` and `SalvageDir` in the agent dir, and start-result derives from `SeenPath` (start_result.go:126).
  - No `/etc/dbpanel`, `/var/lib/dbpanel`, `NewRealHost` or syslog use under internal/panel tests. The only exception is the install-script stub, which rewrites and allowlists those paths and runs as nobody.

### New Breakage in the Fix Diff

**N-1 (Important): the P-3 host rule rejects source hosts that the agent and the panel themselves accept, so a legitimate rebuild can be blocked for good.**
- Where: internal/panel/orchestrator/approval.go:206-237 (`validApprovalSource` / `validHostName`). The rule allows ASCII letters, digits and hyphen only, every label 1-63 characters, and no empty label.
- What else accepts the value:
  - The agent's own source rule is `sourceHostRe = ^[A-Za-z0-9_.:-]{1,255}$` (agent drivers/mysql/snapshot.go:26, used by `ParseSource`/`validSourceHost`, pins.go:368-389). It accepts `_`, a trailing dot (`db.lan.`) and labels over 63 characters.
  - The panel's own `hostRe` (web/handlers_mysql.go:110) has the same character set. It is used by `setMasterSource` (handlers_mysql.go:422), which sets `topo.SourceHost`. The quarantine pins that value on the old master (orchestrator/promote.go:1364 `sendQuarantine(..., topo.SourceHost, topo.SourcePort ...)`).
- Scenario:
  1. The admin sets the master's replication address to `db_master`, or `srv-db.rs.local.` (a name in /etc/hosts or the hospital DNS; glibc resolves underscores). The panel accepts it.
  2. A promote happens, and the old master is pinned with source `db_master:3306`.
  3. Every `mysql.rebuildApproval` answer is now "unusable": the request with its code, and the approval after root runs `approve`. The form says only "Persetujuan di server tidak bisa diperiksa sekarang; klik Periksa lagi." Start says P10b ("kodenya tidak bisa dibaca dari agent … klik Periksa lagi"), and a mid-job refusal says P12b. Every one of them loops back to Periksa lagi, which fails the same way.
  4. The test `"source underscore"` in `unusableApprovals` (diff:1346) makes this behaviour the expected one.
- Cost: the old master cannot be rebuilt through the panel, and nothing on screen says why. Only the panel log's F-9(a) line shows the source.
- The operator's way out is to change the master's source address to a stricter name or an IP. They then also have to run `set-source` on the old master, but nothing on screen tells them either step.
- Fix: accept what the agent and the panel accept for this value. Keep the injection protection: no space, no control or format characters, no brackets or colon outside an IP. Either add `_` and a single trailing dot to `validHostName` and relax the 63 limit to the agent's 255 total, or reuse the panel's `validHost` rule. Change the "source underscore" case into a usable one.

**N-2 (Minor): a "not required" answer may carry `approved:true` and source/address fields and is still usable.**
- Where: approval.go:186-195 (`RebuildApprovalUsable` returns true for `!Required` once the code is empty).
- Effect:
  - `ApprovalSeen` records it, and the audit then says `local_approval: true` (localApprovalDetail) for an agent that reported no quarantine.
  - Nothing of it is shown: the template's `not .Required` branch prints only the server name.
  - P-1 (a)'s principle ("rejects any answer that carries a field its state does not use") is applied to the code only. Cost: a misleading audit field when an agent is broken or hostile.

**N-3 (Minor): a refusal before the decision is audited as `local_approval: false`.**
- Where: handlers_mysql.go:742-744 and `localApprovalDetail`. `Read == false` when `StartCreateReplica` refused before it asked the agent, for example `ErrConnectionsRemain`, or a Retry of a job that is not failed.
- Effect: the audit says `false`, which is the same value as "read, not approved". Before the fix, the handler read and recorded the real state. This follows the item's letter, but "not read" and "not approved" now look the same in the audit; a separate key (for example `local_approval_read`) would tell them apart.

**N-4 (Minor): the browser test does not cover the header change.**
- Where: router.go:2361 (diff), and the test's coverage.
- Detail:
  - `interest-cohort` is an unrecognised feature in current Chromium. It logs a console error on every page; it is harmless, but noise in a security console.
  - `TestE2EBrowserConfirm` fails only on JS exceptions, so blocked CSP/COEP subresources or a broken copy button would not fail it.
- Cost: no breakage found on paper (all resources same-origin). The claim "browser test passes with the new headers" proves less than it sounds.

### Security headers

All are set on every UI and agent-bootstrap response in `SecurityHeaders` (internal/panel/web/router.go, at 472fd35):

| Header | Value | Assessment |
|---|---|---|
| Content-Security-Policy | `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; worker-src 'none'; manifest-src 'self'` | Strict: no inline script or style and no eval. `data:` is for images only. `worker-src 'none'` and `manifest-src 'self'` were added as required. |
| X-Content-Type-Options | `nosniff` | Correct. |
| X-Frame-Options | `DENY` | Correct; duplicates frame-ancestors for older browsers. |
| Referrer-Policy | `same-origin` | Correct; the reason it is not no-referrer is documented (Origin: null on form posts). |
| Permissions-Policy | `camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), hid=(), bluetooth=(), display-capture=(), clipboard-read=(), interest-cohort=()` | As required. clipboard-write is left at its default (`self`) for the copy button. `interest-cohort` is obsolete (console warning only). |
| Cross-Origin-Opener-Policy | `same-origin` | Correct. |
| Cross-Origin-Resource-Policy | `same-origin` | Correct; consistent with COEP. |
| Cross-Origin-Embedder-Policy | `require-corp` | New. All subresources are same-origin, so nothing is blocked. Browsers ignore it on plain-HTTP (non-secure) pages. |
| Cache-Control | `no-store` (static assets and SSE override it) | Correct for pages with tokens. |
| Strict-Transport-Security | `max-age=31536000` (only when `r.TLS != nil`) | Correct; no includeSubDomains (appropriate for an appliance). |
| Server | deleted | Fine. |

### Residual Risks

- Agent errors other than needs_local_approval and `releaseRole` still reach `jobs.error`, step messages, the job log, the replica's last error and alerts raw. Only `redact.Quotes` and a 1 KiB cut are applied (runner.go:1538-1572). job_detail.html renders them with `whitespace-pre-line`. A hostile agent can therefore still put a multi-line, panel-looking instruction (with bidi marks) into any job error by failing any step with its own text. P-1 (c) was scoped to "these paths", so this was not required of this round.
- An approved or requested source is a hostname of the agent's choosing. It is shown in "untuk sumber X". The character set now limits it to hostname shapes, but it is still agent-chosen words.
- P-5's limit is kept in memory: a panel restart refills every bucket. The limit is 5 opens per kind, then about 144 a day per kind per server.
- P-6 relies on `Sec-Fetch-Site`: a browser that sends none is treated as same-origin. A cross-site GET still makes the panel call the read-only `mysql.connections` on the old master.
- The audit outcome of a job action refused by an agent `*proto.Error` stores the agent's raw text, made one-line but unlabelled and with bidi characters intact (`outcomeOf`/`oneLine`, web/audit.go:118-133). The page shows the labelled quote; the audit does not.
- An approved answer with RemainingSec <= 0 is still treated as approved by the panel. The agent refuses at the first protected step, and P12 follows.
- The label names the job's server (`job.ServerID`) for any `*proto.Error` returned by Retry or Approve. This is correct today, since every synchronous agent call there is on that server, but it is not enforced.

### Out-of-Scope Observations

- (Pre-existing, Important for the audit document) The general job-error path described in the first residual risk: `fail()` has no `AgentText` and no label. It is the same forging technique P-1 described, through a different error code. I recommend a follow-up: quote every `*proto.Error` in `fail()` with `AgentQuote`, and drop `whitespace-pre-line` for agent-origin text.
- (Pre-existing, Minor) `oneLine` in web/audit.go removes only CR and LF. Other control and bidi characters in audit outcomes reach /audit unchanged.
- (Pre-existing, Minor) A replica whose agent was set to `quarantined` locally (panel role still `replica`) gets the TLS steps of a tighten job. Its `needs_local_approval` refusal becomes P12, which sends the operator to a rebuild form that the panel will not offer for a `replica` row.

### Verdict

**Fix round:** Findings remain open — every finding in scope is ADDRESSED (P-1 through P-10, F-2 (a), F-7 panel and CLI, F-8, F-9 (a) and (d), addendum section 2), but the fix introduces one Important breakage that must be fixed before merge: **N-1**, the P-3 host rule rejecting source names with `_`, a trailing dot or labels over 63 characters. The agent and the panel's own `setMasterSource` accept these names, so a legitimate rebuild is blocked for good with no reason shown. Minor: N-2, N-3, N-4.
