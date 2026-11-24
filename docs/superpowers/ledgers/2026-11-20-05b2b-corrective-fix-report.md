# Corrective fix R46: keep Ulangi for a failed planned promote; refuse it only for an emergency one

Status: DONE. Branch `claude/install-superpowers-plugin-ev2k9i`, base 5fb84df, pushed (5fb84df..bc71feb).

Commits:
- dcdfacf `fix(panel): keep Ulangi for a failed planned promote; refuse it only for an emergency one`
- bc71feb `docs(plan): owner texts for the planned promote's Ulangi kept`

## What changed (file:line at bc71feb)

Production code:
- `internal/panel/orchestrator/promote.go:1725-1734` `promoteRetryRefused(j store.Job) bool`: now a plain function. It is true only for
  `KindPromoteEmergency` with readable params and `!PromoteSent && !PastPoint`. The planned branch is gone, and so is
  the store read and the error return it needed.
- `promote.go:1739-1745` `RetryRefused`: same contract ("" when the job cannot be read or is not refused). The
  `log.Warn` for a store error is gone because the check no longer reads the store.
- `promote.go:1710-1713` `ErrPromoteRetryBeforePoint`: the text has not changed. Only the comment changed: it now says the error is for emergency jobs.
- `promote.go:413-432` `refuseWhileLockMayStand`: the deadline arithmetic is back inline, byte for byte as before
  f2514a2. `lockMayStandUntil` is removed, because nothing else used it.
- `internal/panel/orchestrator/runner.go:994-996` `Retry`: `if promoteRetryRefused(j) { return ErrPromoteRetryBeforePoint }`.
  It is in the same place as before (after `refuseSuperseded`, before the gate).
- N2: `promote.go:536` `promoteCmdErr` now returns `oldPromoteAgent{name}` (`:542-554`). Its `Error()` is the old text
  letter for letter. Its `Unwrap()` returns `stepError(text)`, so `jobRun.fail` still uses the text as the whole step
  message. `promoteAgainErr` (`:557-563`) rewrites the text for an emergency job only, and is applied at the two
  failure points of `promoteNewMaster` that come before the switch: `peerCred` (`mysql.tlsTrust` on the candidate, `:746`) and the
  `mysql.promote` send (`:784`).
- `internal/panel/web/handlers_jobs.go:145-148` and `handlers_mysql.go:55-58`: comments only ("an emergency promote").
  The web handler and the template are unchanged from f2514a2. The banner still shows whenever `RetryRefused` is
  non-empty, which now happens only for emergency jobs.

Tests:
- Restored exactly to their state before f2514a2 by reverse-applying f2514a2's hunks: `gate_enforce_test.go`
  `TestRetryGatedOnlyWhenDataIsDropped/promote` (Retry of a failed planned promote with a red row is accepted, not
  gated), `promote_test.go` `TestPromotePlannedCatchUpTimeoutUnlocks` (the relock assertion
  `lockForPromote == 2 && unlockPromote == 2` is back), `TestPromoteFailureAtNewMasterUnlocksOrGoesPartial` (the second
  attempt is Ulangi again), and `runner_test.go` `TestPromoteRetryStillTokenless` (a planned job failed before the send,
  retried tokenless, relock ran).
- `promote_retry_test.go`:
  - `TestPromoteRetryRefusedText` and `TestPromoteEmergencyRetryBeforePointRefused`: kept unchanged. They cover the
    refusal, that no agent command is sent, the mode refusal, the tick refusal, and a new emergency start that succeeds.
  - New `TestPromotePlannedRetryLiftsKeptLock` (the N1 scenario): a planned job fails in "Replika mengejar" and its
    unlock goes unanswered. `now` is moved to the lock deadline + grace + 1 h, and the master's agent refuses a second
    lock ("sudah dikunci untuk promosi"). Then `RetryRefused` is "", there is no token and no gate, and Retry is
    accepted. The relock is sent and refused, and the failure path sends `mysql.unlockPromote` with the candidate's
    credential (10.0.0.2). The job records `LockUnlocked`, and no `mysql.promote` is sent.
  - `TestPromoteRetryPastPointStillRuns`: now has two subtests. `planned` is the old body. `emergency` is new: the
    answer to `mysql.promote` is dropped, so the job is partial with `PromoteSent && !PastPoint`; Ulangi is offered,
    tokenless and not gated, and sends `mysql.promote` again; the job succeeds. This also covers the boundary state the
    re-check said had no test.
  - New `TestPromoteOldAgentTextFollowsRetry` (N2): the candidate's agent does not know `mysql.promote`. For an
    emergency job the error ends "… lalu jalankan promote lagi." and Retry is refused. For a planned job it ends
    "… lalu Ulangi." and Retry is offered.
  - Removed `TestPromotePlannedRetryBeforePointRefused` and `TestPromotePlannedRetryWhileLockMayStand`. Both asserted
    the planned refusal or the lock window, which no longer exists.
- `internal/panel/web/web_jobs_test.go`: `TestPromoteRetryStillTokenless` now runs `tokenlessRetry` for a planned job
  with `{}` (failed before the send, as before f2514a2), a planned job past the point, and an emergency job with
  `promote_sent`. `TestPromoteRetryRefusedHidesButton` is narrowed to `KindPromoteEmergency`.

