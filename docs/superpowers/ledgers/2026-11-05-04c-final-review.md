# Final review + security audit: Plan 4c (05f431a..dd45c41)

## Summary verdict
The branch does what the spec asks in §5.12–5.14. Migration 0009 is atomic: the triggers are dropped, the old rows backfilled and the triggers recreated in one transaction, and a crash rolls back to v8 with the triggers in place. AddAudit uses BEGIN IMMEDIATE, so two writers cannot fork the chain. The README is honest about the unkeyed chain. Lock-aware refusals now cover every panel job that makes a replica writable (UnlockMaster only touches a master). The agent lock order is promoteMu → replUserMu → pins.mu, with no inversion. The new routes stay behind authed/authedPost with the global headers (CSP, XFO DENY, nosniff, HSTS, no-store). The fragment check runs before any agent call. Settings are bounded in the form, in Validate and in LoadSettings, and retention never touches audit_log or job_log. tls_verified only drives a warning on the page, so a lying agent gains nothing new.

I ran redact.Quotes over constructed MySQL texts (a scratch copy, not the repo). It censors every templated case: escaped values, '' and \', 512-byte cuts, multi-line Query, hex/bit literals, 'x'@'host' accounts and contractions. It leaks off-template (SEC-74). Some promote-path agent texts reach storage uncensored (SEC-75). Neither is critical.

## Security findings
**SEC-74 (Minor; fix before merge, cheap): redaction leaks off-template.** internal/panel/redact/redact.go:93-112 (templates are line-limited; ER_PARSE_ERROR "near '…'" and "Truncated incorrect … value: '…'" have no template). Constructed leaks:
- `Error 'You have an error … near 'nama = 'Budi'\n AND nik = 3201234567890123' at line 1' on query…` gives `Error '…'\n AND nik = 3201234567890123'…'`
- `Truncated incorrect DOUBLE value: 'Jl. Merdeka No' 5 RT 3'` leaks ` 5 RT 3`
- `Duplicate entry 'Budi\nO' Santoso' for key …` leaks ` Santoso`
- a cut `Error 'Duplicate entry 'O' Brien…` leaks ` Brien`

Trigger: a value holding a quote followed by a non-word character or a newline, or a DDL/SBR parse error. Fix: search each template's follower in the whole message (LastIndex over s, not the line), and add {"near ", "' at line "} and {"Truncated incorrect ", end of message}. Add these cases to the fixed-point test.

**SEC-75 (Minor; fix before merge): agent error text stored uncensored on promote paths.** The paths are orchestrator/promote.go:711 (step message via setStep, runner.go:1184), :1126 (markQuarantine detail), :1147-1156/:1186 (replicas.last_error and promotion detail incl. o.ioErr), and runner.go:290. Today these carry IO or quarantine errors, which seldom quote rows. The README still says job messages are censored. Fix: censor centrally in setStep, in SetReplicaState callers and in markReplica/markQuarantine.

**SEC-76 (Minor; recommended): the limiter parses under a global lock.** hub/hub.go:249/277: isBacklog unmarshals the whole payload (up to 1 MiB) under the hub-wide rateMu for every report once a server's bucket is empty. A flooding, compromised agent therefore holds the admission of every other server behind JSON scans (Review Focus 1). Fix: drop a health payload over 64 KiB (monitor maxPayload) before the decode, and decode outside rateMu.

**SEC-77 (Minor; recommended): the journald copy cannot anchor the chain.** web/audit.go:102 logs no `hash` and logs ts at second precision. agentapi/pair.go:202 (agent_pair refusals) bypasses the copy entirely. A full rewrite or tail truncation can then only be found by comparing row by row, which a non-DBA will not do, and the missing agent_pair lines read as "inserted rows". Fix: log the stored ts string and the hash; route auditPairRefused through the same copy. Optional: `audit-verify --head <hash>`.

**SEC-78 (Minor; may stay, record it): short hash of a typed password in journald.** web/audit.go:268: an unknown username is logged as an unsalted 32-bit SHA-256 prefix, and it is now also in journald, which the adm and systemd-journal groups can read. A password typed into the username field can be confirmed with a dictionary. Fix: an HMAC with a key that stays on the panel host, or no hash in the journald copy.

**SEC-79 (Minor; README line): a reverse proxy defeats the loopback check.** isLoopback and clientIP trust RemoteAddr. A reverse proxy on the panel host would make every request loopback: the local pairing token would be accepted from anywhere, the per-IP login limit would collapse, and audit IPs would all be 127.0.0.1. IPv4-mapped loopback is handled correctly. Fix: the README should say that a proxy in front of the panel is unsupported.

**SEC-80 (Minor; fix before merge, text only): audit page wording.**
- web/templates/audit.html:11 tells the operator to run `dbpanel panel audit-verify`, but root is refused. It should say `sudo -u dbpanel dbpanel panel audit-verify`.
- The green "Rantai utuh" carries no caveat. Add: it does not detect a rewrite or the removal of the last rows; compare with journald.

## Correctness findings
**C1 (Minor; fix before merge): README nits.** Three from the ledger:
- The "Penolakan selama kunci promosi" bullet lacks "+60 detik".
- Exit code 2 of audit-verify (bad usage) is not documented.
- The Keamanan "Sensor" bullet says "sebelum disimpan, ditampilkan, atau dikirim". Narrow it after SEC-75, and add the SEC-74 limit if any remains.

**C2 (Minor): spec §8 not updated.** It lacks the 4c residuals: the unkeyed chain and journald as the real anchor, journald readers, unquoted or heuristic redaction limits, and agent-asserted tls_verified. The README has them; the spec should match.

