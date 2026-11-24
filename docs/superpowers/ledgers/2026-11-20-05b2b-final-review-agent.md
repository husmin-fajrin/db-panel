# Final review — agent side — Plan 5b-2b (78b1bb9..d12a4df)

Scope: `internal/agent/…`, `internal/proto/…`, `Makefile` (`cmd/…` and `scripts/…` have no change in the range). Read: the whole agent diff (3,528 lines, production and tests) in one pass, in parts; the plan (Global Constraints, Review Focus, K1–K11, Tasks 1–7 and 14, the 27 changes); spec 5.7–5.13 and 7; rulings R1–R36; the deferred list. Outside the diff, after `graphify query`/`explain`: `monitor.go` (handler, `pause`, `emit`, `flush`, warn limiter), `session.go` (request loop), `promote_lock.go`, `cli.go` (`unlock-master`, `set-role`), `testmysql/instance.go`, the panel's `monitor.configure` senders, `JudgeBackup`, the master status rule, the README sections named in the dispatch, and the Go 1.26.10 `os` sources the scan relies on. No MySQL test was run. One experiment was run in the scratch directory on a copy of the scan code (A-1); nothing in the tree was changed.

## Strengths

- **The scan keeps every rule of the plan, and the code is stricter than the brief.** The folder is opened with `O_DIRECTORY` (a FIFO, device or file fails at once with ENOTDIR, also through a link), each subdirectory is opened one component at a time with `O_NOFOLLOW` from the folder's own descriptor (`backup.go:309-339`), `Open` on the walk's file system is refused, and entries are lstat'ed from the directory descriptor. I checked the last point against the toolchain: in Go 1.26.10 a directory opened in an `os.Root` runs `fstatat(AT_SYMLINK_NOFOLLOW)` on its own descriptor for every entry at `ReadDir` time (`os/file_unix.go:475-482`, `os/statat_unix.go:13`), so `DirEntry.Info()` in `visit` is a cached value and never a path lookup, even though the sub-`Root` is closed by then. `go.mod` pins `go 1.26.10`, so an older toolchain cannot build it.
- **Old results cannot be reported for a new folder.** The folder generation (`backupGen`) is compared in `backupState` (`backup.go:435`), the result is dropped when a scan of a new generation starts (`:384`), and `TestFolderChangeReportsPendingThenResult` fails if that comparison is removed.
- **`monitor.configure` cannot fail on the folder.** The folder is stored after the interval checks without any validation (`monitor.go:281`); a refused folder is reported `unreadable` with its reason and never scanned (`TestConfigureKeepsSettingsWithBadFolder`).
- **Only closed codes and numbers leave the agent.** `errnoText` strips the path from a `PathError` before it is logged; nothing of an entry's name reaches the log or the panel; the panel maps unknown reasons to one fixed text.
- **Task 1 fixes are small and each has a test that fails without it**: F2 instead of T7 for a read that could not be made (nothing recorded, second settle ends the quarantine), the stale leftover record, the second lift holder's trailer, `mysqldump` beside `mysqld`, F3/F4 with the system-log outcome.
- **Wire compatibility is clean**: every new field is `omitempty`, no decoder in either version uses `DisallowUnknownFields`, `HealthSnapshot` stays comparable (R1 test asserts at run time).

## Issues

### Critical (Must Fix)

None.

### Important (Should Fix)

