# Final fix wave: Plan 4c (after final-review.md)

Commits (not pushed):
- 0a31ef8 fix(panel): redaction templates span the message; promote paths censored; limiter decodes outside the lock; TLS wait on the runner clock
- 48b8c09 fix(audit): journald copy carries ts and hash, no username hash; 0009 names a missing trigger; audit page wording; README

## SEC-74 redaction off-template
Change (internal/panel/redact/redact.go): `template` searches each follower with LastIndex over the rest of the whole
message (not the line). New templates: `near '` (ER_PARSE_ERROR) and `Truncated incorrect <WORD> value: '` (a `lead`
field: "Truncated incorrect " then one word before " value: "), both censored to the end of the message. A template
whose follower is missing (message cut inside the value, e.g. Last_SQL_Error at 512 bytes) now censors to the end of
the message instead of falling back to the quote heuristic. Templates also match a value quoted with `"` (follower
with the same quote) so that Quotes stays a fixed point (Quotes writes every literal as '…').
Tests (redact_test.go): the four review inputs added to quoteCases (parse error with newline and NIK, both nested in
Last_SQL_Error and bare; `Truncated incorrect DOUBLE value: 'Jl. Merdeka No' 5 RT 3'`; `Duplicate entry 'Budi\nO' Santoso'
for key 'nama'`; cut `Error 'Duplicate entry 'O' Brien`), exact expected output. TestRedactQuotesFixedPoint now also
asserts no fragment (Budi, 3201234567890123, " 5 RT 3", Santoso, Brien, "AND nik") survives any input cut at any byte.
One expectation changed: `Duplicate entry 'Budi Santoso for key` now gives `Duplicate entry '…'` (was `'…` without the
closing quote).

## SEC-75 promote paths censored
Change: `jobRun.setStep` (runner.go) censors every step message (redact.Quotes, then the cap); `jobRun.setReplicaState`
(create_replica.go) censors last_error; promote.go: `markQuarantine`/`markReplica` censor the promotion detail, the
repoint `set` closure censors lastErr before replicas.last_error and the detail, `o.ioErr` is censored, the quarantine
and repoint error texts are built from `logErr(err)` (censored before the cap); runner.go createJob's "pekerjaan tidak
bisa dimulai" message uses logErr.
Test: orchestrator TestPromoteAgentErrorsCensored: quarantine and repoint fail with agent errors quoting 'Budi', a
third replica's receiver IO error quotes 'Budi'; no "Budi" in job_steps messages, jobs.error, job log,
replicas.last_error (both replicas) or promotions.replicas_json, while the censored texts are present. Fails on the
pre-fix code (checked by stashing the source changes).

