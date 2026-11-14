# Final fix wave, part D — security re-review of the code changes (0b1c760..6799dc3)

Read as committed (`git show 6799dc3:<path>`), with `final-fix-D-code.diff`, the brief, addenda 1-3 and the report. Nothing run. Graph queried first (`graphify query`) per the repo rule.

### Finding Verdicts

**D-4 / N-1 — ADDRESSED.**
- One definition: `orchestrator.ValidHost` + `hostRe` (`internal/panel/orchestrator/approval.go:289`, `:295`), used by `validApprovalSource` (`approval.go:274`) and by both former web call sites (`internal/panel/web/handlers_mysql.go` connect form and `setMasterSource`). The web copy is gone.
- What it accepts: `^[A-Za-z0-9_.:-]{1,255}$`, and a colon only when `net.ParseIP` parses the host as an IP (so IPv6 only). That covers underscore, trailing dot, empty labels (`db..lan`), labels over 63 characters, and lone `.`, `-` or `_`. Brackets are allowed only when they enclose an IP. `[db_baru]:3306` and `[db-baru.lan]:3306` are refused. `[10.0.0.1]:3306` is accepted, which is harmless: the agent never produces it.
- What it refuses: space, tab, newline, every non-ASCII code point (so U+200F, U+202E and all bidi or format marks), raw IDN (punycode `xn--` passes), and `%` (IPv6 zone). The set of characters that can reach a page, log or audit grew only by `_`, plus length up to 255. All of these are inert in HTML, JSON and log lines. The page also bounds the value with `monitor.AgentText`.
- Agreement with the agent: the agent's `sourceHostRe` (`internal/agent/drivers/mysql/snapshot.go:26`) plus its colon/IP rule (`pins.go:385`, the rule for the pinned source) is the same regex and the same colon rule. No host string is accepted by one and refused by the other.
  - Outside the approval, the agent's `replication.go:251` and `snapshot.go:115` drop the colon rule, so they accept `a:b`, which the panel refuses. Those values flow panel→agent only, so the panel is the stricter side. That is fine.
- Tests: the underscore, trailing-dot, 64-character-label and 255-character cases are now usable. U+200F, tab and the bracketed-name cases are unusable. The existing space, newline and U+202E cases are kept.

**D-5 / N-2 — ADDRESSED.** `approval.go:243`: a `!Required` answer must carry no `Approved`, `Source`, `Addr`, `RemainingSec`, `AgeSec` or `Code` (code at `:237-240`). Five new unusable cases are added. `Invalid`/`InvalidCode` are not covered; see Out-of-Scope.

**D-6 / N-3 — ADDRESSED.** `localApprovalDetail` (`internal/panel/web/handlers_oldmaster.go:857`) writes nothing when `!seen.Read`. It has two callers:
- `handlers_mysql.go:728` (rebuild start, after `StartCreateReplica`).
- `handlers_jobs.go:599` (`jobAction`: retry and approve of a rebuild).

`seen.Read` is set only in `requireRebuildApproval` (`Read: err == nil`). An unusable answer comes back as `errApprovalUnreadable` and so counts as not read. A refusal before the read (connections, typed name, token) leaves the zero value, which also counts as not read. Earlier web refusals (`fail(...)`) build the detail without the field. No path writes `false` without a read, and none omits the field after a read. Tests: `TestRebuildAuditTellsNotReadFromNotApproved` and the changed offline-approve case. Both would fail on the old code.

**D-7 / N-4 — ADDRESSED.** `interest-cohort` is removed (`internal/panel/web/router.go:272`, test `web_test.go`). `scripts/e2e_browser_confirm.js` now fails on:
- every console error,
- every `requestfailed` except `net::ERR_ABORTED`,
- every response with status 400 or higher, except the provoked `POST /confirm` 401 and its console line, which are gated by the `wrongPassword` window and an exact URL match.

On the exclusions, see Residual Risks: `ERR_ABORTED` is excluded globally, the script visits a single path, and it is not part of `make test`.

