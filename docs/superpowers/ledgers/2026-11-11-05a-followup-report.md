# Follow-up report: R-117 (send order) and R-119 (forget-replica-port semua output)

Status: DONE_WITH_CONCERNS (one scope decision, see "Decisions" 1).
Commits: `bb54853` (code), `000f5b5` (docs). Both are pushed to `claude/install-superpowers-plugin-ev2k9i`.

## Fix 1: read-only first, reports after (R-117)

Changes:
- `internal/agent/drivers/mysql/promote_lock.go:562`: `defer d.sendRoundEvents()` replaces the `d.flushStartEvents()` call that ran right after `promoteMu`. It is deferred after `defer d.promoteMu.Unlock()`. Defers run LIFO, so the send runs under `promoteMu` on every return path of a round that took the lock. A round skipped by `TryLock` sends nothing, as before. The doc comment of `GuardTick` (around :539) says this.
- `internal/agent/drivers/mysql/startlock_guard.go:349-368`: `emitStart` now only queues. Its trailing `d.flushStartEvents()` is gone and its comment is updated. The queue's size rule, drop rule, event shapes and `flushStartEvents` itself are unchanged.
- `startlock_guard.go:372-382`: the new `sendRoundEvents` runs `flushStartEvents()` and then emits the round's `EventQuarantined` notice (`qNotice`) if one is set. The notice is sent once and not kept on failure, which matches the old `d.emit` behaviour.
- `internal/agent/drivers/mysql/quarantine.go:328`: `guardQuarantined` sets `d.qNotice` instead of calling `d.emit(EventQuarantined, …)` inline. The warn log line stays where it was.
- `internal/agent/drivers/mysql/driver.go:114-117`: the new field `qNotice *proto.QuarantinedEvent`.

Resulting order in one round on a quarantined server:
1. `guardQuarantined`: read-only is PERSISTed and reports are queued.
2. `guardStartLock`: housekeeping. Any start-failed report is queued.
3. Send: pending start reports oldest first, then the quarantined notice.

The order between the start report and the quarantined notice is the same as before (start report first). `TestQuarantineIdempotent` pins that order and is unchanged. On a master, the send comes after `ensureLocked` and `guardExpired`.

### Tests (`internal/agent/drivers/mysql/startlock_guard_test.go`)
- `stateAtSend` (:576) is an `eventLog` that, at each delivered Emit, records `@@global.super_read_only` (read through root) and whether the start lock file exists. It observes state, so the test does not depend on timing and has no sleeps.
- `TestGuardSendsReportsAfterReadOnlyAndHousekeeping` (:614):
  - Round 1, offline, writable MySQL: a late report is queued.
  - Between the rounds: RESET PERSIST, restart, then the flags are set OFF by hand and the lock file is removed.
  - Round 2, online: every delivered send must see super_read_only ON and the lock file present. Two late reports go out in this round: the one kept from round 1 and this round's own.
- `TestGuardFlushesStartReportsOnEveryPath` (:661): a late report is kept from an offline quarantined round. The test then switches role or lock and runs one online round. Subtests:
  - `not locked` (replica, no lock): the early return.
  - `role not master` (replica, with lock).
  - `locked master` (master, lock, flags set OFF by hand): the send must see super_read_only ON.
  - `lock unreadable` (garbage lock file): the error return. GuardTick returns an error and the report is still delivered.
  - The quarantined path is covered by the test above and by the existing `TestQuarantineStartReportRetriedUntilDelivered`.

