# Review: release fixes (Plan 2 residuals 1 and 2), commits 294f64c, 394df53

Verdict: **Approved**. Spec: item 1 ✅, item 2 ✅. No Critical or Important findings; three Minor findings below.
I traced runner.go (Retry/launch/loop/execute/Cancel/cancelJob/Resume/Close), store.ReopenJob/UpdateJob/ActiveJobForServer,
snapshot.go and the agent's DropUser by reading the code. I did not re-run the tests.

## Item 1: Retry race (runner.go:407-445, execute 372-376)
- Start*, Approve and Retry all take startMu. Retry now does ReopenJob (failed -> running) before it releases startMu,
  so every later ActiveJobForServer/refuseBusy (Start* change_databases.go:76/112, create_replica.go:81) and every other
  Retry on that server already sees the job. Two jobs for one server cannot run at once.
- execute(actRetry) only runs the job when it is running (reopened) or waiting at act.step. A job can only be "running"
  without an active run when the run returned on ctx.Err(), and that case is caught earlier by the userCanceled and
  lj.ctx.Err() checks. The goroutine runs actions one at a time, so a stale actRetry cannot start a second run.
- Side effect, an improvement: a double-clicked Retry used to queue a second actRetry that reopened and re-ran the job
  after a second failure. The second Retry now gets "pekerjaan masih berjalan".
- After a failed job's fail(), setJob(failed) is the last store write (runner.go:836), so reopening while the old
  goroutine is still finishing cannot be overwritten by that old run.
- If launch fails, Retry re-fails the job with its old step and error. UpdateJob does not change a terminal row, so a
  job that is already canceled stays canceled.
- Cancel between reopen and launch, with no live goroutine (the usual case): Cancel sees "running" and starts a cancel
  goroutine, so the job ends canceled. Retry's launch then queues or starts actRetry, which does nothing because the job
  is userCanceled or canceled. The retried step never ran, so the agent has nothing running. A leftover AgentJobID only
  causes a harmless job.cancel on an agent job that already finished.
- Resume, or Close before execute, for a job that was reopened but never launched: RunningJobs picks it up, the job
  fails with resumeError and the replica goes to failed/attention, and it can be retried. This is acceptable.

## Item 2: dump account recorded first (snapshot.go:72-80)
- Both callers, create_replica.go:541 and change_databases.go:279, go through copySnapshot. Name and host are saved
  before createDumpUser. The defer registered at :46 drops the account on failure, cut-off or Cancel (using
  WithoutCancel). Cancel's cancelJob (runner.go:641) and the leftover drop at :51 key on DumpUser.
- The agent runs `DROP USER IF EXISTS` (internal/agent/drivers/mysql/users.go:231), so dropping a never-created
  account succeeds.
- The password is never stored. jobState has no password field; save() runs before p.Password is set; runAgentJob
  persists only the agent job IDs; the logs name only user@host.

## Tests
- TestFailedCreateDumpUserDropsIt and TestCancelDuringCreateDumpUserDropsIt fail on the old code for the right reason
  (old code never called dropUser, so the waitFor times out). Both use condition polling plus a channel gate rather than
  sleeps only. Cancel's second drop is excluded deterministically: the defer clears DumpUser before cancelJob reads it.
- In TestRetryReopensBeforeNewJobs, part 2 (superseded_test.go:176-209, a held liveJob) is the deterministic proof and
  fails on the old code at wantRefusedNew. Part 1 is racy; see M1.

## Findings
**M1 Minor: test flake risk.** superseded_test.go:167-173. In part 1 the retried goroutine runs unhindered and
setReadOnly fails at once. That races the test's ActiveJobForServer and the 7 store queries that StartCreateReplica
makes before refuseBusy, all on a single-connection SQLite store. If the scheduler lets the goroutine re-fail first
(likely only under -race or a loaded CI), ActiveJobForServer reports not busy. StartCreateReplica can also succeed:
it removes the failed replica and creates a second job, which makes the test fail falsely.
Fix: gate setReadOnly on a channel during part 1's checks and close it after wantRefusedNew(), or delete part 1,
since part 2 covers the requirement.

**M2 Minor: Cancel can be lost in a narrow window.** runner.go:432-439. Retry gets errBusy on a just-reopened failed
job only when the old goroutine was still finishing and a Cancel queued actCancel between ReopenJob and launch. If
Retry's re-fail UpdateJob lands before cancelJob, the job ends failed, not canceled, even though Cancel returned nil.
Cancel's cleanup (stopReplicaAfter, RemoveReplica) is then skipped. Nothing runs on the agent and the job stays
retryable, which is acceptable.
Optional fix: re-fail only on errClosed. On errBusy the queued action is that Cancel, which then cancels the running job.

**M3 Minor: misleading log when the master is unreachable.** snapshot.go:75-80 with dropDumpAccount :96-100. When
createDumpUser fails because the master is offline, the deferred drop fails too. The job log then tells the operator to
"hapus manual: DROP USER ..." for an account that was almost certainly never created. DumpUser stays recorded, so the
next Retry fails with "akun dump sementara dari percobaan sebelumnya belum bisa dihapus" instead of the offline message
until the master is back. There is no safety impact, because the retry needs the master anyway.
No change needed for release.

## Scope
There is no behaviour change outside the two items, apart from the double-Retry improvement and the reopen-then-Resume
edge, both covered above. gofmt drift in runner.go (the const block) predates this change; the diff adds none.
