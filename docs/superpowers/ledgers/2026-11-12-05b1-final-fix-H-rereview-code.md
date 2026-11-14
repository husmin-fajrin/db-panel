# Final fix wave, part H (panel code): scoped re-review of 82b9df0..8b0389d

**Method.** I read the code at head 8b0389d, starting each question with `graphify query`. I also read the agent code the study table depends on, without changing it: `internal/agent/drivers/mysql/{dump.go,clone.go,snapshot.go,databases.go}` and `internal/agent/jobs/runner.go`. The only thing I ran was one throwaway sanitizer test, `internal/panel/redact/zz_rereview_test.go`, with `-run TestZZRereview`. It has been deleted, and `git status --short` is empty. Line numbers refer to head.

## Verdicts

### N-G1 / H-1d — ADDRESSED (two Minor gaps: N-H1, N-H3; one Minor wrong-direction case: N-H4)

**The study table checked against the agent code.**

Dump, `dump.go:66-155`:
- Everything up to `// From here on local data changes` (`:131`) is read-only or a refusal. That covers the stored account, the client tools, `refuseRunningReplica`, `refuseOnSource`, `checkClients`, and the rebuild's `completeUserDatabases`.
- After that line come `STOP REPLICA`, `liftReadOnly`, `resetBinaryLogs` (only when `gtidMode == "ON"`, `:142-146`) and `listBefore`.
- Only then does it log and call `j.Progress(0, "dump dimulai")` (`:154-155`).
- The only other dump progress is `reportDumpProgress` (`:579-596`), which runs during the load.

CLONE, `clone.go:50-123`:
- First come `refuseRunningReplica`, `refuseOnSource`, `installClonePlugin`, the two `SET GLOBAL clone_*`, `liftReadOnly` and the session read.
- Then `j.Progress(0, "CLONE dimulai")` (`:123`), immediately before `CLONE INSTANCE` is executed.

The rest of the agent side:
- **Other progress calls.** `grep` finds no other `Progress` call in a copy path. Install and promote have their own, but they are different agent jobs, and `handleEvents` filters events by the copy job's id (`agentjob.go:133`). The jobs runner never emits progress by itself (`jobs/runner.go:83-106`).
- **Direct refusals.** Every error answer to the start call is returned by the handler before `Jobs.Start` runs the body (`snapshot.go:49-99`): unmarshal, validation, `d.allow` (pins and strict), `d.host`, a missing job runner, or `Jobs.Start` refusing (busy). So each one is given before any local change.
- **How the panel recognises a refusal.** `promoteStartRefused` treats any `*proto.Error` except timeout and offline as a refusal (`promote.go:685-691`). The hub creates a `*proto.Error` only from the agent's own response, plus the timeout and offline errors (`hub/hub.go:484-500`). A cancelled context or a decode failure is not a `*proto.Error`, so it does not clear the flag.
- **`dropDatabases`.** It runs without binary logging (`databases.go:65`). The leftover check and the salvage read the binary log, so the drop is correctly not counted as evidence.

**Panel side.**
- `markCopySent` runs immediately before each of the three `runAgentJob` sites (`snapshot.go:54`, `:60`, `:106`). That covers CLONE, dump reattach, and a fresh dump after the dump account is created.
- `copyWatch` is set only around `copySnapshot` (`create_replica.go:770-773`). The first progress event of that agent job sets `CopyBegan` (`agentjob.go:139-141`).
- A successful result sets `CopyBegan` (`create_replica.go:786`). That includes a reattach to an earlier job that already succeeded.
- The flag is cleared only when all of these hold (`:777-781`): this attempt set it (`!sentBefore`), there is no evidence, no agent job was attached, and the start call was a refusal answered by the agent.

**Can evidence be missed after data really changed?** Yes, and in every such case the result is UNCERTAIN, not NOT STARTED:
- the event is lost while the agent is disconnected, or the panel restarts before `handleEvents` runs (`Resume` fails the job with `CopySent` stored);
- the job is cancelled;
- the job ends with an error after `resetBinaryLogs` (for example, `listBefore` fails) and sent no progress;
- the answer to the start call is lost (timeout or offline: not a refusal).