Texts and docs: `README.md:1527-1531, 2897-2904, 3444-3447, 4290-4293`; `web/templates/panduan.html:91`; plan section
"Teks baru untuk dibaca pemilik" (end of
`docs/superpowers/plans/2026-11-20-05b2b-gerbang-prasyarat-panduan.md`).

## Planned jobs compared with the code before f2514a2

For a planned promote job, `Retry` behaves exactly as it did before f2514a2: `promoteRetryRefused` returns false at
its first line, without reading anything. No token, no gate, the same relock in `promoteNewMaster` and `promoteLock`,
the same failure-path unlock (`promoteUndo`), and the same `refuseWhileLockMayStand`, which is now textually identical
to f2514a2^. Run `git diff f2514a2^ -- promote.go runner.go` to check.

These lines are not identical to f2514a2^, and the differences do not affect a planned job:
- `runner.go:994-996`: the call to `promoteRetryRefused`. It is a no-op for planned jobs and is kept for emergency jobs.
- `promote.go:1710-1745`: `ErrPromoteRetryBeforePoint`, `promoteRetryRefused`, `RetryRefused`, kept for emergency jobs.
  The job page asks `RetryRefused` for every failed or partial job; for a planned job it answers "".
- `promote.go:536-563`: `oldPromoteAgent` and `promoteAgainErr` (N2). A planned job gets the same type, and its text
  is unchanged ("… lalu Ulangi.").
- `handlers_jobs.go` and `job_detail.html`: the `RetryRefused` field and banner from f2514a2 remain. They are never
  set for a planned job.

## RED (production code at 5fb84df, tests changed) — `cfw-red.log`

```
--- FAIL: TestRetryGatedOnlyWhenDataIsDropped (1.86s)
        gate_enforce_test.go:368: Retry of a failed promote with a red row: Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master.
--- FAIL: TestPromotePlannedRetryLiftsKeptLock (0.55s)
    promote_retry_test.go:148: RetryRefused of a planned promote = "Promote ini gagal sebelum master diganti. \"Ulangi dari tahap ini\" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master.", want none
--- FAIL: TestPromoteOldAgentTextFollowsRetry (1.01s)
        promote_retry_test.go:270: error = "Agent di cand versi lama dan belum mengenal promote. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu Ulangi."
        promote_retry_test.go:287: RetryRefused = "Promote ini gagal sebelum master diganti. \"Ulangi dari tahap ini\" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master.", want none
--- FAIL: TestPromotePlannedCatchUpTimeoutUnlocks (0.61s)
    promote_test.go:350: Retry of a failed promote before the point of no return: Promote ini gagal sebelum master diganti. ...
--- FAIL: TestPromoteFailureAtNewMasterUnlocksOrGoesPartial (0.43s)
    promote_test.go:880: Retry: Promote ini gagal sebelum master diganti. ...
--- FAIL: TestPromoteRetryStillTokenless (0.56s)
    runner_test.go:245: Retry of a promote without confirmation: Promote ini gagal sebelum master diganti. ...
FAIL	github.com/husmin-fajrin/db-panel/internal/panel/orchestrator	7.253s
EXIT=1
```
In the same run, these passed at the base: `TestPromoteEmergencyRetryBeforePointRefused`, `TestPromoteRetryRefusedText`,
and both subtests of `TestPromoteRetryPastPointStillRuns`. That is expected, because the emergency rule and the
past-point rule are unchanged.

## GREEN — `cfw-green.log`

```
--- PASS: TestRetryGatedOnlyWhenDataIsDropped (1.97s)   (resync, promote, rebuild_approval)
--- PASS: TestPromoteRetryRefusedText (0.00s)
--- PASS: TestPromoteEmergencyRetryBeforePointRefused (0.69s)
--- PASS: TestPromotePlannedRetryLiftsKeptLock (0.60s)
--- PASS: TestPromoteRetryPastPointStillRuns (1.17s)    (planned, emergency)
--- PASS: TestPromoteOldAgentTextFollowsRetry (0.91s)   (emergency, planned)
--- PASS: TestPromotePlannedCatchUpTimeoutUnlocks (0.77s)
--- PASS: TestPromoteFailureAtNewMasterUnlocksOrGoesPartial (0.76s)
--- PASS: TestPromoteRetryStillTokenless (0.77s)
ok  	github.com/husmin-fajrin/db-panel/internal/panel/orchestrator	8.819s
EXIT=0
```

## N2

The text can appear on an emergency job whose Ulangi is now refused. In `promoteNewMaster`, `mysql.tlsTrust` on the
candidate (through `peerCred` → `trustCAOn`) and the `mysql.promote` send both go through `promoteCmdErr`. An unknown
command is a definite refusal, so the job ends "gagal" with `PromoteSent` false. For emergency jobs those two points
now end with "lalu jalankan promote lagi." Every other place keeps "lalu Ulangi.", because Ulangi exists there:
planned jobs, the planned-only `mysql.lockForPromote`, and the steps after the switch (`mysql.quarantine`,
`mysql.repoint`, `OnOnline`).