RED (old code, tests only). Command: `go test ./internal/agent/drivers/mysql/ -run 'TestGuardSendsReportsAfterReadOnlyAndHousekeeping|TestGuardFlushesStartReportsOnEveryPath' -timeout 30m -v`
```
startlock_guard_test.go:644: monitor.quarantineStart sent with super_read_only=false, start lock file present=false; want both before any send
startlock_guard_test.go:644: monitor.quarantineStart sent with super_read_only=true, start lock file present=false; want both before any send
startlock_guard_test.go:644: monitor.quarantined sent with super_read_only=true, start lock file present=false; want both before any send
--- FAIL: TestGuardSendsReportsAfterReadOnlyAndHousekeeping (9.32s)
startlock_guard_test.go:713: monitor.quarantineStart sent before the locked master's super_read_only was put back
    --- PASS: TestGuardFlushesStartReportsOnEveryPath/not_locked (5.17s)
    --- PASS: TestGuardFlushesStartReportsOnEveryPath/role_not_master (5.16s)
    --- FAIL: TestGuardFlushesStartReportsOnEveryPath/locked_master (5.18s)
    --- PASS: TestGuardFlushesStartReportsOnEveryPath/lock_unreadable (5.23s)
FAIL	github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql	30.063s
```
This is a behavioural RED, not a build failure. As expected, the three early-return subtests pass on the old code, because the old code flushes at the top of the round. They guard against a naive move of the flush to the end of the round, which would skip the early returns.

My first RED attempt recorded sends that were refused while offline. I fixed the helper to record only delivered sends before taking the RED above.

GREEN, after the fix, together with the neighbouring guard and quarantine tests. Command: `go test ./internal/agent/drivers/mysql/ -run 'TestGuardSendsReports…|TestGuardFlushesStart…|TestQuarantineStartReportRetriedUntilDelivered|TestGuardEnforcesReadOnlyBeforeStartLockHousekeeping|TestQuarantine|TestBootstrapFailure' -timeout 30m -v`. Result: all PASS, `ok … 170.396s`. That includes `TestQuarantineIdempotent` (event order), `TestQuarantineStartEvents` and `TestQuarantineStartReportRetriedUntilDelivered/{late,start_failed}`.

## Fix 2: `forget-replica-port semua` says what it loses (R-119)

Changes:
- `internal/agent/drivers/mysql/promote_files.go:563-621`:
  - The new `ForgottenPorts{Ports, More, Full}` (:587) with `Changed()`.
  - The new `ForgetAllReplicaPorts(path)` (:599), which reads and empties the file in one call. The removed list therefore comes from the same read as the write.
  - `forgetAllSeenPorts` (:609) sorts the ports, caps the list at `maxSeenPorts` (8) and counts the rest in `More`.
  - `ForgetReplicaPort(path, 0)` delegates to it. Its signature and effect are unchanged, and `seen_cap_test.go` is untouched.
- `internal/agent/cli.go`:
  - :51-55: the usage clause.
  - :515-534: `mainForgetReplicaPort` sends `semua` to `forgetAllReplicaPorts`. The single-port message gets one clause.
  - :536-572: `forgetAllReplicaPorts` prints the file path, the removed ports (bounded, with ", dan N port lain"), the full mark if removed, the existing confirmation line, and the caution. An empty record goes to stderr with the path, saying nothing was removed. The exit code stays 1, as before.
- Effect on the file is unchanged: it is emptied and the mark is removed.

Test: `TestForgetReplicaPortCLI` (`internal/agent/cli_test.go`) is extended. Earlier assertions are unchanged, and the only change is additions. The new checks are:
- the single-port output contains the caution phrase;
- the `semua` output names each removed port 20001-20007, the file path, "penuh", "tidak dicatat lagi dengan sendirinya" and "promosi terencana berikutnya";
- an empty record gives exit 1, empty stdout, and stderr with "sudah kosong", "tidak ada yang dihapus" and the path;
- a hand-written file of 10 ports lists 30001-30008, does not list 30009 or 30010, and says "2 port lain";
- the usage text contains the caution clause (whitespace-normalised).
- The test also gained the `fmt` import.