**D-8 — ADDRESSED.** The text at `internal/agent/drivers/mysql/quarantine.go:523` ends exactly with `Mulai lagi Bangun ulang sebagai replika dari panel; tombol "Ulangi dari tahap ini" pada pekerjaan ini tidak cukup, karena salinan data harus diulang.` The preceding text is unchanged character for character (diff). It is pinned by `TestConfigureReplicationEndsQuarantine` (`pins_test.go`), which compares the whole message. The advice is now true, because D-9 offers the rebuild from that state.

**D-9 — ADDRESSED as specified, with two new problems in the state it opens (N-B2, N-B3 below).**
- One predicate, `orchestrator.AwaitingRebuild` (`approval.go:81`): role quarantined, OR role replica + failed row + `GetTopologyByOldMaster`. The web wrapper `oldMasterAwaitingRebuild` uses it in five places:
  - `rebuildTarget` (form GET and POST)
  - `oldMasterViews` (box)
  - `connectionsOf` (`handlers_oldmaster.go:138`)
  - `rebuildURL` on the replica page (`handlers_mysql.go:920`)
  - `rebuildURL` on the job page (`handlers_jobs.go:340`)
- What the box hides in the new state: Periksa/Amankan (`server_detail.html:382`), Hapus (`:403`) and the "Server ini adalah replika." banner.
- The sentences on the replica page and job page match addendum 2 exactly (tests compare the full HTML).
- The rebuild from this state takes the same handler and orchestrator path as a first rebuild: connections, then approval, then `startMu`, then the store. `StartCreateReplica` order is unchanged; the D-10 check sits inside `startMu`, after the agent reads.
- Nothing wider: an ordinary failed replica does not match (no old master). A running rebuild (row creating) and a succeeded one (`ClearOldMaster` in `replicaVerify`, `create_replica.go` ~`:987`) do not match either.

**D-10 — ADDRESSED (as specified).**
- `refuseAwaitingRebuild` (`approval.go:111`) refuses at three points:
  - `StartResync`: first statement under `startMu` (`resync.go:56`), before any agent call or store read beyond the predicate.
  - `DetachReplica`: inside `detachable` (`controls.go:157`), before the stop-mode `stopReplica`/`resetReplica`/`releaseRole`, and again before `RemoveReplica` (`controls.go:129`).
  - Non-rebuild `StartCreateReplica`: `create_replica.go:149`, before the failed-row removal and the role change.
- The text is exact (`approval.go:107`). It is one definition, shared with the web.
- The rebuild path is untouched (`if !p.RebuildOldMaster`). The order is still connections → approval → `startMu` → store.
- `TestFailedRebuildRefusesResyncAndDetach` uses a real runner. It asserts the exact text, row and role unchanged, no job, and no command to the old master after the mark. It would fail without each check (stop mode sends `stopReplica` first; Buat replika removes the row).
- The web test uses the fake orchestrator. It only pins the rendering, as asked.

#### State table (old master X of topology T; new master N)

Abbreviations:
- Offers / Accepts: what the page offers and what the server accepts.
- Ret: retry of the failed rebuild job.
- 2nd: a second rebuild while the failed job exists.
- Lepas: Lepas replika (detach).
- BuatR: Buat replika, non-rebuild, with X as target.
- Leftover: Periksa / Amankan data tertinggal.
- Hapus: Hapus server.
- Promote: promote of T.
- TLS: StartMySQLTLS on X. Tighten (T-wide) and add-database are listed in the notes below the table.