Nothing that happens after the command was sent lands in NOT STARTED, with two exceptions:
- **N-H1.** The `CopySent` save fails. `markCopySent` uses the error-swallowing `save()`, so a lost answer then lands in NOT STARTED.
- **N-H3.** A job whose `RebuildPromotionID` is 0 because the store read failed at creation is classified by the old-shape rule, which ignores `CopySent`.

**One case in the wrong direction (N-H4).** On CLONE, "CLONE dimulai" is sent before `CLONE INSTANCE`. That statement can fail while connecting to the donor (for example `Clone Donor Error: 1045 Access denied`, the `clone.go:375` case) before anything is dropped. The job is then STARTED and Periksa is refused although the data is intact. For an old master this is close to unreachable: the copy method is CLONE only when the target has no user databases (comment at `create_replica.go:715-716`).

**Old-shape jobs and unreadable params** (`oldmaster.go:151-205`) follow the brief:
- past the step → STARTED;
- at the step with an agent job id or a snapshot GTID → UNCERTAIN;
- otherwise → NOT STARTED;
- unreadable params → UNCERTAIN, and only for a job created at or after the promotion.

One note on old-shape jobs: `AgentJobID` may be an earlier step's agent job (install). That errs toward UNCERTAIN, which is the allowed direction.

**Sentences.**
- `msgCopyUncertain` (`handlers_oldmaster.go:400-402`) equals the brief's sentence character for character.
- The box shows it as a warning banner above Periksa, only in the plain quarantined state (`server_detail.html:387`).
- The form adds it only when `noCheck == ""` (`handlers_oldmaster.go:1066`). In the failed state the attempt-pending sentence is shown instead, as the brief asks.

**Status advice.**
- `copyStatusAdvice` (`handlers_oldmaster.go:408-421`) replaces the advice only when `Role == quarantined`, `Code == StatusQuarantined` and the state is STARTED.
- It is applied to the box's status (`:529`) and to `/servers/{id}/status-fragment` (`views_monitor.go:122-125`), which re-renders `data-status-for` in that box. Those are the only places the full status (with the advice) of a quarantined server is shown.
- The fragment logs a store error and keeps the monitor's advice. That fails open, toward offering.

### N-G2 / H-2 — ADDRESSED, with one rough edge (N-H2)

- **Save failure.** `saveErr` is used only at the settle (`create_replica.go:979-990`). On failure the flag is reset in memory and the step fails with `simpan keadaan pekerjaan: …` before the clear. `save()` still logs and swallows the error for every other caller (`runner.go:1673-1677`), so their behaviour is unchanged.
  - Resulting state: flag false, marker kept, agent settled. The job is retryable (`supersededBy`: row failed, role replica, same master). Retry re-runs configureReplication and heals it (`TestSettleSaveFails`).
  - "Batalkan" is still accepted in this state. That is the conservative mismatch already recorded as R-G2.
- **Clear failure.** The flag is stored and the marker kept. The clear now runs on every pass, so a Retry clears it (`TestSettleClearFails`).
- **The new refusal.** `CancelRebuildAttempt` refuses through `latestRebuildEnded` (`controls.go:165-170`, `:185-201`) with the existing text "server ini bukan master lama dengan percobaan bangun ulang yang gagal".
  - **N-H2.** The page does not know about this refusal: `AwaitingRebuild` is still true, so the box and the replica page offer the button, which is then refused with a text that contradicts the box header.
  - Unreadable params count as "not ended", so Batalkan is allowed; the store transaction still checks its own conditions.
- **No stuck states.** No path leaves the agent settled and the job not retryable, except an operator who chooses Batalkan in the save-failure state; a new rebuild then works with `required:false`.

### N-G3 — PARTIALLY ADDRESSED (as far as the brief allowed)

- The status-panel advice is fixed in the web layer (above).
- The monitor's alert texts are unchanged under the brief's rule:
  - `monitor/alerts.go:385` (startLateText);
  - `monitor/promote.go:695` (startUnreadableText).