RED. Command: `go test ./internal/agent/ -run TestForgetReplicaPortCLI -timeout 30m`. The first run was a build failure (missing `fmt` import). After adding the import it was a behavioural RED:
```
cli_test.go:893: forget-replica-port 20000 = 0, "port 20000 dihapus dari catatan port replika\n", ""
cli_test.go:909: forget-replica-port semua does not name removed port 20001: "semua port replika dihapus dari catatan; …"
cli_test.go:914: forget-replica-port semua lacks "/tmp/TestForgetReplicaPortCLI…/002/replica-ports-seen": …
cli_test.go:914: forget-replica-port semua lacks "penuh": …
cli_test.go:914: forget-replica-port semua lacks "tidak dicatat lagi dengan sendirinya": …
cli_test.go:924: forget-replica-port semua on an empty record = 1, "", "catatan port replika sudah kosong\n"
cli_test.go:938: forget-replica-port semua of 10 ports = 0, "semua port replika dihapus dari catatan; …", ""
cli_test.go:945: usage of forget-replica-port lacks the caution: …
FAIL	github.com/husmin-fajrin/db-panel/internal/agent	0.012s
```
GREEN. Command: `go test ./internal/agent/ -run 'TestForgetReplicaPortCLI|TestForgetReplicaCLI'`. Both PASS, `ok … 0.015s`. `go test ./internal/agent/drivers/mysql/ -run TestSeen` also passes (`ok`).

## Gates (on the code of bb54853, before the commit)
| Command | Exit | Duration |
|---|---|---|
| `make test` (vet, all packages, `install_test.sh` with shellcheck) | 0 | 2224 s (37m04s); `internal/agent/drivers/mysql` 2180.150s, `internal/panel` 552.171s |
| `make test-e2e` | 0 | 539 s (`ok internal/panel 537.908s`) |
| `graphify update .` | ok | graph updated (graphify-out/ is git-ignored, not committed) |

- No flake occurred. `TestWSLogsReplacedConnection` passed inside `make test`.
- I never ran two test runs at the same time.
- `gofmt -l internal/` is clean.

## Existing tests changed
None weakened or deleted:
- `TestForgetReplicaPortCLI` was only extended. Its single-port assertion gained `&& contains(caution)`, which is stricter.
- `TestQuarantineIdempotent` and all other existing tests pass unchanged.

## Texts for the owner's eye (new Indonesian user texts, verbatim)

- `internal/agent/cli.go:52-55` (usage; lines 54-55 are new, 53 gained the ending "; port"):
  ```
  hapus port replika dari catatan; semua juga
  menghapus tanda catatan port penuh; port
  replika yang tidak tersambung tidak dicatat
  lagi dengan sendirinya
  ```
- `internal/agent/cli.go:529-531` (single port, stdout): `port %d dihapus dari catatan port replika; bila replika di port itu sekarang tidak tersambung ke master ini, port itu tidak dicatat lagi dengan sendirinya dan replika itu tidak dibaca sebelum master dibuka pada promosi terencana berikutnya`
- `internal/agent/cli.go:546` (empty record, stderr): `catatan port replika sudah kosong (%s); tidak ada yang dihapus`
- `internal/agent/cli.go:549`: `catatan port replika: %s`
- `internal/agent/cli.go:551`: `port yang dihapus: tidak ada` (only the full mark was there)
- `internal/agent/cli.go:559` and `:561`: `port yang dihapus: %s%s`, where the suffix is `, dan %d port lain`
- `internal/agent/cli.go:564`: `tanda catatan port penuh ikut dihapus`
- `internal/agent/cli.go:566`: unchanged line, kept: `semua port replika dihapus dari catatan; port replika yang tersambung dicatat lagi oleh agent dalam beberapa detik`
- `internal/agent/cli.go:567-570`: `Perhatian: port replika yang sekarang tidak tersambung ke master ini tidak dicatat lagi dengan sendirinya. Replika di port seperti itu tidak dibaca sebelum master dibuka pada promosi terencana berikutnya. Bila ada replika yang memakai salah satu port di atas dan sekarang tidak tersambung, sambungkan lagi (atau pastikan replika itu sudah dipensiunkan) sebelum promosi berikutnya.`
- Docs (commit 000f5b5): the Indonesian rows and sentences quoted under "Docs changes" below.

Example `semua` output with 7 ports and the full mark:
```
catatan port replika: /var/lib/dbpanel/replica-ports-seen
port yang dihapus: 20001, 20002, 20003, 20004, 20005, 20006, 20007
tanda catatan port penuh ikut dihapus
semua port replika dihapus dari catatan; port replika yang tersambung dicatat lagi oleh agent dalam beberapa detik
Perhatian: port replika yang sekarang tidak tersambung ke master ini …
```