| State of X | Rebuild form GET/POST | Ret | 2nd | Resync | Lepas (keep/stop) | BuatR | Leftover | Hapus | Promote | TLS |
|---|---|---|---|---|---|---|---|---|---|---|
| Quarantined, no row | Offers / Accepts (approval) | n/a | n/a | not a replica: refused | not a replica: refused | not offered as target; refused | Offers / Accepts | Offers / Accepts (cutover/pending guards) | allowed (overwrites old master, see R-3) | refused (quarantined) |
| Rebuild running (row creating) | not offered / 400 | busy | busy (refuseBusy) | not offered; refused (busy) | refused (busy) | not listed / refused | not offered / refused (role replica) | not offered / refused (replica) | refused (busy) | refused (busy) |
| Rebuild failed, agent still quarantined (row failed) | Offers / Accepts, same checks | Offers / Accepts (approval re-read) | Offers / Accepts; the old job then becomes superseded (`NewerJobExists`, `runner.go:936`) | not offered / refused (D-10) | not offered / refused (D-10) | **listed as a target (`handlers_mysql.go:601`) / refused (D-10)**: offered but refused, Minor N-B4 | **not offered / refused, but the rebuild form tells the operator to run it** (N-B3) | **not offered / refused (ErrServerIsReplica, "lepas replika dulu", which is refused too)**: dead end (N-B2) | allowed; X then drops out of the predicate (R-3) | not offered / accepted; the agent needs an approval (R-5) |
| Rebuild failed after `configureReplication` succeeded (agent no longer quarantined; failed at `startReplica`/verify) | Offers / Accepts (approval not required) | Offers / Accepts (the cheap way out) | as above | **refused with "masih dikarantina": false** (N-B1) | **refused, false text** (N-B1) | listed / refused | not offered | dead end as N-B2 | as above | as above |
| Rebuild succeeded | not offered / 400 (`ClearOldMaster`) | superseded | n/a | Offers / Accepts (ordinary replica) | Offers / Accepts | n/a | not offered | needs Lepas first: works | normal | normal |
| Rebuild cancelled | quarantined again (`keepQuarantined`): as row 1 | superseded (newer job? no: row gone → "tidak lagi terdaftar") | n/a | refused | refused | refused (quarantined) | Offers | Offers | allowed | refused |
| Deleted and re-added | new id, unassigned; the agent pin stays quarantined (pre-existing, R-4) | — | — | — | — | Offers / panel accepts; the agent refuses without approval | — | Offers | — | — |

Notes on the table:
- 2nd while the first failed job exists: while the second runs, retrying the first is refused by the busy check (`Retry`: "server ini sudah menjalankan pekerjaan lain"). After the second ends (succeeded, failed or cancelled), the first is refused by `supersededBy` → `NewerJobExists`. The job page hides its retry button and therefore the RebuildURL line (`CanRetry` false).
- No new test pins this path (`TestRebuildAgainAfterFailedRebuild` does not retry the first job). It relies on the generic SEC-31 superseded tests. The agent is a second barrier: without a load record it refuses `configureReplication` (C-6).
- Tighten (T-wide) skips non-ready rows. Add-database is refused (state not ready/attention).

### New Breakage in the Fix Diff