- So in state STARTED, master_start_late still says "jalankan Periksa data tertinggal" on the page and in Telegram, and it cannot close through a check.

### N-G4 / H-3 — ADDRESSED for the scope given (kept literal followed by another literal)

I enumerated every concatenation of up to 3 of 23 pieces (12,720 inputs), and every byte cut of each. The pieces were:
- kept phrases in both quote characters;
- the `dbpanel_` account with and without its host;
- both advised-account sentences;
- `'Budi'` and `"Budi"`;
- stray `'`, `"`, `''`, `""` and `\`;
- a newline, `é`, a space, `x` and `;`.

**Fixed point.** The round introduced **no** new failure (`newOnly=0`). The total number of failing cuts fell from 36,772 to 23,014. Every remaining failure also fails at 82b9df0. They are the pre-existing censored-then-censored adjacency, for example `'Budi'"Budi"` → `'…''…'` → `'…'`. That case lies outside the brief's "kept literal" scope and is listed under Out-of-Scope. None of them leaks.

**Regressions.** I compared head against the 82b9df0 algorithm, which I copied into the throwaway test.
- 702 outputs differ only by `'…'` becoming `"…"` after a kept literal (intended).
- 72 differ only where an old short lead had kept a pair that is now censored (word-glued or partial lead; intended).
- No other output changed.

**Agent texts.** The three agent texts and the other kept phrases still pass unchanged (`TestQuotesKeepsPanelPhrases`, unchanged and passing per the gate).

### N-G5 / H-3 — ADDRESSED as specified (residual N-H5)

- `accountLeads` (`redact.go:145`) are the agent's sentence parts. They match `rebuild_load.go:208-209` character for character.
- A lead counts only at the start of the message or after a non-word byte (`:156`). `é` and `x` before it are both refused.
- The re-review's four leaking inputs are now censored, as checked by the throwaway test.
- A nested-quote breakout can still keep a pair when the row value itself holds the *whole* Indonesian sentence part. Example: `Unknown column 'x' tidak bisa dipastikan lengkap: akun MySQL 'NIK…'@'budi.santoso' y'` keeps the pair. This is N-H5.
- Templates still win: Duplicate entry and `Query:` values that contain the full prefixes are censored.

### N-G6 / H-4 — ADDRESSED (fallback gap N-H3)

- `RebuildPromotionID` is set from `PromotionByOldMaster(target)` at `StartCreateReplica` (`create_replica.go:203-211`), and jobs are matched by id (`oldmaster.go:178-183`).
- A second promote that makes X old master again creates a newer promotion, so the earlier jobs drop out. A second promote of T that does not involve X does not change X's latest promotion.
- Jobs without the id use the time rule.
- Tests cover "clock stepped back" and "another promotion".

### 8b0389d (TestWSLogsReplacedConnection) — diagnosis correct, assertion not weakened

- `Hub.Attach` calls `OnOnline` inside itself (`hub/hub.go:160`). The handler logs the replacement only after `Attach` returns (`agentapi/ws.go:48-51`). So the test read the log too early.
- The test now polls a `syncBuffer` for up to `wait` (5 s) and asserts the same two substrings (WARN message and `remote=127.0.0.1:`). It is no weaker.
- The product-side ordering is benign: `OnOnline` before a log line. No real bug is hidden.

## New breakage in the fix diff

**Critical: none. Important: none.**

### Minor

**N-H1 — the `CopySent` save failure is swallowed.**
- Where: `snapshot.go:193-199` (`markCopySent` → `save()`, which logs and continues).
- Scenario: the job-params write fails, the copy is still sent, the answer is lost (timeout or disconnect), and the step fails. The stored `CopySent` is false, so the state is NOT STARTED.
- Operator experience: Periksa is offered without the uncertainty warning. If the dump did reset the binary logs, the check reports "Tidak ada data tertinggal" with nothing to signal doubt. That is false reassurance, but it needs a store write failure and a lost answer in the same step.
- Fix: use `saveErr` in `markCopySent` and fail the step before sending when it fails.