## Docs changes (commit 000f5b5)

### `docs/superpowers/security/2026-11-10-inventaris-risiko.md`
- The R-117 row is removed from the class C table. A new row is added at the end of "Sudah ditangani" with:
  - what was wrong;
  - what is true now (all sends last, deferred, on every path; `emitStart` only queues);
  - the pinning tests;
  - origin "A5a SEC-89 (pemeriksaan ulang gelombang perbaikan)";
  - "Ditangani di": "Tindak lanjut sebelum merge (bb54853)".
- The R-119 row stays in class C, priority Tinggi, and is rewritten:
  - the treatment column starts "**Sudah dikerjakan (bb54853), perilakunya tidak berubah:** …", describes the new output, names `TestForgetReplicaPortCLI` and keeps the README procedure;
  - "Yang belum: `semua` tetap melupakan port itu; … Rencana 5e";
  - status "terbuka (operator kini diperingatkan)".
- Ringkasan table: class C goes from 15/22/47/84 to 14/22/47/83, and the total from 27/37/54/118 to 26/37/54/117.
- Recount method: a script parsed every `| R-…` row under the three "## Kelas …" sections and counted the priority column (second-to-last cell).
  - Before the change it reproduced the old table exactly: A 18 (6/8/4), B 16 (6/7/3), C 84 (15/22/47), total 118 (27/37/54).
  - After the change: A 18, B 16, C 83 (14/22/47), total 117 (26/37/54).
- Other sentences:
  - The old recount sentence is relabelled "Angka setelah gelombang perbaikan …", which keeps the history.
  - A new block "Perubahan setelah tindak lanjut sebelum merge (bb54853)" has three bullets, including "118 − 1 = 117; kelas C 84 − 1 = 83 (Tinggi 15 − 1 = 14)".
  - R-117 is removed from the "Butir Tinggi terbuka lainnya" list.
  - The Rencana 5b bullet "R-116 dan R-117: …" becomes "R-116: hasil start di snapshot kesehatan. (R-117, urutan kirim laporan, sudah ditangani sebelum merge di bb54853.)".
- Left as history:
  - line 84, the list of rows added by A5a, which names R-117;
  - the "sisa R-116, R-117" in the A5a fix-wave row of "Sudah ditangani".
- R-119 in the 5e list stays (still open).

### `docs/superpowers/security/2026-11-12-audit-plan-5a.md` (every place matching `flushStartEvents`, `R-117`, `R-119`, `semua` that describes these items)
- Header (line 4): added "Untuk R-117 dan R-119 saja (sisa SEC-89 dan SEC-88), keadaan kode yang dijelaskan adalah `bb54853`, tindak lanjut sebelum merge yang ditemukan pemeriksaan ulang dan dikerjakan setelah audit."
- §1 open-items list (line 49): the Rencana 5b bullet no longer lists the send order. It now carries a note: "(Urutan kirim-laporan sebelum penegakan baca-saja, R-117, ditemukan pemeriksaan ulang dan sudah diperbaiki setelah audit di `bb54853`.)"
- §4.0 table:
  - SEC-89 row: "sisa R-116, R-117 (R-117 diperbaiki setelah audit di bb54853)".
  - SEC-88 row: unchanged, because R-118 and R-119 are both still open.
- SEC-88 residue on R-119: appended a note that the re-review found it and that after the audit `bb54853` made `semua` print the file, the removed ports and the caution (pinned by `TestForgetReplicaPortCLI`). The behaviour is unchanged, so R-119 stays open with the operator now warned.
- SEC-89:
  - "Perbaikan" gained "(Sejak `bb54853` pengirimannya pindah ke akhir putaran; lihat R-117 di bawah.)".
  - The R-117 residue gained "**Diperbaiki setelah audit di `bb54853`** (ditemukan pemeriksaan ulang): …" with the mechanism and the two pinning tests.