One edge case: if, after such a refusal, the panel cannot confirm that the candidate is read-only and its job has
ended (the candidate's agent is unreachable when asked), the emergency job ends "selesai sebagian" and Ulangi is
offered. The text then says "jalankan promote lagi", which is still a true description of what Ulangi does there.

## Operator texts changed (exact)

- Step error, emergency promote, agent does not know a promote command, before the switch:
  `Agent di %s versi lama dan belum mengenal promote. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu jalankan promote lagi.`
  (unchanged elsewhere: `… lalu Ulangi.`)
- Job page and Retry refusal: unchanged, now shown only for emergency jobs:
  `Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master.`
- README "Promote" paragraph (2897-2904):
  `Bila promote darurat gagal sebelum master diganti (replika terpilih belum bisa ditulis dan tidak ada yang berubah), halaman pekerjaannya tidak menawarkan **Ulangi dari tahap ini** dan menulis "Promote ini gagal sebelum master diganti. "Ulangi dari tahap ini" tidak tersedia untuk keadaan ini: mulai promote baru dari halaman master." Promote baru menilai lagi jenisnya dan meminta lagi centang dan kata sandi. Bila promote terencana gagal, **Ulangi dari tahap ini** tetap ada: ia melanjutkan pekerjaan itu tanpa menilai lagi daftar **Prasyarat** dan tanpa meminta kata sandi lagi.`
- README "Prasyarat" (1527): `Tidak ada tombol untuk melewati gerbang.` →
  `Tidak ada tombol untuk melewati gerbang saat tindakan terencana dimulai. Satu pengecualian: **Ulangi dari tahap ini** pada promote terencana yang gagal melanjutkan promote itu tanpa menilai lagi daftar **Prasyarat**.`
- Guide "Memperbaiki baris merah" (guide 8), changed in README 3444 and `panduan.html:91` together: `Tidak ada tombol untuk melewati baris merah.` →
  `Tidak ada tombol untuk melewati baris merah saat tindakan terencana dimulai. Satu pengecualian: **Ulangi dari tahap ini** pada promote terencana yang gagal tidak menilai lagi barisnya.`
  (followed by the unchanged `Promote darurat tidak pernah ditolak oleh sebuah baris.`). `TestGuideMatchesREADME` passes. Guides 5 and 7 are untouched.
- README "Keamanan" (4290-4293): `Tidak ada tombol untuk melewatinya.` →
  `Tidak ada tombol untuk melewatinya saat tindakan itu dimulai; satu pengecualian: **Ulangi dari tahap ini** pada promote terencana yang gagal tidak menilai lagi barisnya.`
- Plan, "Teks baru untuk dibaca pemilik": the f2514a2/d01422b entry for the job page now says it applies to emergency
  promotes (planned promotes keep `Ulangi dari tahap ini`). The README "Promote" entry is replaced with the new
  paragraph. Four entries are added (step error, README Prasyarat, guide 8, Keamanan), each marked "tidak ada di rencana".

## Gates (on bc71feb; logs in (scratch directory))

- `go vet ./...`: clean. `gofmt -l internal cmd`: prints nothing. `graphify update .` was run.
- Gate 2, `cfw-gate2.log` (`go test ./internal/panel/... -race -short -timeout 120m`), no `--- FAIL`:
  ```
  ok  	github.com/husmin-fajrin/db-panel/internal/panel/orchestrator	260.740s
  ok  	github.com/husmin-fajrin/db-panel/internal/panel/redact	(cached)
  ok  	github.com/husmin-fajrin/db-panel/internal/panel/store	(cached)
  ok  	github.com/husmin-fajrin/db-panel/internal/panel/web	213.412s
  EXIT=0
  ```
- Gate 3, `cfw-gate3.log` (`make test`). The agent MySQL package was not cached and ran in full (3476.698s). No failure:
  ```
  ok  	github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql	3476.698s
  ...
  ?   	github.com/husmin-fajrin/db-panel/web	[no test files]
  ok  scripts/install_test.sh
  EXIT=0
  ```
- Gate 4, `cfw-gate4.log` (`make test-e2e`):
  ```
  go test -timeout 30m -run TestE2E ./internal/panel/
  ok  	github.com/husmin-fajrin/db-panel/internal/panel	1132.253s
  EXIT=0
  ```
No gate was rerun, and no known timing failure occurred.

## Noticed outside this change

- The re-check's point 5 remains as the ruling leaves it: an emergency job that is partial with `PromoteSent && !PastPoint` keeps a
  tokenless Ulangi that sends `mysql.promote` again at any later time. The new emergency past-point subtest pins this
  behaviour; it does not change it.
- The lock window can still be re-armed by pressing Ulangi on the planned job (out of scope, as before f2514a2).
  `refuseWhileLockMayStand` and README 1449-1462 describe it, and they are unchanged.
- With the planned Ulangi back in every state, PC-1's planned half is open again by decision K4/R46. The new README
  sentences now state it: no Prasyarat re-check and no password on that Ulangi.