**N-H2 — in the "clear failed" state, Batalkan is offered and then refused.**
- Where: `controls.go:165-170` against `server_detail.html:408` and `replica_detail.html:90`, which are gated by `AwaitingRebuild` only.
- Scenario: the settle's `ClearOldMaster` fails, so the flag is stored and the marker kept.
- Operator experience:
  - The box still says "Master lama (dikarantina …)", and the replica page says "masih dikarantina".
  - Batalkan is shown, then refused with "server ini bukan master lama dengan percobaan bangun ulang yang gagal".
  - Resync and Lepas are refused with "masih dikarantina".
  - "Ulangi dari tahap ini" on the job fixes it. That is confusing but not stuck, and it needs a store failure.
- Fix: make the view's predicate include `latestRebuildEnded` (hide Batalkan and the old-master box), or reword the refusal to send the operator to the job's Retry.

**N-H3 — jobs with promotion id 0 ignore `CopySent`.**
- Where: `oldmaster.go:197-202`.
- Scenario: `PromotionByOldMaster` errors at rebuild start, so `RebuildPromotionID` stays 0. The copy is sent and the answer lost, with `AgentJobID` empty. The job is classified NOT STARTED.
- Operator experience: Periksa is offered without the warning. This is the same exposure as N-H1, and just as narrow.
- Fix: treat `js.CopySent` as UNCERTAIN whatever the promotion id is (`case js.CopySent:`).

**N-H4 — CLONE's "CLONE dimulai" counts as evidence before CLONE changes anything.**
- Where: `agentjob.go:139-141` with `clone.go:122-123`.
- Scenario: the CLONE fails at the donor (authentication, network or version) before it drops the local schemas. The job is STARTED.
- Operator experience: the page says the data is deleted, and Periksa and Amankan are refused although the binary log is intact.
- It is close to unreachable for an old master, because CLONE is chosen only for a target with no user databases. But it contradicts the binding principle.
- Fix: for CLONE, require a progress event with a percent above 0, or the result.

**N-H5 — whole-prefix breakout.** This is the residual of N-G5, described above.
- Operator experience: practically never; a row value would have to contain the agent's whole Indonesian sentence part plus a quote.
- Fix, if ever wanted: require the full agent sentence start, `daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL `, at the start of the message or after `": "`.

**Checked and clean:**
- `runner.go`: the `saveErr` split; `lastRefused` set before mapping; the test seams, which are nil in production.
- `agentjob.go`: `startRefused` reset per call.
- `controls.go`.
- `render.go`: one new template function.
- `views_monitor.go`.
- The template line, which is escaped and visible only in the intended state.

## State table, rows for N-G1 and N-G3 (old master X)

Cell abbreviations:
- **attempt-pending:** the sentence telling the operator to press Batalkan first.
- **monitor advice:** the StatusQuarantined advice from the monitor.
- **copy-started sentence:** `CopyStartedText`.
- **Uncertain warning:** `msgCopyUncertain`.

| Copy state of the latest promotion's rebuilds | Failed state (role replica, failed row) | Plain quarantined (after Batalkan or a cancel) |
|---|---|---|
| NOT STARTED: nothing sent, or a direct refusal | attempt-pending sentence; Batalkan offered and accepted; then → right-hand cell | Periksa and Amankan offered and accepted; monitor advice kept; normal form warnings. ✓ |
| UNCERTAIN: sent with no evidence; lost answer; progress lost; cancelled mid-copy; unreadable params; old shape at the step with a job id or GTID | attempt-pending sentence (as state 1) ✓ | Uncertain warning above Periksa/Amankan; both offered and accepted; the same sentence as the form warning; monitor advice kept ✓ |
| STARTED: progress or result received, or past the step | copy-started sentence; no Periksa ✓ | Periksa and Amankan hidden and refused with the copy-started sentence; the status advice replaced by it ✓; **master_start_late text still "jalankan Periksa", and it cannot close (N-G3, by rule)** |