- SEC-116 re-review note: gained "; setelah audit, `bb54853` memindahkan semua pengiriman ke panel ke akhir putaran."
- §5 R-030 row: the "Sisa" sentence now says that the residue, R-117, was fixed after the audit in `bb54853`.
- §7:
  - Item 1's R-117 bullet gained "**Sudah dikerjakan sebelum merge di `bb54853`.**".
  - The Rencana 5b bullet "R-117: urutan `flushStartEvents`." is removed, because it is no longer planned work. Its history stays in item 1.
  - The 5e list's R-119 now reads "R-119 (keluaran `semua` sudah memperingatkan sejak `bb54853`; sisanya: hanya membuang port yang tidak dipakai)".
- Executive summary counts by status are unchanged: SEC-88 and SEC-89 were already "Diperbaiki", and R-117 and R-119 are residues with inventory rows, not SEC findings. The table and the "Status:" line are untouched.

### README
Unchanged. Its sentence about `forget-replica-port semua` (README.md:715-722) still matches: it says the record is emptied, connected replicas are recorded again, and the operator should check the file first. The new output adds detail but contradicts nothing.

No model names, agent IDs or `.superpowers/` paths are in the committed files.

## Decisions
1. **`EventQuarantined` is also moved to the end of the round.** The brief requires that "nothing that can block on the panel connection" runs before (a) read-only, (b) start-lock housekeeping and (c) master lock work.
   - Besides the start reports, `guardQuarantined` sends `monitor.quarantined` inline via `d.emit` → `Events.Emit`. That send is after read-only but before housekeeping, so it can block for the same 10 s.
   - Moving only the start reports would also have flipped the event order pinned by `TestQuarantineIdempotent` (start report, then quarantined).
   - So the notice is now held in `qNotice` and sent by `sendRoundEvents` after the start reports. This keeps the pinned order, and the new order test pins (b) for both kinds of send.
   - Delivery semantics are unchanged: it is sent once in the same round and not retried, as before.
2. `emitStart` only queues. All callers are inside `GuardTick` (`reportQuarantineStart` via `guardQuarantined`, and `watchBootFailure` via `guardStartLock`), so a report queued in a round goes out at the end of that same round.
3. The `semua` list is capped at `maxSeenPorts` (8), with the rest counted as "dan N port lain". The empty-record exit code stays 1 (output-only change). When only the full mark is present, the output prints "port yang dihapus: tidak ada" and still prints the caution.
4. The removed-port list comes from the same read as the write (`ForgetAllReplicaPorts`), not from a separate read in the CLI first.

## Concerns
- The `monitor.quarantined` move (Decision 1) goes slightly beyond the literal "start reports" scope of the brief. It is needed to meet requirement (b) and to keep the pinned event order. The reviewer should confirm.
- In a round that queued reports, the panel now learns of a late start or a quarantine at the end of the round, not mid-round. That is milliseconds to `--print-defaults` time later. It is never a later round.
- The three early-return subtests of `TestGuardFlushesStartReportsOnEveryPath` pass on the old code too. They are a guard against regression, not a RED, as the brief anticipated.

---

## Fix round 1

Commits: `ef453b6` (code), `faefde5` (docs). Both are pushed; the working tree is clean. The earlier `monitor.quarantined` move is kept, as ruled.

### I1: R-117 back to open (docs only; no code, per ruling)
- Checked in the code:
  - `internal/agent/monitor/monitor.go:315-327` (`guardLoop`): the loop runs the guards, then waits `GuardEvery` (`<-m.gafter(GuardEvery)`). It is "never a ticker".
  - `internal/agent/session.go:18`: `writeTimeout = 10 * time.Second`; `:40` applies it per send.
  - So a send blocked at the end of round N delays round N+1 by up to 10 s: about 15 s between enforcements, once per broken session.
- Inventory, `docs/superpowers/security/2026-11-10-inventaris-risiko.md`:
  - The R-117 row is removed from "Sudah ditangani" and put back in the class C table after R-116 (now line 241).
  - The rewritten row says what bb54853 and ef453b6 fixed: within one round, read-only, the start lock and the lock-guard work come before any send that can block, and `selfQuarantine` writes the start lock before its notice.
  - It says what remains, citing `monitor.go` `guardLoop` and `session.go` `writeTimeout`, with the scenario. Treatment: take guard sends off the guard's critical path, in Plan 5b with R-116.
  - Priority stays **Tinggi**. The file's definition judges impact only, and what is lengthened is the two-master window (same reasoning as R-097). The likelihood is small, and the row says so.
  - Status: "terbuka (urutan dalam putaran diperbaiki)".