## SEC-76 limiter
Change (hub.go): a health payload over 64 KiB (`maxHealthPayload`, the monitor's maxPayload) is dropped before any
decode and counted in Dropped, logged with reason "payload over 64 KiB" (rate drops log reason "over the rate limit").
With the bucket empty, the lock is released, the backlog mark decoded outside rateMu (`backlogOf`), then the lock is
retaken: a token that came back is used first, else the allowance. Drop counting/logging moved to `drop`.
Tests: TestHubDropsOversizeReportUnread (oversize dropped with tokens left, not decoded, counted, reason logged; one
at exactly 64 KiB passes), TestHubDecodesBacklogOutsideLock (decode called 5 times after the 10 tokens, never with
rateMu held, via TryLock).

## SEC-77 journald anchor
Change: store.AddAuditRecord returns {ID, TS (stored text), Hash}; AddAudit wraps it. store.LogAudit writes the
journald line with stored, id, ts (stored text), hash and the fields. web auditAs and agentapi auditPairRefused both
use it, so agent_pair refusals now have a journald copy.
Tests: TestAuditInsertLogged checks each line's ts and hash equal the row's (read from SQLite); pair_test asserts four
`msg=audit … hash=<64 hex> username=agent … action=agent_pair` lines for the four refusals.

## SEC-78 typed-username hash
Change: journalUsername strips the hash from "(tidak dikenal) <hash>" for the journald audit copy and for the
"login failed" log line (handlers_auth.go also logged it). The DB row keeps the hash.
Test: TestAuditInsertLogged asserts the hash of the typed name appears in no log line.

## SEC-79 README
New Keamanan bullet "Reverse proxy di depan panel tidak didukung" (loopback pairing check, per-IP login limit and
audit IPs rely on the real remote address).

## SEC-80 audit page
Change (web/templates/audit.html): the broken-chain alert says `sudo -u dbpanel dbpanel panel audit-verify`; the green
"Rantai utuh" line now carries "Pemeriksaan ini tidak dapat mendeteksi penulisan ulang seluruh catatan atau
penghapusan baris terakhir; bandingkan dengan salinan di journald (journalctl -u dbpanel-panel)."
Test: TestAuditPageShowsChainStatus asserts both texts. scripts/tailwind.sh run; web/static/app.css unchanged.

## C1 README
"+60 detik" in the lock-refusal bullet; audit-verify exit codes 0/1/2 (2 = bad arguments); the Sensor bullet in
Keamanan and the "Sensor kutipan data" paragraph now list what is censored (stored in panel.db and sent to Telegram:
reports, job messages and log incl. promote/repoint steps, replica last error, promotion details, agent-derived audit
outcomes), the whole-message templates and to-end-of-message cases, and the heuristic limits (unquoted data; off-template
values holding a quote then a space or punctuation). Also documented: the journald copy carries ts and hash and covers
agent_pair; the username hash stays in panel.db only.

## C3 tests
TestAddAuditWaitsForOtherWriter: the 200 ms sleep is replaced by `auditLockHook` stages in AddAuditRecord ("begin"
before BEGIN IMMEDIATE, "locked" after it). The test waits for "begin" while the other connection holds the write lock,
checks "locked" has not happened, commits, and asserts "locked" saw the commit (atomic flag). awaitVerifiedLink's
deadline uses jr.r.now(); the pause is a stopped time.NewTimer of pollEvery (at most 1 s). Test
TestAwaitVerifiedLinkUsesRunnerClock: verifyIOWait = 1 h, the runner clock advances 20 min per mysql.status poll; the
Perketat job fails after 3-4 polls (the old code would wait the wall-clock hour).
Note: the poll pause is still wall time (pollEvery), only the deadline is on the runner's clock; the runner has no
sleep abstraction, and adding one is beyond the ruling.

## C4 migration 0009
Change (audit_chain.go): before dropping, each append-only trigger is looked up in sqlite_master; a missing one fails
the migration with "audit trigger <name> is missing: it was removed outside the panel, so the audit log may have been
tampered with; compare audit_log with the journald copy (journalctl -u dbpanel-panel) before restoring the trigger".
Test: TestMigrate0009MissingTriggerSaysTampered (error names audit_log_no_delete and says tampered; schema stays at 8).

## Not done
C2 (spec §8) was not in this wave's rulings; left as is.

## Commands
```
$ go test ./internal/panel/... -race -short
ok  internal/panel 83.230s; agentapi 17.085s; auth, ca (cached); hub 1.549s; monitor 36.716s; notify 14.972s;
ok  orchestrator 64.098s; redact 1.045s; store 28.972s; web 55.165s
$ go test ./internal/agent/... -short -timeout 60m   (one run, in the background)
ok  internal/agent 10.558s; commands 0.007s; config 0.030s; drivers/mysql 1683.926s; drivers/mysql/testmysql 14.185s;
ok  jobs 0.017s; monitor 0.052s; probe 0.003s   (exit 0)
$ go vet ./...            -> clean
$ gofmt -l (tracked .go)  -> empty
$ bash scripts/tailwind.sh -> Tailwind v4.1.14 + daisyUI v5.1.29 -> web/static/app.css (no diff)
$ go test ./internal/panel/store -race -run TestAddAuditWaitsForOtherWriter -count=5 -> ok
$ graphify update .       -> graph updated (graphify-out is untracked)
```

## Concerns
- The commit trailers carry the model name, as the brief gave them verbatim; this conflicts with "no model names
  anywhere". No model name appears in code, README or this report otherwise.
- The new to-end-of-message cases (cut template values, `near '`, Truncated) censor more diagnostic text than before:
  anything after such a value in the same message is lost. Fail-closed by design.
- The whole-message LastIndex censors from the first value to the last follower when a message holds two such values.
- C2 (spec §8) is untouched.