My earlier ✗ rows, with their new cells:
- "Failed AT the copy step, before any drop" (approval expired, agent offline, wait then cancel): the copy command was never sent, or was refused directly. The state is NOT STARTED, so Periksa is available after Batalkan. ✓
- "Attempt cancelled, copy started": ✓, except the monitor alert text noted in the table.
- New ✗ cells: N-H1 and N-H3 can turn an UNCERTAIN case into NOT STARTED (no warning). N-H4 can turn a NOT STARTED CLONE case into STARTED.

The direction of every other remaining error is the safe one. A salvage is never refused, and "data sudah dihapus" is never shown, without a progress event, a result, or the job having passed the step.

## Tests: would they fail without the fix?

**H-1d.** `copy_state_test.go`:
- `NothingSent`, `RefusedDirectly`, `AnswerLost`, `Evidence` and `EarlierEvidenceCounts`;
- the `StoredJobs` table, with old shapes, unreadable params, new shapes, clock stepped back and another promotion.

The report shows RED by substituting the part G rule. The web tests `TestCopyUncertainWording` and `TestCopyStartedStatusAdvice` were a compile failure first; for the advice test, a behavioural RED with the function disabled is also shown.

Missing cases:
- the `markCopySent` save failure (N-H1);
- promotion id 0 with `CopySent` (N-H3);
- a CLONE that fails right after "CLONE dimulai" (N-H4);
- a copy job that ends with an error and sent no progress (the expected result is UNCERTAIN);
- a reattach to a running earlier job;
- the status fragment on a store error.

**H-2.** `TestSettleSaveFails` and `TestSettleClearFails` show RED only as compile failures, because the seams are new. Read against the part G code, both would fail on behaviour: the swallowed save, and the clear guarded by the first pass. Missing: the page in the clear-failure state, which would expose N-H2.

**H-3.** The RED is real (stash). `TestQuotesAdjacentFixedPoint` covers kept-then-other-quote adjacency at every cut. It does not include censored-then-censored adjacency, which still breaks (pre-existing).

**H-4.** Covered by the `StoredJobs` rows.

**8b0389d.** The same assertion, now with a bounded wait.

## Residual risks (operator experience and severity)

- **R-H1 (Minor).** The monitor's master_start_late texts still advise Periksa in state STARTED, and that alert cannot close through a check. The operator sees an alert asking for an action the page does not offer, until the rebuild or the delete.
- **R-H2 (Minor).** UNCERTAIN can be a copy that did reset the binary logs. A "Tidak ada data tertinggal" result is then wrong, but the warning says exactly that.
- **R-H3 (Minor, kept from R-G2).** A lost configure answer after the agent settled still shows an old master, and Batalkan is accepted. That is conservative; a rebuild heals it.
- **R-H4 (Minor, kept from R-G1 and R-G3).** The monitor stays silent in the failed state. A second promote while a rebuild failure is pending is not handled.
- **R-H5 (Minor).** In dump `COMMENTED` mode (a configured replica with a database subset), the agent does not reset the binary log, yet progress still counts as STARTED. I did not find a reachable old-master path to this mode that is not already STARTED from an earlier job. Not fully verified.

## Out-of-scope observations

- **Fixed point, pre-existing.** It still fails for two censored literals in different quote characters placed side by side (`'Budi'"Budi"` → `'…''…'` → `'…'`), on 23,014 of the enumerated cuts. There is no leak; the second pass only merges two tokens.
- **Pre-existing `dbpanel_` host keep.** A `dbpanel_` account followed by `@'Budi'` keeps `'Budi'` as a host. That is by design of the host rule.
- **Unchanged from part G:** `users.go:545` advice is still censored, and `RenewMySQLTLS` still proceeds when `GetServer` errors.

## Merge judgment

No finding blocks a merge.
- The data-loss item (N-G1) is fixed in the safe direction on every path that does not need a store write failure.
- The remaining Minors are narrow (store failures, or CLONE on an old master) or wording.
- What stays untrue is in the monitor texts, which this round was not allowed to change.