- Other inventory changes:
  - The block "Perubahan setelah tindak lanjut sebelum merge (bb54853, ef453b6)" is rewritten. It says R-117 stays open, notes that docs commit 000f5b5 had briefly moved it and was reverted, and says the counts are unchanged.
  - R-117 is back in the "Butir Tinggi terbuka lainnya" list.
  - The Plan 5b bullet reads again "R-116 dan R-117: … pengiriman penjaga di luar jalur kritis penjaga (urutan di dalam putaran sudah diperbaiki di bb54853)".
- Recount, with the same script as before (it parses every `| R-…` row under the three "## Kelas" sections and counts the priority column):

  | Class | Rows | Tinggi / Sedang / Rendah |
  |---|---|---|
  | A | 18 | 6 / 8 / 4 |
  | B | 16 | 6 / 7 / 3 |
  | C | 84 | 15 / 22 / 47 |
  | **Total** | **118** | **27 / 37 / 54** |

  The Ringkasan table is restored to these numbers.
- Audit document, `docs/superpowers/security/2026-11-12-audit-plan-5a.md`. In the SEC table, R-117 is not an item of its own: it is a residual of SEC-89. So SEC-89 keeps its status "Diperbaiki", and the executive summary counts are unchanged. The residual is stated in these places:
  - header :4 (code state for R-117/R-119 is ef453b6, covering bb54853 and this round);
  - §1 :49 (R-117 remainder still in Plan 5b);
  - SEC-89 table row :131 ("urutan dalam putaran diperbaiki …; jeda antarputaran tetap terbuka");
  - SEC-89 residual :252-255, rewritten as "Diperbaiki sebagian setelah audit" with three sub-bullets: what was fixed, the three inline sends, and what remains;
  - SEC-116 :513;
  - §5 R-030 row :678 ("**Sisa:** R-117 …");
  - §7 item 1 :722 and the Plan 5b list :728 (R-117 re-added).

### M1a: selfQuarantine writes the start lock before its notice
- Change: `internal/agent/drivers/mysql/promote_lock.go:772-777`. The order is now `lock = d.lockStart(ctx, h)`, then `d.emit(EventQuarantined, …)`, then `return lock, nil`. It is the same event, the same single try, and the same error handling. The doc comment (:742-750) says so.
- Callers are unchanged otherwise. They only use the returned error or lock state:
  - `unlockPromote` (:481);
  - `guardExpired` (:648, :671), both of which return right after;
  - `mysql.quarantine` (`quarantine.go:149`, via `withStartLock`).
- Test: `TestSelfQuarantineWritesStartLockBeforeNotice` (`startlock_guard_test.go:672`). It uses `newQuarantineRig` and `mysql.quarantine`, which share `selfQuarantine` with the guard and with `unlockPromote`.
  - `stateAtSend` gained an optional `cfg` field and records whether the managed config holds `init_file=<lock>`.
  - At the `monitor.quarantined` send, it asserts that super_read_only is on, the lock file exists and the line exists.
- RED: `go test ./internal/agent/drivers/mysql/ -run TestSelfQuarantineWritesStartLockBeforeNotice -timeout 30m -v`
  ```
  startlock_guard_test.go:691: monitor.quarantined sent with super_read_only=true, start lock file present=false, init_file line present=false; want all three before the send
  --- FAIL: TestSelfQuarantineWritesStartLockBeforeNotice (5.23s)
  ```
  This is a behavioural failure.
- GREEN: `go test ./internal/agent/drivers/mysql/ -run 'TestSelfQuarantineWritesStartLockBeforeNotice|TestQuarantine|TestUnlockPromote|TestGuard|TestLockExpired|TestPromoteGuard' -timeout 30m -v` gives all PASS, `ok … 232.534s`. This includes `TestGuardQuarantinesWhenCandidateIsMaster`, `TestUnlockPromoteRefusedWhenCandidateWritable`, `TestUnlockPromoteReadsListedReplicas`, `TestGuardChecksListedReplicasBeforeUnlock` and `TestQuarantineIdempotent`.

