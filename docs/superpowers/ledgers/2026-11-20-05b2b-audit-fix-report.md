# Security-audit fix wave — report

Base b950757, branch claude/install-superpowers-plugin-ev2k9i. Commits (oldest first):

| Commit | Item |
|---|---|
| 5f9dc80 fix(panel): point a presumed-down master to the emergency promote | S1 |
| 899513c fix(panel): bound the agent's role pin in the master_unpinned alert | S2 |
| 344a963 fix(panel): refuse a state-changing post from the panel's own http origin | S3 |
| f2514a2 fix(panel): refuse Ulangi of a promote that failed before the switch | S4 |
| fcbdd64 fix(agent): refuse an over-long backup folder before it is stored | S5 |
| d01422b docs(plan): list the security-audit wave's new operator texts | texts |
| 5fb84df fix(panel): no-store on static errors and the event stream; stricter Permissions-Policy | S6 |

RED/GREEN outputs are quoted below (cut at 170 characters per line; full files in the scratch directory
`s<n>-red.txt` / `s<n>-green.txt`).

---

## S1 (WB-1) — fixed

**Change.**
- `web/templates/_status.html:50-61` (`down-guide`): the box keeps its title and its two harmless first steps, drops
  steps 3-5 and the note; the first step now also names MySQL; a new step 3 points to the page's own card and button
  and links the guide. No SQL, no "panel belum bisa".
- `internal/panel/web/views_monitor.go:45-46`: comment of `DownGuide` only.
- `README.md:1056-1060` ("Pemantauan", master statuses): the sentence that said the page shows "langkah manual" and
  that the panel cannot move the master role is replaced.

**Operator texts (new or changed), exactly:**
- Box `Yang bisa dilakukan sekarang` (title unchanged), list:
  1. `Periksa listrik, jaringan dan MySQL di server master. Bila master hidup lagi, panel melihatnya sendiri dan replika menyambung kembali.` (was: `Periksa listrik dan jaringan server master. …`)
  2. `Jangan me-restart replika: posisi replikasinya dibutuhkan untuk langkah berikut.` (unchanged)
  3. `Bila master tidak bisa dipulihkan, jalankan promote darurat: di kartu <strong>Ganti master</strong> di halaman ini, tekan <strong>Promote…</strong>. Ikuti langkahnya di panduan "<a class="link" href="/panduan#promote-darurat">Promote darurat</a>".`
  - Removed: old step 3 (Executed_Gtid_Set), step 4 (the SQL), step 5 (`Arahkan aplikasi ke alamat IP replika itu.`), and `Catatan: panel belum bisa melakukan langkah ini; versi berikutnya akan melakukannya otomatis.`
  - "Ganti master" and "Promote…" are exactly the card heading and button of `master_detail.html:98,102`; the card has no condition, so it is on the page whenever the box is.
- README: `Untuk master yang diduga tumbang, halamannya menampilkan kotak "Yang bisa dilakukan sekarang": periksa listrik, jaringan dan MySQL di server master, jangan me-restart replika, dan bila master tidak bisa dipulihkan, jalankan promote darurat dari kartu **Ganti master** (tombol **Promote…**) dengan panduan "Promote darurat".`

**Search for manual promotion in operator texts.** Command:
`git grep -n -i "RESET REPLICA ALL\|secara manual\|panel belum bisa\|belum bisa melakukan\|versi berikutnya" -- ':!docs' ':!graphify-out'`,
then `git grep -n -i "STOP REPLICA\|super_read_only=OFF\|read_only=OFF\|promote.*manual\|manual.*promote\|dengan tangan\|jadikan .* master"` over README, `web/`, `internal/panel`, `internal/agent/cli*.go`, `cmd`, and the alert/Telegram texts (`internal/panel/monitor/alerts.go`, `notify/message.go`: `master_down` texts only say "Master diduga tumbang: …", no advice to promote by hand). Found:

| Place | What | Changed? |
|---|---|---|
| `web/templates/_status.html:58,61` | the down-guide SQL and "panel belum bisa" | changed (above) |
| `README.md:1058` | "langkah manual … panel belum bisa memindahkan peran master sendiri" | changed (above) |
| `README.md:700, 2989`, `panduan.html:24` | "dijalankan ulang secara manual" = replaying a salvage SQL file | not promotion; unchanged |
| `internal/agent/drivers/mysql/install.go:403`, `realhost.go:160,171` | "pasang MySQL secara manual", "perbaiki file secara manual" | not promotion; unchanged |
| `internal/panel/monitor/explain.go:93` | "panel belum bisa mengarantinanya karena agent-nya belum …" | about a pending quarantine, not promotion; unchanged |
| `README.md:1388`, `handlers_mysql.go:208-209`, `replica_detail.html:218` | after **Lepas dan hentikan replikasi** (detach), how to open the detached server: `mysql -e "SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF"` | unchanged: the detach is a separate, confirmed feature that removes the server from the topology; it does not tell the operator to promote a replica of a down master. Listed for the owner (see "Noticed"). |
| `README.md:2700-2710, 3385-3413, 2871-2872, 3273-3274`, `panduan.html:47-59` | guides 5 and 7 and their README sources (start lock by hand; reusing a quarantined server) | unchanged by rule |
| `cli.go:351` | "lepas dengan tangan" (start lock) | not promotion; unchanged |
| agent `promote.go`, `replication.go` and tests | `RESET REPLICA ALL` in code and logs, not operator advice | unchanged |
| `docs/` | plans, specs, ledgers (not shown to operators) | unchanged |