**C3 (Minor, tests).** store/audit_chain_test.go uses `time.Sleep(200ms) // AddAudit is now waiting`, which does not prove it waited. awaitVerifiedLink uses time.Now/time.After rather than r.now. Neither weakens a fixture.

**C4 (Minor).** backfillAuditChain's `DROP TRIGGER` fails when a trigger was already removed (tampering). The panel then won't start, and the operator sees only "apply migration 0009". Failing closed is fine; add a sentence that says why.

## Deferred-minor triage (ledger)
- **Must fix before merge:** the Task 5 README nits (C1).
- **May stay:** cap/paginate unbounded replies (documented ~10k limit); first prune after upgrade; AddAudit inside a store tx (no caller); case-sensitive Query anchor; crafted `' on query`; the duplicated retryable-errno list; the transient tls_verified flip; "Menunggu" master → emergency (documented); backlog before Attach; old rows not rewritten (4a/4b never ran on real data).

## Residual risks to present to the owner
1. Anyone who can write panel.db (root, the dbpanel user) can rewrite the audit chain undetected. Only a journald copy shipped to another host defends against that (needs rsyslog/journal-remote).
2. Redaction is a heuristic. Unquoted data (trigger SIGNAL text, parse-error tails) and the agent journal or MySQL error log on DB servers keep the raw text.
3. The promote-lock refusals depend on the panel's record. `unlock-master` run by hand is unknown until Ulangi.
4. tls_verified and AgentVersion are agent-asserted; a compromised replica agent can hide its own warning.
5. Retention can be set to 1 day by the admin (audited), which erases the alert and event history.
6. On a verification failure the panel reverts to an unverified source. An active MITM can force that, and the warning stays visible (pre-existing).

## Declined to judge
Pin columns in servers (§5.13 lists them; the plan scoped 5.13 to audit_log and settings). The revert-to-unverified design (4a).

Final verdict: mergeable after fixes (SEC-74, SEC-75, SEC-80, C1; SEC-76/77 recommended in the same wave).

## Re-review of the fix wave (dd45c41..48b8c09)

Re-ran all 31 scratch inputs against redact.go at 48b8c09: no leak remains. All 4 leaks from the review are now fully censored:
- the parse error, bare and nested in Last_SQL_Error
- `Truncated incorrect DOUBLE value: '… No' 5 RT 3'`
- `Duplicate entry 'Budi\nO' Santoso'`
- the cut `Error 'Duplicate entry 'O' Brien`

The new extra cases are censored too: `Incorrect DATE value: 'Jl. X' 5 RT'`, `Truncated incorrect INTEGER value: 'Budi' 3201'`, the double-quoted variant, and a multi-line value nested in Error. Censoring is a fixed point (Quotes(Quotes(s)) == Quotes(s)) on every input. The only leak left is in made-up text that matches no MySQL message (`later 'Y' 3201'`); it is the documented limit of the quote heuristic.

**Cut-off values.** A template censors to the end of the message only when its closing text is missing from the whole message. In the usual single-value case (`Could not execute Write_rows …; Duplicate entry 'A' for key 'p.PRIMARY', Error_code: 1062; handler error …; … end_log_pos 1234`), Error_code, the handler error and the binlog position are kept. The key name shows as `'…'`, exactly as at dd45c41: the general quote rule censors it, no template swallows it, so this is not a regression. Neither version ever showed the key name, and the README example says `for key '…'`. Panel advice is never swallowed: alert advice is added after censoring, logf censors only its arguments, and I found no panel step message with quoted text that the new censoring in setStep could hit.

**Test hooks.** `auditLockHook` (store/audit.go) defaults to a no-op function. `backlogOf = isBacklog` (hub.go) is the same function. Only tests replace either, so production behaviour is unchanged.

Verdicts:
- **SEC-74 ADDRESSED.** Templates span the whole message, with new `near` and `Truncated incorrect` templates. The new cases are in the redaction tests, and the fixed-point test now checks no fragment survives a cut at any byte.
- **SEC-75 ADDRESSED.** Censoring is in setStep, setReplicaState, the repoint `set` closure, markQuarantine/markReplica, and runner.go:290 via logErr. TestPromoteAgentErrorsCensored fails on the old code.
- **SEC-76 ADDRESSED.** A payload over 64 KiB is dropped before decoding. The backlog mark is decoded with rateMu released, then the lock is retaken and a token is tried again. If Disconnect runs in that gap, a fresh bucket is created: harmless.
- **SEC-77 ADDRESSED.** store.LogAudit logs the stored ts text and the hash. agent_pair refusals now go through it, and the tests compare each logged ts and hash with the row.
- **SEC-78 ADDRESSED.** journalUsername strips the hash from the audit copy and from the "login failed" log line. The database row keeps it.
- **SEC-79 ADDRESSED.** New README Keamanan bullet: a reverse proxy in front of the panel is unsupported.
- **SEC-80 ADDRESSED.** The audit page says `sudo -u dbpanel …`, and the "Rantai utuh" line now carries the caveat.
- **C1 ADDRESSED.** README has "+60 detik", exit codes 0/1/2, and the narrowed Sensor text with its limits.
- **C3 ADDRESSED.** The 200 ms sleep is replaced by the lock-hook stages. awaitVerifiedLink's deadline is on the runner clock; the pause between polls is still real time, at most 1 s, which is acceptable.
- **C4 ADDRESSED.** Migration 0009 now checks sqlite_master and names the missing trigger, with advice.

No new Critical or Important issue in this diff.

Final verdict: mergeable