### M1b: send order described as it is
- `promote_lock.go:539-546` (GuardTick comment) now states:
  - the start reports and guardQuarantined's quarantine notice are deferred to the end of the round (`sendRoundEvents`), on every path;
  - three sends stay inline, each the last action of its path and followed only by `sendRoundEvents`: keepLock's and the unlock's EventLockExpired (guardExpired), and selfQuarantine's EventQuarantined, which is sent after its start lock is written;
  - ordering within a round does not shorten the pause after it.
- Verified: in `guardExpired`, each `keepLock` call is followed by `return nil` (or only the `g` field writes before it). The unlock path's `emit` is followed by `return nil`. After the swap, `selfQuarantine`'s `emit` is followed only by `return`. In `GuardTick` only the deferred `sendRoundEvents` and `db.Close` then run.
- Docs: the same wording is in the inventory R-117 row and in the audit SEC-89 residual (:253-254) and SEC-116 (:513).

### M2: only the full mark
- `internal/agent/cli.go:550-554`: with no ports, it prints `tidak ada port di catatan; hanya tanda catatan port penuh yang dihapus` and returns 0. There is no port caution and no "di atas".
- Test: `TestForgetReplicaPortCLI` gains a mark-only file case. It checks exit 0, the plain line, the path, no "di atas" and no caution phrase.

### M3: every port named
- `promote_files.go`: `ForgottenPorts` loses `More`, and `forgetAllSeenPorts` returns every port, sorted. The file is bounded by `maxSeenSize` when read.
- `cli.go:555-559`: prints them all. The "dan N port lain" branch is gone.
- Test: the 10-port case (my own test from this task) is replaced by a 12-port hand-written file. All of 30001-30012 must be named, and "port lain" must be absent.
- RED for M2 and M3: `go test ./internal/agent/ -run TestForgetReplicaPortCLI`
  ```
  cli_test.go:938: forget-replica-port semua of 12 ports = 0, "…port yang dihapus: 30001, …, 30008, dan 4 port lain\n…"
  cli_test.go:942: forget-replica-port semua of 12 ports does not name 30009 (also 30010, 30011, 30012)
  cli_test.go:953: forget-replica-port semua with only the full mark = 0, "…port yang dihapus: tidak ada\ntanda catatan port penuh ikut dihapus\n…"
  FAIL	github.com/husmin-fajrin/db-panel/internal/agent	0.012s
  ```
- GREEN: `TestForgetReplicaPortCLI`, `TestForgetReplicaCLI` PASS (`ok … 0.016s`); `go test ./internal/agent/drivers/mysql/ -run TestSeen` ok.
- Existing tests: none weakened. The only assertions replaced are this task's own 10-port "bounded list" assertions, which the M3 ruling reverses. `seen_cap_test.go` is untouched.

### Gates (code of ef453b6, before commit, one at a time)
| Command | Exit | Duration |
|---|---|---|
| `make test` | 0 | 2265 s (37m45s); `drivers/mysql` 2221.279s, `internal/panel` 538.828s, `install_test.sh` ok |
| `make test-e2e` | 0 | 533 s (`ok internal/panel 532.237s`) |
| `graphify update .` | ok | run before the gates |

There were no flakes.

### Texts for the owner's eye (changes in this round)
- `internal/agent/cli.go:552` (new): `tidak ada port di catatan; hanya tanda catatan port penuh yang dihapus`
- `internal/agent/cli.go:559` (changed; the suffix `, dan %d port lain` is removed): `port yang dihapus: %s`
- All other texts are as listed in the first section, at shifted lines: empty record :546, path :549, full mark :561, confirmation :563, caution :564-567.
- Docs (faefde5):
  - the R-117 row (inventaris:241);
  - the rewritten "Perubahan setelah tindak lanjut" block;
  - the R-119 row wording ("setiap port yang dihapusnya (urut)", "(bila yang ada hanya tanda penuh, ia mengatakannya tanpa peringatan port)");
  - the audit SEC-89 residual sub-bullets (:252-255).