**A-1. A folder with more than 10,000 entries is judged from an arbitrary older part of it, and nothing tells the operator.**
`internal/agent/monitor/backup.go:147` (`case w.found:` answers `ok` with `truncated`), `:302` (entries visited in ascending name order, depth first), `:234` (stop at entry 10,001); `internal/panel/monitor/alerts.go:1092-1118` (`JudgeBackup` never reads `Truncated`; no production code in `internal/panel` does); `README.md:1192` (only "paling banyak 10.000 entri").
What is wrong: the agent sets `Truncated`, the plan defines the field (Task 6), and no task consumes it. With date-named subfolders, which sort ascending, the folders cut off are the newest ones.
Scenario: a master's backup tool writes `/backup/2026-11-17/simrs/*.sql.gz` … `/backup/2026-11-23/simrs/*.sql.gz`, 2,000 files a day, 7 days kept (one file per table; mydumper and xtrabackup trees look the same). I ran the scan code on exactly this tree: it answers `status=ok truncated=true`, newest file 48 hours old, although today's 2,000 files are there. The panel then writes "Berkas terbaru di folder cadangan master … berumur lebih dari 48 jam (batas 48 jam)", opens "Cadangan master usang", and the Cadangan row is red: planned promote and Bangun ulang are refused, with no button past the gate. The operator looks in the folder, sees today's backup, and has no text that explains the difference. The only ways out are to empty the folder field or raise the age limit. The error is always toward "older" (never a false green).
Smallest fix I would accept: (1) the panel says so wherever the verdict is shown when the report is `ok` and `truncated` (card, alert, gate row), one sentence, for example "Pemindaian berhenti di 10.000 entri; berkas yang lebih baru mungkin tidak terlihat. Arahkan folder cadangan ke subfolder yang lebih kecil." (new text, owner's list), and one sentence in README "Cadangan (backup)"; a test in `TestJudgeBackupTable`/`TestBackupMessageTexts`. (2) Optional and agent-only: visit names in descending order (`backup.go:302`), so date-named folders are read newest first; `TestBackupScanLimits` "regular file first" then needs its file name changed. (1) alone is enough.

### Minor (Nice to Have)

**A-2. Two `monitor.configure` requests are applied by the agent in no defined order.**
`internal/agent/session.go:96` (each request runs in its own goroutine), `internal/agent/monitor/monitor.go:281`; panel `internal/panel/monitor/monitor.go:368-373, 403-440` (each send reads the server row, then calls, with nothing between the sends of one server). This is the agent half of the deferred named item of Task 14. Scenario: a backup-form save races with another configure of the same server (a second save, a Pengaturan save, a reconnect); the agent ends on folder A while the store says B; every later report describes A and is judged as B, until the next configure. A false green on the Cadangan row is possible. For a person typing two different folders the window is microseconds on the panel and one goroutine start on the agent, so it is rare; it is silent when it happens. Smallest fix: on the panel, one lock per server held across "read the row, call, take the answer" in `configure`. Ordering only the sends is not enough, because the agent does not keep request order. No agent change is needed then. (The complete alternative, a number the panel sends with the folder and the agent echoes in `BackupState`, also closes the R22 refill window, but is a protocol change; not needed for merge.)

**A-3. A new folder reads "pending" for as long as a scan of the old folder hangs.**
`backup.go:375-378` (no scan starts while one runs, whatever the folder) with `:435` (another generation → `pending`). Scenario: the backup folder is on an NFS mount that hangs; the card says "pemindaian tidak selesai dalam 10 detik". The operator changes the folder to a local disk. The agent now answers `pending` on every report until the stuck system call returns or the agent restarts; the card says "Belum ada laporan dari agent." and the row is yellow, with the cause only in the agent log ("backup folder scan still running"). The rule "one scan at a time" is right; the reported state is the gap. Smallest fix: in `backupState`, when the stored generation differs, a scan is running and it started more than `backupScanTimeout` ago, answer `unreadable`/`timeout`; one case added to `TestBackupScanOneAtATime` (which today asserts `pending` there).

**A-4. A subfolder that cannot be opened is skipped without a trace.**
`backup.go:219-227`. Scenario: backups on a NAS over NFS with root squash; the top folder is 0755, the dated subfolders 0700 of the backup user. The agent (root, mapped to nobody) lists the top, fails on each subfolder, skips them, and answers `none` or the age of an old top-level file: the card says "Folder cadangan … tidak berisi berkas" although the cause is permissions. Smallest fix: count skipped subfolders and keep the first errno; when no regular file was found and at least one subfolder was skipped, answer `unreadable` with `backupReason` of that errno; in every case one rate-limited log line with the count (no names).

**A-5. A file with a modification time in the future keeps the row green.**
`backup.go:87-92` (age never below 0; plan Task 6 asks for this). Scenario: one backup was written while the server clock was wrong (months ahead); that file stays "newest" with age 0 until that date, so backups can stop without the row or the alert noticing. `touch -d 2099-01-01` by anyone who can write there does the same, but such a person can also touch a file every hour. Smallest fix, for the owner to decide (plan value): treat a newest file more than, say, 24 hours ahead of the agent clock as a problem state, or at least say it in README "Cadangan (backup)". Can wait.

**A-6. The README's residual risk names less than the report discloses.**
`README.md:4194-4202` says a compromised panel learns "umur dan ukuran berkas terbaru". It also learns, for any path outside the refused trees: whether it exists (`missing`), whether it is a directory (`not_dir`), whether it holds a regular file within three levels (`none`), and whether it holds more than 10,000 entries (`limit`, `truncated`). Two more facts for the audit: the 5-minute limit binds only an unchanged folder (each folder change is scanned at the next check, `backup.go:380-386`, so a hostile panel gets about one scan per 10 s, each bounded at roughly 30,000 lstat calls); and `/var` and `/run` pass the folder rule although `/var/tmp` and `/run/user` lie below them (`backup.go:36`). The second is harmless under the shipped unit (`scripts/install.sh:316-317`, PrivateTmp and ProtectHome make both empty for the agent). Smallest fix: one sentence in the README; no code.

**A-7. `testmysql.freePort` picks the port before `mysqld --initialize-insecure`.** See named item 5.

**A-8. The result of a scan reaches the panel one check interval after the scan.**
`monitor.go:339-342`: the loop runs `tick` before `scanBackupFolder`, so a result is reported by the next tick. After a folder change the card reads "Belum ada laporan dari agent." for up to two check intervals plus the scan (30 s by default, 10 minutes at the 300 s maximum); README says the folder "dipindai pada pemeriksaan agent berikutnya", which is true of the scan, not of the card. Smallest fix: call `scanBackupFolder` before `tick`; the fake-clock tests that count rounds need the matching change. Can wait.

## Named items (the controller's five questions)

**1. Ruling R14, symlinked backup folder.** Not a defect; the ruling stands.
- *Steered outside the allowed places:* no. The folder path is resolved by the kernel, the resolved path is read from `/proc/self/fd/N` and must pass `ValidBackupFolder` (`backup.go:185-194`); `os.Root` is opened on that descriptor and checked with `SameFile`. Links to `/proc/1`, `/tmp`, a FIFO, a file, a dangling target are tested. A link to `/`, or through `/proc/<pid>/cwd` or `/proc/self/root`, resolves to a real path that is checked the same way. The open happens before the check, but an `O_DIRECTORY` open has no side effect on a device or FIFO (ENOTDIR before the driver is reached).
- *R14 gives a hostile panel nothing new:* the panel may name any directory outside the refused trees directly, so a symlink adds no place it could not already ask for. A local user who can place the link only changes what the agent tells the panel, which that user can also do by touching a file.
- *Made to follow a link inside the folder:* no (`O_NOFOLLOW` per component, entries lstat'ed from the directory descriptor, swap tests at every component).
- *Made to hang or run long:* bounded as designed. One scan goroutine at most; the check loop waits 10 s once and then never again while that goroutine is stuck (`backup.go:375`); the guard loop is separate and unaffected. Entries read are bounded at about 10,001 per level, three levels. A hostile panel can leave one goroutine and one thread stuck on a hung mount until it returns, and can force a scan per check round by changing the folder (A-6).
- *Made to report on a file the rule should hide:* not under the shipped unit. Without PrivateTmp/ProtectHome the folders `/var` and `/run` would include `/var/tmp` and `/run/user` (A-6).
- *A local user who can write inside the folder* can: fake a fresh backup (inherent to an age check; also A-5); hide the true newest file behind 10,000 entries with early names (false red, A-1); make a subfolder unreadable to root with a FUSE mount (A-4), or the whole folder "izin ditolak" when the mount is at the top level (this FUSE case is from reading the code and Go's `readdir`, not from a run). Every one of these but the first is a false red, which stops planned work and not an emergency promote.
- *What the report discloses:* status and reason codes, age and size of the newest regular file, the truncated flag, seconds since the scan. So not only age and size: see A-6. No name, no content, no path.

**2. `unlockMaster` leaves the saved `read_only`.** Not a defect of this plan's scope: `promote_lock.go:503-504` and `cli.go:828` are unchanged in the range, the plan names neither, and R32/R33 chose to document it; the README (lines 813-817, 2691-2695, 3310-3313, 3387-3394) and guides 6 and 7 now say it correctly. As a product risk it is worth a named item for the next plan, graded Important there. Worst outcome: an old master is made master again by hand (`set-role master`, then `unlock-master`), and the operator follows only what the two commands print; `set-role` prints "buka dengan: sudo dbpanel agent unlock-master" (`cli.go:352`) and `unlock-master` prints "master dibuka: MySQL bisa ditulis", neither mentions the saved `read_only`. Weeks later MySQL restarts, `read_only` is ON again with `super_read_only` OFF: every application account without SUPER gets ERROR 1290, and the panel shows the master as "Sehat … bisa ditulisi" with no alert, because the master status reads only `super_read_only` (`internal/panel/monitor/status.go:265-268`). No data is lost and no second writer appears; the cure is one statement. Smallest fix when it is taken up: `unlock-master` and `unlockMaster` also send `RESET PERSIST IF EXISTS read_only` (they run only on a server pinned master, where a saved `read_only=ON` is never wanted), with one real-mysqld test; or, without touching lock code, `setRoleNextMaster` names the statement. Not for this merge unless the owner wants it.

**3. `monitor.configure` with a bad, empty or changed folder.** Not a defect, with two minors.
- Settings kept: yes, the folder is never validated in `Configure` (tested).
- Old state as the new folder's: no (generation check, tested); an old panel's params without the field turn the scan off (tested).
- Configure during a scan: the running scan of the old folder ends within its 10 s bound, its result is stored under the old generation and never shown, and the new folder is scanned at the next round. The exceptions are A-3 (the old scan hangs: `pending` without end) and A-8 (one interval of delay).
- The backlog the handler flushes before its answer (`monitor.go:235`) can carry the old folder's state; it is sent before the answer and is transient (the deferred R22 refill window).
- Order of two configures: A-2.

**4. Compatibility.** Nothing breaks in either direction.
- This agent with the previous panel: the three new snapshot fields are ignored (lenient decode at 78b1bb9, no `DisallowUnknownFields`); the old panel sends no `backup_folder`, so no scan runs and `Backup` stays nil; a panel rolled back turns a running scan off at its next configure.
- Previous agent with this panel: `backup_folder` is ignored (same lenient handler); `ClockUnixMS` 0, `PerfSchema` nil and `Backup` nil are read by the panel as "not known" (yellow), and the Versi agent row is red, as spec 5.12 asks. This plan adds no agent command, so monitoring and every ungated action keep working with an old agent.
- One state that reads slightly wrong (panel text, for the web reviewer): with a folder set on a server whose agent is the previous version, the card says "Belum ada laporan dari agent." for good, although the agent reports and is only too old to scan.

**5. `testmysql.freePort`.** A real flaw of the harness, Minor, not required before merge. `instance.go:120` takes the port before `--initialize-insecure` (seconds), so any `bind(0)` or outgoing connection of another test package can take it before mysqld binds (`instance.go:279-286`). Cheapest improvement: move the `freePort()` call to just before `my.cnf` is written (after the initialize), which shrinks the window from seconds to mysqld's start; three lines, no behaviour change. The full fix (start again on another port when the error log says the address is in use) belongs with the deferred work on the 60-minute package. Take the cheap one in the fix wave only if that wave touches `testmysql` anyway; a spurious failure costs one more run of an hour-long gate.

## Triage of deferred findings (one line each: fix before merge / can wait / not a defect — reason)

Agent side:
- Task 1, commit 548c1fd does not build its tests until 9127ee9: not a defect — history only, no rewrite.
- Task 1, F2 and `errLiftLockUnopenable` carry a raw cause with quotes the panel's censor may cut: can wait — K9 item for the audit; the usual cause ("Access denied for user '…'@'…'") has balanced quotes and the advice after it survives.
- Task 1, only the `openAdmin` branch of F2 is tested: can wait — the three branches return the same type one line apart.
- Task 1, every teardown opens a root connection first: not a defect — bounded at 2 s, test harness only.
- Task 5, `TestChangedOnRolePinAndPerfSchema` is in monitor_test.go: not a defect.
- Task 5, the agent mysql test package takes about 60 minutes: can wait — a later plan; R3 covers the timeout.
- Task 6, folder logged clipped in one line and unclipped in another: not a defect — the unclipped line is reached only by a folder that passed the 1024-byte rule.
- Task 6, folder deleted between open and readlink reads `none`: can wait — corrects itself at the next scan (`missing`).
- Task 6, `hidden`/`invalid` through `monitor.configure` tested for `/home/x` only: can wait — the rule is one function with a full table test.
- Task 6, gate 1 ran while a comment was edited: not a defect.
- Task 6, the R14 test's directory under `/var/lib` survives a killed run: can wait.
- Task 6, `backupFS` doc comment overstates "the real depth": can wait — comment only.
- Task 6, `to-proc1` cases assume `/proc/1` opens: can wait — the tests run as root.
- Task 7, `JudgeBackup` adds the agent's age unchecked: can wait — an honest agent never sends a negative value (`secondsSince`); a hostile one can report "fresh" anyway.
- Task 8, R22 refill window: can wait — transient; the agent applies the folder before it answers, so the answer always ends it. Persistent only through A-2.
- Task 9, `testmysql.freePort`: can wait — see named item 5 (A-7).
- Task 13 note, `unlock-master` and the saved `read_only`: not a defect of this plan — see named item 2; carry it as a named item.
- Task 14, README:803 comment "kedua flag OFF sampai restart": can wait — the paragraph right below it is exact.
- Task 14, e2e test 3 leaves `/var/lib/dbpanel-e2e-backup-*` on a timeout panic, and the 5-minute rescan is shown only by unit tests: can wait.
- Task 14 NAMED ITEM, two configures out of order: fix before merge — on the panel, one lock per server across read-and-call (A-2); small, and it closes a silent false green.

Not agent side (left to the panel reviewers): every other line of the list (Tasks 2, 3, 4, the rest of 7 and 8, 9 to 13, and the remaining lines of 14).

Items "carried to Task N": none of them is on the agent side.

## Findings against rulings

None. R13 and R14 are right as built (named item 1). One note on R14's wording: its cost says "two numbers"; the report also carries the status and reason codes (A-6). That changes nothing in the ruling.

## Declined to judge

- K1 (an emergency promote to a candidate whose agent version differs is refused, against spec 5.12): owner's decision, panel side.
- The panel's master status showing "Sehat" with `read_only` ON and `super_read_only` OFF: panel, before this plan; named under item 2 only for its effect.
- Changing `unlockMaster` itself: outside the plan's tasks (item 2).
- K9 items (`cancelAgentCopy` outside `startMu`, quoted names in censored lines, `MYSQL_*` environment of client programs, `tail` on a FIFO named `log_error`, `putBack`/`finalCheck` without `defer`, `grant-admin`): handed to the audit by the plan.
- Two stacked lifts where the first holder's full load ends first (its trailer turns `super_read_only` on under the second load): before this plan, and no panel flow starts two loads on one server; A-M8 fixed the order the previous review named.
- `drop-database` (root CLI) now runs the `mysqldump` beside the detected `mysqld`: the same trust the agent's own dumps, `mysqlbinlog` and `mysqld --validate-config` already place in that directory; for the audit.
- An operator who names the live data directory as the backup folder gets a permanently fresh reading: operator error, not in the spec.
- Automounts triggered, or access times changed, when the agent opens a panel-chosen directory: no write, outside the plan.
- The clock row under a send that stalls for tens of seconds: panel side.
- `backup.go` on a system other than Linux (it uses `openat` and `/proc`): the product is Linux only.
- The wording of F2 "tidak ada yang diubah" after replication was already configured in the same step: text fixed by the plan, for the owner's list.
- Tests that start MySQL and the e2e tests: not run by me (review rule); I relied on the gate logs the controller read.

## Assessment (Ready to merge? Yes / With fixes / No — reasoning in 2–3 sentences)

**With fixes.** The agent side does what the plan and spec ask, its safety rules hold under a hostile panel and a local writer, and I found no way to make the scan leave the allowed places, follow a link, or hang the agent. One gap spans the two sides and should be closed before merge: a truncated scan is shown as a plain stale backup (A-1), which gives a false red that an operator cannot explain; with it, take the one-lock fix for the out-of-order configure on the panel (A-2). Everything else can wait or goes to the audit and the owner.

Counts: Critical 0, Important 1, Minor 7.