**Tests.** `go test ./internal/panel/web/ -run 'TestMasterPageDownPointsToEmergencyPromote|TestNoTemplatePromotesByHand'`
(`web_monitor_test.go`: the old `TestMasterPageDownShowsManualGuidance`, which asserted the SQL was there, is replaced; the
healthy-master part is kept; new `TestNoTemplatePromotesByHand` scans every `web/templates/*.html` for `RESET REPLICA ALL`,
`master secara manual`, `panel belum bisa`.)

RED:
```
--- FAIL: TestMasterPageDownPointsToEmergencyPromote (0.08s)
    web_monitor_test.go:270: status section lacks "Periksa listrik, jaringan dan MySQL di server master.":
    web_monitor_test.go:270: status section lacks "Bila master tidak bisa dipulihkan, jalankan promote darurat: di kartu <strong>Ganti master</strong> …
    web_monitor_test.go:270: status section lacks "href=\"/panduan#promote-darurat\"":
    web_monitor_test.go:276: status section still has "RESET REPLICA":
    … "STOP REPLICA", "SET PERSIST", "read_only", "secara manual", "panel belum bisa", "versi berikutnya", "Executed_Gtid_Set"
--- FAIL: TestNoTemplatePromotesByHand (0.00s)
    web_monitor_test.go:692: ../../../web/templates/_status.html contains "RESET REPLICA ALL"
    web_monitor_test.go:692: ../../../web/templates/_status.html contains "master secara manual"
    web_monitor_test.go:692: ../../../web/templates/_status.html contains "panel belum bisa"
```
GREEN:
```
--- PASS: TestMasterPageDownPointsToEmergencyPromote (0.05s)
--- PASS: TestNoTemplatePromotesByHand (0.00s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/web	0.063s
```
`TestGuideMatchesREADME` unaffected (no guide changed). `bash scripts/tailwind.sh`: app.css unchanged (the `link` class already existed).

---

## S2 (PC-3) — fixed

**Change.** `internal/panel/monitor/alerts.go:468` new bound `maxAgentPin = 32`; `:568-575` `unpinnedMessage`: a pin
other than `replica` / `quarantined` (the two values the message already named as such; `""` and `unassigned` have
their own sentences) is `"tidak dikenal (" + agentText(pin, maxAgentPin) + ")"` — the form of the server page
(`handlers_servers.go:685`). `agentText` makes it one line (controls, line breaks, bidi marks → one space) and cuts it at
32 characters with `…`. Test-only `internal/panel/monitor/export_test.go` exports `unpinnedMessage` to an external test
that builds the Telegram text with `notify.Format` (monitor's internal tests cannot import notify: cycle).

**Operator text (changed):** `Master tidak dipin: peran yang dipin di server ini tidak dikenal (<pin, one line, ≤32 characters>), bukan master. Agent tidak menjaga server ini sebagai master (…). Saran: bila server ini memang master, jalankan di server ini: sudo dbpanel agent set-role master, lalu jalankan ulang Siapkan master.` (only the pin part changed; before: the raw pin).

**Tests.** `go test ./internal/panel/monitor/ -run 'TestMasterUnpinnedAlertBoundsAgentPin|TestUnpinnedTelegramTextBounded'`
(5,000-character pin with `\n`, `\r\n`, U+202E: the stored message is one line, ≤400 characters, names `tidak dikenal (PINX…`;
the Telegram text has exactly one line break (message, tail) and ≤500 characters.)

RED:
```
--- FAIL: TestMasterUnpinnedAlertBoundsAgentPin (0.03s)
    alerts_test.go:1379: message is not one line: "Master tidak dipin: peran yang dipin di server ini PINXJalankan ini\nsudo rm -rf /\r\n‮Jalankan ini…
    alerts_test.go:1385: message is 4978 characters, want the pin bounded: …
--- FAIL: TestUnpinnedTelegramTextBounded (0.00s)
    unpinned_notify_test.go:25: Telegram text has 834 line breaks, want 1 (message, tail):
    unpinned_notify_test.go:28: Telegram text is 5368 characters, want the pin bounded:
    unpinned_notify_test.go:31: Telegram text lacks the bounded pin:
```
GREEN:
```
--- PASS: TestMasterUnpinnedAlertBoundsAgentPin (0.03s)
--- PASS: TestMasterUnpinnedAlertAfterThreeReports (0.07s)
--- PASS: TestUnpinnedTelegramTextBounded (0.00s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/monitor	0.112s
```

**The web-side point (`Status.Detail` of "Tidak terjangkau").** Already bounded / never shown raw; nothing changed:
- `internal/panel/monitor/status.go:166-171, 260-263` set `Status.Detail` to the agent's `MySQLError` (censored by
  `redact.Quotes` at ingest, `state.go:223`).
