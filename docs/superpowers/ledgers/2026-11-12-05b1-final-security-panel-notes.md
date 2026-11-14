# Panel-side audit of plan 5b-1 — controller's notes of the auditor's report (head 8ade0e0)

Findings P-1..P-8 with evidence and required fixes: see final-fix-findings.md ("From the panel security audit"). Severity: P-1 Medium (required), P-2 Medium, P-3..P-6 Low, P-7/P-8 Informational. No Critical or High.

## Verdict (auditor)
Panel side mostly sound: html/template escapes every new sink; strict CSP holds and the new template needs nothing it forbids; Start, Retry and Approve of a rebuild are checked against the agent server-side, not only by the disabled button; the six-digit check protects the command on the form. One real hole in the central asset: the check was not applied on the second path that builds the "sudo dbpanel agent approve <code>" sentence (a job step refused with needs_local_approval, `localApprovalErr`). Merging acceptable after a small fix wave.

## Security headers (all set in SecurityHeaders, internal/panel/web/router.go:272-293, wrapping the whole mux at :258; plain-HTTP helper serves the same handler, servers.go:136)
| Header | Current | Assessment | Recommended |
|---|---|---|---|
| Content-Security-Policy | default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self' (router.go:264-266, 275) | Strong: no unsafe-*, no inline handlers or styles in replica_new.html; remaining DOM sinks are innerHTML with same-origin server-rendered fragments (app.js:713,783) | Keep; optionally worker-src 'none'; manifest-src 'self'; require-trusted-types-for 'script' only after checking Alpine CSP |
| Strict-Transport-Security | max-age=31536000 on TLS requests only (288-290) | Adequate; no protection for users reaching the panel by IP or without a trusted CA (R-008) | same (includeSubDomains only on a dedicated DNS name) |
| X-Content-Type-Options | nosniff (276) | Good | same |
| X-Frame-Options | DENY (277) | Good | same |
| Referrer-Policy | same-origin (282) | Good | same |
| Permissions-Policy | camera=(), microphone=(), geolocation=() (283) | Too narrow | add payment, usb, serial, hid, bluetooth, display-capture, clipboard-read, interest-cohort (keep clipboard-write for the copy button) |
| Cross-Origin-Opener-Policy | same-origin (284) | Good | same |
| Cross-Origin-Resource-Policy | same-origin (285) | Good | same |
| Cross-Origin-Embedder-Policy | not set | Missing (low value; all assets same-origin) | require-corp |
| Cache-Control | no-store on every page incl. approval form, job pages, fragments (286); public, max-age=3600 on /static/ (188, 423-428); no-cache on SSE (sse.go:77) | Good | same |
| Server | removed (287) | Good | same |
| X-XSS-Protection | not set | Fine (deprecated) | optionally 0 |
| Session cookie | __Host-dbpanel_session, Path=/, HttpOnly, Secure, SameSite=Lax, browser-session; server-side sliding TTL + absolute lifetime (auth/middleware.go:16,55-64; auth/session.go:17-32) | Good; Lax lets cross-site GETs carry the session (P-6, R-128) | keep Lax (Telegram links); gate agent-calling GETs on Sec-Fetch-Site |
| CSRF on POST | sameOrigin checks Origin/Referer, rejects a request with neither (handlers_auth.go:162+; router.go:184-185) + confirmation tokens on destructive actions | Good | same |

