# Re-check of the corrective change R46 (5fb84df..bc71feb) — S4 / N1 / N2 / N3

Read-only review of commits dcdfacf and bc71feb against my earlier notes (`audit-fix-recheck-core-agent.md`).
Head bc71feb, working tree clean before and after. Two targeted runs, one at a time, nothing else running:
`go test ./internal/panel/orchestrator -short -count=1 -run 'TestPromotePlannedRetryLiftsKeptLock|TestPromoteOldAgentTextFollowsRetry|TestPromoteEmergencyRetryBeforePointRefused|TestPromoteRetryPastPointStillRuns|TestPromoteRetryStillTokenless|TestPromotePlannedCatchUpTimeoutUnlocks'`
→ all six PASS, exit 0; `go test ./internal/panel/web -short -count=1 -run 'TestGuideMatchesREADME|TestPromoteRetryStillTokenless|TestPromoteRetryRefusedHidesButton'`
→ all three PASS, exit 0.

## a. Emergency refusal — ADDRESSED

- In the orchestrator, not only on the page: `promoteRetryRefused` (`orchestrator/promote.go:1725-1734`) is true
  for `KindPromoteEmergency` with readable params and `!PromoteSent && !PastPoint`; `Retry` returns
  `ErrPromoteRetryBeforePoint` on it at `runner.go:994-996`, under `startMu`, after `refuseSuperseded` and before
  the gate, the token and `ReopenJob` — the same place as at 5fb84df. Nothing before it sends an agent command for
  a promote job (unchanged from my earlier point 1). `TestPromoteEmergencyRetryBeforePointRefused` is unchanged
  and passes (refused, nothing sent, mode refusal, tick refusal, new start succeeds).