- Web: `internal/panel/web/views_monitor.go:82-83` replaces it with the panel's own `MySQL tidak menjawab` in every
  status view (page, row, fragment).
- Timeline/events: `internal/panel/monitor/state.go:564-569` (`eventDetail`) stores `MySQL tidak menjawab` instead.
- Alert `mysql_unreachable`: fixed text (`alerts.go:224-229`); `stoppedMessage` uses `Status.Detail` only for a stopped
  replica (an errno, never the MySQL text).
- The raw text is shown only under **Detail teknis** via `shownSnap` → `agentLine` (`handlers_mysql.go:1076`),
  i.e. labelled and bounded by `maxAgentWords` (`AgentQuoteN`). Covered by the existing assertion in
  `web_monitor_test.go` (replica page: "dial tcp" not in the status section, present in Detail teknis).

---

## S3 (WB-3) — fixed

**Change.** `internal/panel/web/handlers_auth.go:162-176`: `sameOrigin` now also requires `u.Scheme == "https"` for the
Origin (or, without one, Referer). Choice: the auditor's fix exactly — https always, not "same scheme as the request".
Why that is safe and not weaker: the UI's state-changing routes are only served by the TLS server
(`internal/panel/servers.go:125-146`); the plain-HTTP side of the port (`tlsui.go:902-913`, `httpHelperHandler`) answers
only GET/HEAD of the certificate paths and 301s everything else, so no legitimate POST ever reaches `sameOrigin` with an
http origin. Unit tests (httptest without TLS) now post with `testOrigin = "https://example.com"`
(`web_test.go:35`); real-TLS tests in `internal/panel` already used an `https://` base.

**Tests.** New cases: the origin `http://example.com` (= `http://<Host>`), and for `/login` etc. also as Referer, on a
request built over TLS (`httptest.NewRequest("POST", "https://example.com"+path, …)`) in `TestPostRejectsCrossOrigin`
(old routes `/login`, `/logout`, `/servers`, `/servers/1/token`, `/servers/1/delete`), `TestCSRFOnNewRoutes` (5a/5b-1
control routes incl. `/servers/2/snooze`) and `TestBackupRoutesNeedLoginAndSameOrigin` (this plan's three routes).
Command: `go test ./internal/panel/web/ -run 'TestPostRejectsCrossOrigin|TestBackupRoutesNeedLoginAndSameOrigin|TestCSRFOnNewRoutes'`.

RED (excerpt):
```
--- FAIL: TestCSRFOnNewRoutes (0.10s)
    web_controls_test.go:560: POST /replicas/2/stop origin="http://example.com": status 303, want 403
    … /replicas/2/start, /relock, /resync/prepare, /servers/2/check, /servers/2/snooze …
--- FAIL: TestBackupRoutesNeedLoginAndSameOrigin (0.05s)
    web_servers_test.go:162: POST /settings/panel-key-backed-up origin="http://example.com": status 303
    web_servers_test.go:162: POST /settings/audit-forwarded origin="http://example.com": status 303
    web_servers_test.go:162: POST /servers/1/backup origin="http://example.com": status 303
--- FAIL: TestPostRejectsCrossOrigin (0.07s)
    web_test.go:436: POST /login origin="http://example.com": status 303, body ""
    web_test.go:439: POST /login origin="http://example.com": cookie set
    web_test.go:436: POST /login origin="Referer http://example.com/servers/1": status 303, body ""
    … /logout, /servers, /servers/1/token …
```
GREEN:
```
--- PASS: TestCSRFOnNewRoutes (0.06s)
--- PASS: TestBackupRoutesNeedLoginAndSameOrigin (0.04s)
--- PASS: TestPostRejectsCrossOrigin (0.04s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/web	0.160s
```
No operator text changed (the 403 text is the existing `msgForbidden`).

---

## S4 (PC-1, ruling R43) — fixed for the emergency kind; planned kind fixed except one state, where Retry is unchanged (see fallback)

### Trace (done before the code)

Facts from the code at b950757:
- A promote job ends **failed** only with `!PastPoint && !PromoteSent`: `promoteFailed` (`promote.go:1015-1061`) returns
  partial whenever `PastPoint`, or `PromoteSent` with an unknown/possibly-running agent job; it resets `PromoteSent`
  only when the candidate is read-only and its agent job is over, then calls `promoteUndo`. `resumePromote`
  (`:1140-1153`) fails a job only when neither flag is set. So "failed promote job" ⇔ "mysql.promote never sent (or
  sent and definitively refused and restored)" ⇔ the refusal condition.