## Checked and found sound (auditor)
- Output encoding: every new value rendered by html/template in text or quoted-attribute context (replica_new.html:4-35,109); only template.HTMLAttr is a constant (render.go:99-104); no template.HTML; JS writes SSE job/step text with textContent (app.js:139,232); Telegram plain text without parse_mode (telegram.go:68); job_failed alert carries no agent text (alerts.go:774-784).
- Command on the form: code exactly six ASCII digits, checked in orchestrator and handler (approval.go:149-159; handlers_oldmaster.go:748-751); not-approved branch shows no Source; a code typed on the wrong server fails (bound to that server's request file).
- No cross-server display: Hub.Call routes by server id, replies matched per connection (hub/conn_ws.go:92,196); "Periksa lagi" link uses only panel integer ids (replica_new.html:32); rebuildTarget checks the server is quarantined AND the old master of this master's topology (handlers_oldmaster.go:836-842). No IDOR.
- Server-side enforcement: Start (create_replica.go:101-105), Retry and Approve (runner.go:785,842) require the agent's approval whatever the button shows; POST still needs the confirmation token and typed name (handlers_mysql.go:647,700); Retry/Approve keep requireConfirm for data-dropping jobs (handlers_jobs.go:464-470); ApprovalOK set only in the success branch (handlers_oldmaster.go:752-759).
- Delete of a quarantined server sends no release (controls.go:173-181), warning text panel-owned.
- TLS jobs: new job, retry and daily renewal skip quarantined servers (tls.go:63-65,90-93,~795).
- Alert integrity: each server's reports affect only its own alerts and marks; reconciliation requires the panel's own role record to be quarantined (promote.go:806-809); ids capped at 64 bytes, entries at 16, kinds at 3; ages clamped before multiplication; stale reports ignored by sequence number.
- Store and migration: 0012 additive with ON DELETE CASCADE; foreign_keys(1) set (store.go:47); server ids AUTOINCREMENT, never reused (0001_init.sql:2); a binary older than the schema is refused (migrate.go:17); all queries use placeholders.
- Limits: hub frames 1 MiB (conn_ws.go:15); page agent calls 5 s (handlers_oldmaster.go:65); orchestrator reads 30 s.
- Logging: structured slog; the code is not logged, only the decision (approval.go:56,77); agent errors pass through redact.Quotes.
- Failure handling: store errors give a generic 500 (router.go:418-421); offline, outdated and unreadable agents each have panel wording.
- Dependencies: none added; no build flag or external asset.

## Residual risks (auditor)
- A hostile agent can always hide or forge the start alerts of its own server (lying in snapshots, toggling over, flipping its pin). Inherent (R-026); spec section 8 accepts the agent as the authority on its own server.
- Agent sentences are still shown, length-limited and escaped (SP-11): a broken-into database server can show the operator persuasive but false advice. P-2's fix narrows this where it matters most.
- Start alerts can lag a report interval (~120 s on a quarantined server) until T12-8 is fixed.
- Panel record not quarantined but agent pin quarantined: deleting does not tell the operator the server stays quarantined (deferred, Task 10).
- No user roles: every panel login is a full administrator (R-042, R-073).

## Risk inventory impact (auditor, panel side)
R-109 narrowed (delete no longer sends releaseRole to a quarantined server); R-019 narrowed (rebuild of the old master needs root's local approval, checked and shown by the panel); R-116 narrowed (start alerts follow the agent's durable state in every snapshot); R-125 partly closed (local approval and its age audited; see P-4); R-026 unchanged (inherent; P-5 adds a churn path); R-057 slightly worse (more audit/alert rows; P-5); R-128 gains a member (P-6, ?bangun-ulang=); R-127/SP-11 unchanged (new agent-text sinks P-2, P-3; one unbounded P-1); R-008 unchanged. No regression against a finding recorded as fixed, apart from a2782b9's intent not reaching localApprovalErr (P-1).

## Unconfirmed (auditor)
- Whether the agent refuses mid-job when its approval names a source other than the new master the panel uses — controller: the agent's Pins.Allow refuses rebuild commands that name a source other than the pinned one (Task 3 test: dumpFrom to another host → "server ini dipin mengikuti sumber …"), and CheckRebuildApproval ties the approval to the pinned source; to be confirmed by the agent-side audit.
- Whether a backlog report replayed after a reconnect can briefly reopen a blocked alert for an older id — settled by a test in the fix wave (P-10).