- Because an emergency job only ends "gagal" with `PromoteSent` false (`promoteFailed`, reset at the "agent
  restored it" branch; `resumePromote`), every "gagal" emergency promote is refused.
- No other way in: `Retry` and `Approve` have one caller each, the two web routes (`web/router.go:242-243`);
  `Approve` answers `errNotWaiting` because no promote step ever waits; `/retry/prepare` only redirects for a job
  without a wipe list; startup resume fails or marks partial and launches nothing; `OnOnline` only sends a pending
  quarantine or repoint. Params that cannot be read are refused earlier by `promoteSupersededBy`.
- Unchanged and still open by the ruling's boundary (my earlier point 5): an emergency job that is "selesai
  sebagian" with `PromoteSent && !PastPoint` keeps its tokenless Ulangi. The new subtest
  `TestPromoteRetryPastPointStillRuns/emergency` now pins that behaviour.

## b. Planned behaviour — ADDRESSED (same as before f2514a2; the implementer's claim holds)

- `git diff f2514a2^..bc71feb -- promote.go runner.go` shows only: the three-line call in `Retry`
  (`runner.go:994-996`), the appended block `ErrPromoteRetryBeforePoint` / `promoteRetryRefused` / `RetryRefused`
  (`promote.go:1710-1745`), and the N2 change (`promote.go:536-563`, `:746`, `:784`). `refuseWhileLockMayStand`
  (`promote.go:390-447`) is byte-identical to f2514a2^ again; `lockMayStandUntil` is gone and nothing refers to it.
- Each difference, for a planned job:
  1. `runner.go:994-996` — `promoteRetryRefused` returns false at its first line for any kind but emergency
     (`promote.go:1726-1728`); no store read, no error path. No effect.
  2. `RetryRefused` — the job page asks it for every failed or partial job (`web/handlers_jobs.go:340-344`); for a
     planned job it is always "", so the button and page are as before f2514a2. One extra read of the job row.
  3. `promoteCmdErr` now returns the type `oldPromoteAgent` instead of a `stepError`. Its `Error()` is the old
     text letter for letter and it unwraps to a `stepError`; the only place that recognises a step error is
     `errors.As` in `jobRun.fail` (`runner.go:1869`), which finds it through `Unwrap` — the same pattern as
     `oldAgentError` (`tls.go:166`) and `outdatedAgentError` (`oldmaster.go:360`). The other consumers of that
     error use only its text or `errors.Is` against hub codes it never matched (`promoteQuarantine`,
     `repointReplica`, `promoteUndo`, `sendQuarantine`). `promoteAgainErr` returns the error untouched unless the
     job is an emergency one (`promote.go:557-563`). No effect; `TestPromoteOldAgentTextFollowsRetry/planned`
     asserts the unchanged ending and that Ulangi is offered.
  I found no fourth difference. So a planned job's Ulangi is again: no token, not gated, relock when the lock is
  not held, failure-path unlock — in every state, at any time.
- Tests: `gate_enforce_test.go`, `promote_test.go` and `runner_test.go` are byte-identical to f2514a2^ (empty
  diff), so what they pinned before the wave is pinned again, including the relock assertion my earlier notes
  listed as lost.

## c. N1 — ADDRESSED

- Trace of my scenario (planned job failed before the send, unlock not confirmed, guard kept the lock, more than
  deadline + 60 s later): `Retry` → `promoteRetryRefused` false → no gate (`gateActionForJob` has no promote kind)
  → no token (`dropsData` false) → reopen → the failed step runs again. Whichever step failed ("Kunci master",
  "Replika mengejar" through `relock`, or "Jadikan master baru" through `promoteCatchUp` at `:756-760` because
  `!locked() && !PromoteSent`) the first agent command is `mysql.lockForPromote`; the master's agent refuses it
  while its lock stands; `fail` → `promoteFailed` → `promoteUndo` sends `mysql.unlockPromote` with the candidate's
  credential, and the agent opens the master when it can read the candidate and the listed replicas. One click,
  as before f2514a2. No time condition is left anywhere on this path.
- `TestPromotePlannedRetryLiftsKeptLock` (`promote_retry_test.go:115-180`): it sets up exactly that state (unlock
  unanswered, `LockTried && !LockUnlocked`, a stored deadline), moves the clock to deadline + grace + 1 h, makes
  the master refuse a second lock, and asserts: `RetryRefused` empty, no token, not gated, `Retry` accepted, a
  second `lockForPromote` and a second `unlockPromote` sent, the unlock carrying the candidate's address,
  `LockUnlocked` recorded, no `mysql.promote` sent. It failed at 5fb84df (report's RED) and passes now.
  It proves the panel's half of its name: that the panel asks the master's agent to lift the lock in this state.
  That the agent then lifts it is the fake's answer (`reply(..., nil)`); the real behaviour is the agent's
  `unlockPromote` (`agent/drivers/mysql/promote_lock.go:438-497`), which this diff does not touch and which I read
  for the earlier notes. Good enough for N1; the name slightly overstates what a fake-agent test can show.

## d. N2 and N3 — PARTLY (N2 true; N3 true about the promote, but "one exception" is not exact)

**N2 — true.** `promoteAgainErr` (`promote.go:557-563`) changes the ending to "lalu jalankan promote lagi." only
for an emergency job at the two points of `promoteNewMaster` that precede the switch and go through
`promoteCmdErr`: `mysql.tlsTrust` on the candidate (`:746`) and the `mysql.promote` send (`:784`). An unknown
command is a definite refusal, so that job ends "gagal" and its Ulangi is refused — text and button agree. Every
other producer keeps "lalu Ulangi." and Ulangi exists there: any planned job, the planned-only
`mysql.lockForPromote` (`:653`), and the steps after the switch (`mysql.quarantine`, `mysql.repoint`).
`TestPromoteOldAgentTextFollowsRetry` asserts both endings together with the button state.
One corner (Info): when the candidate cannot be asked afterwards, the emergency job ends "selesai sebagian"
with the "jalankan promote lagi" text while Ulangi is the only way on; a new promote is then refused with
"promote sebelumnya … belum tuntas; buka pekerjaannya dan tekan Ulangi", which sends the operator back to the
right button.

**N3 — the statements about the promote are true; the word "Satu" is not.**
- README 2897-2904 (Promote): true. An emergency promote that failed before the switch (status "gagal") offers no
  Ulangi and shows the sentence; a new promote reads the mode again and asks for the tick and the password (also
  the typed master name, not mentioned, not false). A failed planned promote keeps Ulangi, which continues the job
  without the Prasyarat list and without the password — exactly what the code does (point b). README 1449-1462
  (press Ulangi on the planned promote to confirm the lock open) is true again at any time.
- README 1527-1531 (Prasyarat), 3444-3447 and `web/templates/panduan.html:91` (guide 8, identical;
  `TestGuideMatchesREADME` passes), 4290-4293 (Keamanan): "no button past the gate when a planned action starts"
  is true (every gated start is judged: `prepare_master.go:65`, `create_replica.go:184`, `resync.go:84`,
  `change_databases.go:114,153`, `promote.go:298`), and what they say about the planned promote's Ulangi is true.
  But each calls it the one exception ("Satu pengecualian" / "satu pengecualian"), and it is not the only Ulangi
  that runs on without judging the rows: Retry is judged only for a job whose remaining steps drop data
  (`JobGateInput`, `runner.go:733-743`; `gateActionForJob` `:709-724`). Ulangi of **Siapkan master** (no gate
  action at all), of **Tambah database** with nothing to drop (`dropsData` false, `:664-665`), and of **Buat
  replika** once its copy is made (`:657-661`) continue a gated action with a row red as well; for Tambah database
  the load itself still lies ahead. README 1525-1527 already says the re-judging is for "pekerjaan yang membuang
  data", so the paragraph now holds both "only data-dropping jobs are judged again" and "one exception".
  Severity Rendah (wording; no behaviour). Smallest fix, same in all four places: say the class instead of the
  count, e.g. "**Ulangi dari tahap ini** pada pekerjaan yang tidak membuang data tidak menilai lagi daftar
  **Prasyarat**; itu termasuk promote terencana yang gagal, yang dilanjutkan tanpa penilaian itu."
- Plan section "Teks baru untuk dibaca pemilik": the two earlier entries are corrected (emergency only; new README
  paragraph) and four entries added (step error, README Prasyarat, guide 8, Keamanan), each "tidak ada di rencana".

## e. Newly broken or newly opened by this diff — none broken; one thing reopened by decision

- Nothing in the diff weakens a check for the emergency kind, and no production switch for tests was added. The
  job page and template are unchanged from f2514a2; the banner can now appear only on an emergency job.
- Reopened on purpose (R46, K4; Info here, the open decision for the owner): PC-1's planned half is back whole.
  Ulangi of a failed planned promote runs the promotion on with no Prasyarat list, no backup tick and no
  password, with no time limit while it is the topology's latest promotion. The master's agent (lock) and the
  candidate's agent (old master must answer, be locked, candidate caught up) still guard, so this path does not
  make two masters by itself. README now says so in four places.
- Rendah: the "one exception" wording (point d).
- Info: after a refused POST of an emergency job's Ulangi the sentence still appears twice (error flash and info
  banner), as noted before.

## Out of scope (code this diff does not touch)

- Emergency job "selesai sebagian" with `mysql.promote` sent and the outcome unknown: tokenless Ulangi that sends
  it again at any later time (earlier point 5); remains the open part of PC-1 for the emergency kind.
- With the planned Ulangi back, each Ulangi that reruns "Kunci master" and fails with the unlock unconfirmed again
  pushes back the time from which an emergency promote is allowed (`promote.go:651` with `:415-432`); and the
  master agent's lock deadline is still stored unbounded (`:657-658`). Both older than the wave.
- A cancelled planned promote with an unconfirmed unlock is still told to press an Ulangi it does not have; the
  kept-lock alert still advises "Buka kunci master", which the agent refuses under a promote lock. Older texts.

## Verdict

a ADDRESSED · b ADDRESSED · c ADDRESSED · d PARTLY (N2 true; N3 true except "Satu pengecualian", Rendah) ·
e nothing newly broken; PC-1's planned half reopened by ruling R46 for the owner's decision.