- `promoteUndo` (`:1093-1134`) sets the promotion record to `failed`; for the planned kind with `LockTried` it calls
  `mysql.unlockPromote` with the candidate's credential: success, or "tidak dikunci untuk promosi", records
  `LockUnlocked`; any other error leaves the lock and logs that the master's agent lifts it at the deadline.
- A new `StartPromote` (`:158-329`) refuses only a previous promotion that is `running` or `partial`
  ("promote sebelumnya … belum tuntas"), an active job (`ActiveJobForTopology`/`refuseBusy`: running, waiting, queued
  only), a pending cutover, and, for an emergency, `refuseWhileLockMayStand`. A `failed` promotion record and a
  `failed` job block nothing. "pekerjaan belum selesai" is not reached by a failed job.
- The master's agent refuses a second lock while one exists (`promote_lock.go:177-185`: "sudah dikunci untuk promosi
  … kunci baru ditolak"); `unlockPromote` (`:438-…`) only for the same candidate, only while the candidate is readable
  and read-only and every listed replica is readable; the guard (`GuardTick`, `:544-…`) lifts the lock at its
  deadline (30 min, `promoteLockSec`) after reading the candidate read-only twice a grace (60 s) apart, keeps it while a
  listed replica is unreadable or after an agent restart (no credential), and quarantines on a writable candidate.
  The panel's **Buka kunci master** (`controls.go:64-79`, `mysql.setReadOnly off`) is refused by the agent while a
  promote lock stands (`pins.go:209`: "jalankan di server ini: sudo dbpanel agent unlock-master").
- `refuseWhileLockMayStand` (`:390-…`) refuses an emergency (and data loads) while the latest promotion is a failed
  planned one whose lock was asked and not confirmed open, until deadline + grace; its text tells the operator to press
  **Ulangi** on that planned job "(panel memastikan kunci terbuka)" — README `1450-1462` says the same.
- **Batalkan** (`Cancel`, `runner.go:1174-1190`) refuses failed/partial jobs (`ErrJobFinished`); it applies only to a
  running job (and calls `promoteUndo` there).

Per kind, after a job failed before the promotion step:

| | Emergency | Planned, lock never asked or unlock confirmed (`!LockTried` or `LockUnlocked`) | Planned, unlock not confirmed (`LockTried && !LockUnlocked`) |
|---|---|---|---|
| Master locked? | never (no lock in this kind) | no | maybe, until deadline (30 min) + 60 s grace; possibly longer if the guard keeps it (listed replica unreadable, agent restarted) |
| Promotion record | `failed`: blocks nothing | `failed`: blocks nothing | `failed`: blocks nothing; blocks an **emergency** start until deadline + grace (`refuseWhileLockMayStand`) |
| What releases a lock | — | — | the failure path's `promoteUndo` (already failed here), a later `promoteUndo` of a job of the same candidate (Retry today, or a new start's failed lock step), the guard at the deadline, root's `unlock-master`; not **Buka kunci master**, not Batalkan |
| New start possible at once? | yes: emergency start with the tick (mode re-read); while the master is healthy again it is refused by the mode check, and a planned start (gate) is possible | yes: planned start, judged by the gate and the backup tick | **no, not guaranteed**: a new planned start is accepted but its lock step is refused by the master's agent while the old lock stands (with the same candidate its undo then unlocks, so a *third* start works; with another candidate nothing until the guard). An emergency start is refused by `refuseWhileLockMayStand` until deadline + grace, with a text that names Ulangi. |

### Decision
- **Emergency: built.** Retry refused (`ErrPromoteRetryBeforePoint`), no command sent; nothing stands, a new start is
  possible at once.
- **Planned, lock not standing (`!LockTried`, `LockUnlocked`, or now ≥ deadline + 60 s): built.** Refused; a new
  planned start runs at once through the gate and backup tick.
- **Planned, lock may stand (unlock not confirmed and now < deadline + 60 s): not built — fallback "leave unchanged".**
  A new start is not guaranteed to work then, and today's Ulangi is the panel's documented way to confirm the lock open
  (the emergency refusal text and README 1450-1462 send the operator there). The auditor's alternative (Retry requiring
  mode re-read, gate, backup tick and a password confirmation) needs a confirmation dialog with password and backup tick
  on the job page for a promote job and a change to K4 in `JobGateInput`/`confirmedJobAction`; I judged that not safe to
  build in this single wave without review, so this state keeps today's tokenless, ungated Retry. The window is bounded
  by the lock's deadline + grace (at most ~30 min + 60 s after the lock was taken, or `LockTriedAt` + 35 min when the
  answer was lost). After it, Retry is refused like any other: at that point a Retry could not continue the promotion
  either while a guard-kept lock stood (the agent refuses the relock), and a new start with the same candidate reaches
  the same `promoteUndo` → `unlockPromote` the Retry would have — so no way out the panel had is removed.