**N-B2 (Important) — D-10 leaves no way to remove a failed-rebuild old master from the panel.**
- Code: `controls.go:157` (detach refused); `controls.go:292` (`ErrServerIsReplica`: "server ini masih terdaftar sebagai replika; lepas replika dulu"); `server_detail.html:403` (Hapus hidden).
- Scenario: X's rebuild fails, for example A6 or a disk too small, or X dies afterwards. The hospital decides to retire the machine.
  - Hapus server is not offered. A direct POST is refused with "lepas replika dulu".
  - Lepas replika is refused with "pakai Bangun ulang sebagai replika".
  - Bangun ulang needs X's agent online and root's approval. Non-rebuild Buat replika is now refused too; it was the only accidental path back to the quarantined role, where Hapus works.
  - So X can never be removed, and N cannot be deleted either (`ErrServerIsMaster` while X's row exists).
- Before D-10, Lepas replika (keep) followed by Hapus worked. Addendum 2 point 3 said to stop if a button is "needed for a legitimate way out". With D-10 in place, detach was that way out for a server that will not be rebuilt.
- Suggested fix, either of:
  - (a) For an `AwaitingRebuild` server, `DetachReplica(stop=false)` removes the row and sets the role back to `quarantined` in one store transaction, with no agent call (what `StartCreateReplica` already does). The box then offers Periksa/Amankan/Hapus with their existing guards. This also fixes N-B3.
  - (b) `deletableLocked` treats an `AwaitingRebuild` server like a quarantined one: no `releaseRole`, cutover/pending guards, row removed with the server. Show Hapus in the box.
- Either option needs the owner, because addendum 3 fixes "both modes refused". Option (a) keeps the panel's view truthful.

**N-B3 (Important) — the rebuild form in the failed state tells the operator to run an action the page hides and the server refuses.**
- Code: `handlers_oldmaster.go:954` and `:957`, rendered by `rebuildTarget`, which D-9 opened to this state. Against them: `server_detail.html:382` (Periksa/Amankan hidden when `RebuildFailed`) and `oldmaster.go:103` (`StartLeftover`/`StartSalvage` refuse when role is not quarantined).
- Scenario: the operator skipped "Periksa data tertinggal" before the first rebuild. The rebuild fails before the load step (check master/target, preflight, prepare). The old master's un-replicated hospital transactions are still on disk. The form now says "Jalankan Periksa data tertinggal (dan Amankan bila ada) di halaman server ini dulu". The server page offers no such button, and a direct POST answers `msgNotOldMaster`.
  - The operator can either rebuild and lose the transactions, or remain stuck. The accidental route back to quarantined (non-rebuild Buat replika) is closed by D-10.
  - The same happens with `master_start_late` open (its warning at `:954`), which the monitor keeps open while the row is failed.
- Suggested fix: fix (a) above. Alternatively, let `oldMasterOf`/`oldMasterFromPath` accept `AwaitingRebuild` when the failed job stopped before the snapshot step. At minimum, word the two warnings truthfully for `RebuildFailed` (say the check is not available here and why) rather than send the operator to a hidden button.

**N-B1 (Minor) — the predicate and its texts claim "masih dikarantina" for a server whose agent ended its quarantine.**
- Code: `approval.go:81` and `:107`; the box header via `handlers_oldmaster.go:473`; the replica page line.
- Scenario: the rebuild's `configureReplication` succeeds at the agent (quarantine ended, pin replica). Then `startReplica`, `awaitVerifiedLink` or `waitCaughtUp` fails, the last of which is likely on a large lagging database. The row is failed and the old master is still recorded (`ClearOldMaster` runs only on verify success). The predicate matches:
  - the box says "Master lama (dikarantina sejak …)";
  - the replica page says "Server ini master lama yang masih dikarantina";
  - Resync and Lepas are refused with "…masih dikarantina…".

  All are false. Nothing unsafe follows: the retry of the verify step works, and a new rebuild runs with `required:false`. But the panel misstates the agent's state, and the job-page line pushes toward a needless full re-copy.
- Suggested fix: neutral wording ("master lama yang bangun ulangnya gagal"). This needs addendum 3's text changed. Alternatively, record in the job state that the connect step completed and leave such a server out of the predicate.

**N-B4 (Minor) — "Buat replika" still lists such a server as a retry target, then refuses it.** `handlers_mysql.go:592-601` lists every failed replica row. D-10 refuses it (`create_replica.go:149`), so the page offers something the server refuses. The implementer noted this. Suggested fix: skip `AwaitingRebuild` servers in the target list.

Checked and clean:
- Templates: the new lines render only `RebuildURL`, built with `fmt` from integer ids in an `href`, contextually escaped; no agent or operator text.
- No new GET calls the agent or changes state: `rebuildURL` and `AwaitingRebuild` only read the store.
- The new POST refusals keep the token and confirmation path unchanged.

### Security headers as served at the head

All are set by `SecurityHeaders` (`internal/panel/web/router.go:279-299`) on every UI response.

| Header | Value | Assessment |
|---|---|---|
| Content-Security-Policy | `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; worker-src 'none'; manifest-src 'self'` | Strong. No inline script or eval; `data:` for images only. |
| X-Content-Type-Options | `nosniff` | Good. |
| X-Frame-Options | `DENY` | Good (duplicates `frame-ancestors`). |
| Referrer-Policy | `same-origin` | Good; the reason for not using no-referrer is documented. |
| Permissions-Policy | `camera=(), microphone=(), geolocation=(), payment=(), usb=(), serial=(), hid=(), bluetooth=(), display-capture=(), clipboard-read=()` | Good. `interest-cohort` is gone. `bluetooth` gives a console warning (not an error) in current Chromium; the owner kept it (addendum 3). |
| Cross-Origin-Opener-Policy | `same-origin` | Good. |
| Cross-Origin-Resource-Policy | `same-origin` | Good. |
| Cross-Origin-Embedder-Policy | `require-corp` | Good. |
| Cache-Control | `no-store` (static assets and SSE replace it: `router.go:433`, `sse.go:77`) | Good. |
| Strict-Transport-Security | `max-age=31536000` (TLS requests only) | Adequate for an IP or host-addressed panel; no `includeSubDomains`, deliberately. |
| Server | removed | Good. |

### Residual Risks

- **R-1 Monitor silent in the failed state.** While X is role replica with a failed row, `master_start_unguarded` and the start-state reconciliation (late/failed/bypassed start) are not raised, because they require role quarantined. A bad, writable MySQL start of X goes unalerted until the next rebuild starts. Precondition: a failed rebuild and a MySQL restart on X.
- **R-2 Browser check scope.** The check excludes every `net::ERR_ABORTED`, so a subresource cancelled for another reason would pass. It visits one path (login → replica → job) and runs only with `-tags e2e_browser`, outside `make test`/`make test-e2e`. A header or CSP regression on other pages is caught only by `TestSecurityHeaders`.
- **R-3 A later promote drops X out of the predicate.**
  - `SwapMaster` overwrites `topologies.old_master_server_id`, and `StartPromote` does not refuse while an old master is recorded. If T is promoted again while X is in the failed state, X becomes an ordinary failed replica in the panel while its agent stays quarantined. Resync and Lepas are then offered and accepted, and Lepas leaves a quarantined agent recorded as unassigned.
  - The agent still refuses protected commands without an approval.
  - Precondition: a second promote of T before X is rebuilt.
- **R-4 Deleted and re-added quarantined server.** It appears as unassigned while its agent stays quarantined. Only the agent's approval gate protects it. This predates this round.
- **R-5 TLS on X in the failed state.** `StartMySQLTLS` is not refused, and the daily renewal skips only role quarantined. A renewal job on X needs the agent's approval to install. Without one it fails, and as a newer job it supersedes the failed rebuild job: its retry and its RebuildURL line disappear, and the box's rebuild stays. With a kept approval it installs a certificate and restarts MySQL; the start lock holds read-only.
- **R-6 D-6 semantics.** `local_approval:false` is also written for a read `required:false` answer (agent not quarantined), so an audit reader tells "not required" from "not approved" only by the entry's outcome.
- **R-7 Accepted host shapes.** The approval source may now be a 255-character name, `_`, `.` or `-`-only. These are inert, bounded by `AgentText`, and still a compromised agent's choice of display string within that charset.

### Out-of-Scope Observations

- `RebuildApprovalUsable` still allows `Invalid`/`InvalidCode` on a `required:false` answer (the brief did not list them). The form would then show an invalid-approval reason next to "no approval needed". Same principle as N-2; a small follow-up.
- `refuseAwaitingRebuild` returns nil for role quarantined (`approval.go:119`). A quarantined server that still has a replica row is reachable only if `RemoveReplica` fails in `createJob`'s undo before `keepQuarantined` runs. That server could be resynced or detached to unassigned. Using `AwaitingRebuild` unconditionally for resync and detach would close it.
- In `StartCreateReplica` with `RebuildOldMaster`, an ordinary failed replica as target has its row removed and is left unassigned before the "bukan master lama" refusal (`create_replica.go` ~`:152-184`). The same goes for a failed old master of another topology. Web blocks it through `rebuildTarget`. It predates this round.
- `TestFailedRebuildStartsAsFirstRebuild` always sends `accept_connections=1`, so a missing acknowledgement in this state is not tested at web level. The orchestrator check is shared code.

### Verdict
**Fix round:** All findings (D-4 … D-10) addressed as specified. New Important breakage: N-B2 (no way to remove a failed-rebuild old master: detach refused, delete refused "lepas replika dulu", Buat replika refused) and N-B3 (the rebuild form in the failed state tells the operator to run Periksa data tertinggal, which the page hides and the server refuses; un-replicated data can no longer be checked or salvaged). Minor: N-B1, N-B4.