- Never stranded: no new state leaves a master read-only; the refusal sends nothing and changes nothing; every lock path
  above is as before. Never easier: the change only removes Retry in some states.

### Change
- `internal/panel/orchestrator/promote.go:435-458` `lockMayStandUntil` (the deadline computation moved out of
  `refuseWhileLockMayStand`, `:390-…`, unchanged behaviour); `:1694-1750` `ErrPromoteRetryBeforePoint`,
  `promoteRetryRefused`, `RetryRefused(jobID)` (for the job page; `""` when it cannot tell — Retry decides then).
- `internal/panel/orchestrator/runner.go:994-998`: in `Retry`, after `refuseSuperseded` (so a superseded job keeps its
  own message) and before the gate and the token.
- Web: `internal/panel/web/handlers_mysql.go:55-57` interface method `RetryRefused`; `handlers_jobs.go:145-148,340-344`
  `jobPage.RetryRefused`, `CanRetry=false` when set; `web/templates/job_detail.html:19` an info banner with the
  sentence. The button "Ulangi dari tahap ini" is not rendered for such a job; a POST anyway is refused by `Retry`.
- README `2894-2903` (section "Promote", before "Bila promote berhenti setelah …"): new paragraph. README 1450-1462
  (Ulangi in the lock window) stays true and unchanged. No guide mentions Ulangi (checked `panduan.html`); mirror test
  green.

**Operator text (new):** `Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master.`
(only change from the given text: the button's screen name "Ulangi dari tahap ini" in quotes.) Shown as the job-page
banner and as the refusal of a POSTed Retry.

**README (new):** `Bila promote gagal sebelum master diganti (replika terpilih belum bisa ditulis dan tidak ada yang berubah), halaman pekerjaannya tidak menawarkan **Ulangi dari tahap ini** dan menulis "Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master." Promote baru menilai lagi jenisnya dan meminta lagi centang, kata sandi, dan untuk promote terencana daftar **Prasyarat**. Satu pengecualian: setelah promote terencana yang kunci masternya belum terbukti dibuka, **Ulangi dari tahap ini** tetap ada sampai tenggat kunci itu ditambah 60 detik lewat (lihat "Promote terencana dan darurat").`

### Tests
New `internal/panel/orchestrator/promote_retry_test.go`:
- `TestPromoteEmergencyRetryBeforePointRefused`: candidate refuses `mysql.promote` → job failed, promotion failed;
  Retry → `ErrPromoteRetryBeforePoint`, no agent command after it, job still failed, `mysql.promote` sent once;
  master back healthy → new emergency start refused "master masih sehat"; master gone again → without the tick
  `ErrOldMasterAck`, with it the new promote succeeds; the old job is then superseded.
- `TestPromotePlannedRetryBeforePointRefused`: catch-up timeout, lock lifted → Retry refused, nothing sent; new planned
  start with a red row → `ErrGateRed`; green → succeeds.
- `TestPromotePlannedRetryWhileLockMayStand`: unlock fails (agent offline answer) → `RetryRefused` empty and Retry runs
  (as today); at deadline + grace − 1 s still offered; at + 1 s refused, nothing sent.
- `TestPromoteRetryPastPointStillRuns`: partial job (quarantine failed) → `RetryRefused` empty, `DropsData` false,
  `JobGateInput` not gated, a red row on the new master, Retry `JobAck{}` → succeeded.
- `TestPromoteRetryRefusedText`: the exact text.
Updated (what they guard for past-point jobs kept): `runner_test.go` `TestPromoteRetryStillTokenless` now retries a
partial (past-point) promote without a token; `gate_enforce_test.go` `TestRetryGatedOnlyWhenDataIsDropped/promote`
asserts the before-point refusal and that the new start is gated (the past-point no-gate case is in
`TestPromoteRetryPastPointStillRuns`); `promote_test.go` `TestPromotePlannedCatchUpTimeoutUnlocks` (Retry refused, no
second lock) and `TestPromoteFailureAtNewMasterUnlocksOrGoesPartial` (Retry refused; the "writable candidate → partial"
and "partial → Retry → succeeded" halves now run on a new start's job). Web: `web_jobs_test.go`
`TestPromoteRetryRefusedHidesButton` (both kinds: banner shown, no `/retry` form, no button), `TestPromoteRetryStillTokenless`
now with a past-point job's params.

Commands: `go test ./internal/panel/orchestrator/ -run '<the tests above>' -v`; `go test ./internal/panel/web/ -run 'Tokenless|TestPromoteRetryRefusedHidesButton' -v`.

RED (production stub: `ErrPromoteRetryBeforePoint` declared, `RetryRefused` returning ""):
```
--- FAIL: TestPromoteEmergencyRetryBeforePointRefused (0.06s)
    promote_retry_test.go:76: Retry = <nil>, want ErrPromoteRetryBeforePoint
--- FAIL: TestPromotePlannedRetryBeforePointRefused (0.17s)
    promote_retry_test.go:128: Retry = <nil>, want ErrPromoteRetryBeforePoint
--- FAIL: TestPromotePlannedRetryWhileLockMayStand (0.29s)
    promote_retry_test.go:191: Retry = <nil>, want ErrPromoteRetryBeforePoint
FAIL	github.com/husmin-fajrin/db-panel/internal/panel/orchestrator	0.661s
--- FAIL: TestPromoteRetryRefusedHidesButton (0.11s)
    web_jobs_test.go:409: body does not contain "Promote ini gagal sebelum master diganti. &#34;Ulangi dari tahap ini&#34; tidak tersedia …
    web_jobs_test.go:411: body contains "action=\"/jobs/4444444444444444444444444444444b/retry\""; body:
    web_jobs_test.go:411: body contains ">Ulangi dari tahap ini<"; body:
```
After the implementation, before updating the old tests (they asserted the old rule):
```
--- FAIL: TestRetryGatedOnlyWhenDataIsDropped   gate_enforce_test.go:368: Retry of a failed promote with a red row: Promote ini gagal sebelum master diganti. …
--- FAIL: TestPromotePlannedCatchUpTimeoutUnlocks   promote_test.go:350: Retry of a failed promote before the point of no return: Promote ini gagal …
--- FAIL: TestPromoteFailureAtNewMasterUnlocksOrGoesPartial   promote_test.go:880: Retry: Promote ini gagal …
--- FAIL: TestPromoteRetryStillTokenless   runner_test.go:245: Retry of a promote without confirmation: Promote ini gagal …
```
GREEN:
```
--- PASS: TestRetryGatedOnlyWhenDataIsDropped (0.37s)
--- PASS: TestPromoteRetryRefusedText (0.00s)
--- PASS: TestPromoteEmergencyRetryBeforePointRefused (0.09s)
--- PASS: TestPromotePlannedRetryBeforePointRefused (0.23s)
--- PASS: TestPromotePlannedRetryWhileLockMayStand (0.29s)
--- PASS: TestPromoteRetryPastPointStillRuns (0.10s)
--- PASS: TestPromotePlannedCatchUpTimeoutUnlocks (0.18s)
--- PASS: TestPromoteFailureAtNewMasterUnlocksOrGoesPartial (0.12s)
--- PASS: TestPromoteEmergencyRefusedWhileLockMayStand (0.20s)
--- PASS: TestPromoteEmergencyAllowedAfterConfirmedUnlock (0.20s)
--- PASS: TestPromoteRetryStillTokenless (0.09s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/orchestrator	1.920s
--- PASS: TestPromoteRetryStillTokenless (0.07s)
--- PASS: TestTightenTLSRetryTokenless (0.06s)
--- PASS: TestPromoteRetryRefusedHidesButton (0.09s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/web
```

---

## S5 (AG-1, length check only) — fixed

**Change.**
- `internal/agent/monitor/backup.go:46-48`: `ValidBackupFolder` checks `len(p) > maxBackupFolderLen` first, before
  `filepath.IsAbs` and `filepath.Clean`.
- `internal/agent/monitor/monitor.go:247-…` (`Configure`): a `backup_folder` over 1024 bytes is cut to its first 1025
  bytes, copied (`strings.Clone`, `:284-288`, so the request's 1 MiB string is not kept), before it is compared and
  stored. Handling as for any refused folder today: still over the limit, so `ValidBackupFolder` refuses it → not
  scanned, reported `unreadable`/`invalid`; the call's other settings (check interval, summaries, lag, role hint) are
  applied; the same over-long folder sent again is no change (no new generation). Doc comments updated
  (`monitor.go:165-168`, `Configure`).
- Panel: already aligned, no production change — `internal/panel/web/backup_folder.go:62` refuses `len(p) > 1024`
  (same `maxBackupFolderLen = 1024`), so it never accepts a longer folder than the agent. The table shared with the
  agent (`backup_folder_test.go` `backupFolderCases`, used by `TestBackupFolderRules` and
  `TestBackupFolderRulesMatchAgent`) gained the 1024-byte limit case, a 1 MiB folder and an over-long non-clean folder.
- Not done by instruction: no request semaphore, no change to `session.go` or the systemd unit.

**Tests.** `go test ./internal/agent/monitor/ -run 'TestConfigureRefusesOverlongFolder|TestValidBackupFolderLengthFirst'`;
`go test ./internal/panel/web/ -run TestBackupFolder`.
- `TestConfigureRefusesOverlongFolder`: 1 MiB folder → answer `unreadable/invalid`, loop pause 30 s and summaries 600 s
  applied, at most 1025 bytes kept, same folder again keeps the generation, no scan; a 1024-byte folder is kept whole
  and pending.
- `TestValidBackupFolderLengthFirst`: an over-long, not-clean folder (`"/" + "a//"×65536`) → invalid with zero
  allocations (`testing.AllocsPerRun`; `filepath.Clean` allocates twice for it). (A first version with `"a/../"` passed
  before the fix — `Clean` does not allocate for that shape — and was replaced; noted for honesty.)

RED:
```
--- FAIL: TestConfigureRefusesOverlongFolder (0.02s)
    backup_test.go:619: monitor keeps 1048581 bytes of the folder, want at most 1025
--- FAIL: TestValidBackupFolderLengthFirst (0.01s)
    backup_test.go:654: ValidBackupFolder of an over-long folder allocates 2 times, want 0 (length first)
FAIL	github.com/husmin-fajrin/db-panel/internal/agent/monitor	0.033s
```
GREEN:
```
--- PASS: TestConfigureKeepsSettingsWithBadFolder (0.00s)
--- PASS: TestConfigureRefusesOverlongFolder (0.03s)
--- PASS: TestValidBackupFolderLengthFirst (0.00s)
ok  	github.com/husmin-fajrin/db-panel/internal/agent/monitor	0.037s
--- PASS: TestBackupFolderRules (0.00s)
--- PASS: TestBackupFolderRulesMatchAgent (0.00s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/web	0.019s
```
No operator text changed.

---

## S6 (WB-8, no-risk parts of WB-9) — fixed

**Change.**
- `internal/panel/web/router.go:443-462`: `cacheFor` wraps the static handler's writer (`noStoreOnError`): an answer
  of status ≥ 400 gets `Cache-Control: no-store` as its header is written (Go's file server deletes Cache-Control on
  its errors). The rest of the set comes from `SecurityHeaders` as before.
- `internal/panel/web/sse.go:77`: `Cache-Control: no-store` (was `no-cache`).
- `router.go:277-285` `permissionsPolicy`: adds `accelerometer=(), gyroscope=(), magnetometer=(), midi=(),
  fullscreen=(), picture-in-picture=(), publickey-credentials-get=(), screen-wake-lock=(), xr-spatial-tracking=(),
  autoplay=(), encrypted-media=(), idle-detection=(), local-fonts=(), window-management=(), browsing-topics=()` — all
  fifteen. Checked: `web/static/app.js` uses none of them (only `navigator.clipboard.writeText` and `fetch`); the only
  hits in `alpine-csp.min.js` are the boolean-attribute name lists (`allowfullscreen`, `autoplay`), not API use.
- Not changed by instruction: CSP, HSTS, TLS minimum, Clear-Site-Data.

**Tests.**
- New `internal/panel/web/web_headers_test.go` `TestSecurityHeadersOnEveryAnswerClass` (table; `wantHeaders`, i.e. the
  full set + `no-store` + no Server + no HSTS over plain HTTP): 303 to /login, 303 after a POST (snooze), 400, 401 and
  200 (token) of /confirm, 429 of /confirm (router's own limiter), 403 of sameOrigin, 403 of a refused confirmation
  (delete without token), 405 (GET /logout), 413, 421 (router wrapped as `servers.go:228` wraps it), /alerts/count,
  status fragment, connections fragment, `/jobs/{id}/log`, the salvage download (reachable without an agent through the
  fake orchestrator of `promotedEnv`), static 404 and `/static/`. `TestCertDownloadHeaders`: the certificate download.
- `internal/panel/tlsui_test.go` `TestPlainHTTPRedirectHeaders`: the plain-HTTP 301 carries the same security headers as
  the certificate page served there, `no-store`, and the new Permissions-Policy (passes before and after except for the
  policy; it is coverage).
- Updated: `web_test.go` `baseHeaders` (new policy), `TestSecurityHeaders` and `TestSSEStreamsStatus` expect `no-store`
  on /events.
Commands: `go test ./internal/panel/web/ -run 'TestSecurityHeaders|TestCertDownloadHeaders|TestSSEStreamsStatus' -v`;
`go test ./internal/panel/ -short -run TestPlainHTTPRedirectHeaders -v`.

RED (excerpt; every class failed on Permissions-Policy, plus):
```
--- FAIL: TestSecurityHeadersOnEveryAnswerClass (0.11s)
    web_headers_test.go:93: 303 to /login: Permissions-Policy = "camera=(), …, clipboard-read=()", want "camera=(), …
    web_headers_test.go:93: static 404: Cache-Control = "", want "no-store"
    web_headers_test.go:93: static directory: Cache-Control = "", want "no-store"
--- FAIL: TestCertDownloadHeaders (0.04s)
--- FAIL: TestSecurityHeaders (0.09s)
    web_test.go:816: GET /events: Cache-Control = "no-cache", want "no-store"
```
GREEN:
```
--- PASS: TestSecurityHeadersOnEveryAnswerClass (0.12s)
--- PASS: TestCertDownloadHeaders (0.04s)
--- PASS: TestSecurityHeaders (0.09s)
--- PASS: TestSSEStreamsStatus (0.05s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/web	0.317s
--- PASS: TestPlainHTTPRedirectHeaders (0.00s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel
```
One commit for S6 (code and tests together).

---

## Gates

- Gate 1 `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 120m` → `afw-gate1.log`, last lines:
  `ok .../internal/agent/drivers/mysql/testmysql 20.811s`, `ok .../internal/agent/monitor 4.620s`, `ok .../internal/proto (cached)`, `EXIT=0`; no `--- FAIL`.
- Gate 2 `go test ./internal/panel/... -race -short -timeout 120m` → `afw-gate2.log`, last lines:
  `ok .../panel/orchestrator 243.373s`, `ok .../panel/web 199.533s`, `EXIT=0`; no `--- FAIL`.
- Gate 3 `make test` → `afw-gate3.log`: STARTED, NOT FINISHED when the handback was forced (only `ok .../cmd/dbpanel` so far, no failure). Result unknown.
- Gate 4 `make test-e2e` → `afw-gate4.log`: NOT RUN (gate 3 still running; one test process at a time).
- Not pushed (the push waits for green gates).

Also: `go vet ./...` clean; `gofmt -l internal cmd` empty; `bash scripts/tailwind.sh` → app.css unchanged;
`graphify update .` run.

---

## Texts not in the plan

All added to the plan's section "Teks baru untuk dibaca pemilik", new sub-section "Gelombang perbaikan audit keamanan",
each marked "tidak ada di rencana" (commit d01422b):
1. Master page, box "Yang bisa dilakukan sekarang": step 1 changed, new step 3, old steps 3-5 and the note removed (S1).
2. README "Pemantauan", master statuses sentence (S1).
3. Alert `Master tidak dipin` (and its Telegram text): unknown pin shown as `tidak dikenal (…)`, one line, ≤32 characters (S2).
4. `Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master.` (S4, job page and refusal).
5. README "Promote" paragraph (S4).

---

## Noticed outside the wave (not changed)

- **Flaky test at base: `TestBackupScanOneAtATime`** (`internal/agent/monitor/backup_test.go:477`, "scans = [], want
  one while it hangs"). Reproduced at b950757 in a separate worktree: 2 failures in 40 full-package runs (0 in 300
  isolated runs); on head 1 in 30. Not touched by S5 (folder `/srv/a` is short; ordering of checks irrelevant to it). It
  looks like a race between the scan goroutine and the fake's call record; it can hit gate 1 or gate 3.
- The detach route's instruction to open a detached replica with `SET PERSIST super_read_only=OFF; … read_only=OFF`
  (`README.md:1388`, `handlers_mysql.go:208`, `replica_detail.html:218`) is a way to make a former replica writable;
  legitimate for a deliberate detach, but the owner may want to say "only when the master is not used any more".
- `web/templates/_status.html` down-guide step 2 (`Jangan me-restart replika: …`) was kept word for word; "langkah
  berikut" now refers to the promote.
- S4's lock-window exception keeps a gate-less, password-less Ulangi for a planned promote for up to ~31 minutes after
  a failure whose unlock was not confirmed (see S4). README "Prasyarat" still says "Tidak ada tombol untuk melewati
  gerbang"; that sentence is not exactly true in that window (it was not true before either).
- PC-1's planned fallback (confirmation dialog + gate for that window) would need a decision on K4 and job-page UI work.

## Self-review

- Every item has a failing test first; RED outputs kept. S5's first allocation test passed before the fix (wrong path
  shape) and was replaced before the implementation; S6's plain-HTTP 301 test is coverage, not RED.
- No existing check or refusal weakened. Tests changed only where they asserted the old behaviour the items change
  (S1 box SQL, S3 http test origin → https, S4 Retry of failed promotes, S6 no-cache/policy); each keeps what it
  guarded for the cases that still apply.
- Test-only export (`internal/panel/monitor/export_test.go`) is a `_test.go` file, not a production switch.
- S4 is the riskiest: the refusal is placed after `refuseSuperseded` and before the gate/token, reads the job's params
  and promotion only, sends nothing. The lock-window rule reuses the exact deadline arithmetic of
  `refuseWhileLockMayStand` (moved into `lockMayStandUntil`), so the two never disagree.
- S3: chose https always (auditor's fix); safe because no state-changing route is served over plain HTTP.
