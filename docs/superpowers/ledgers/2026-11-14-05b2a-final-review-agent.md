# Final review, agent side (head 7ee4051) — findings, verbatim

Verdict: ready to merge with fixes. Critical: none. `cmd/`, `scripts/`, `Makefile` have no changes in the range. Commit 79ad8d9 (test-only) is sound.

## Important

A-I1. **The guard stops relocking, silently, when the lift lock file cannot be opened.**
- Where: `lift.go:355-358` (`tryGuard` returns "not free" on any `openGuardLock` error); `quarantine.go:353-361`, `:429-433`, `:627-631`.
- What is wrong: "cannot open the lock" is treated as "a legitimate lift is open". The guard leaves `super_read_only` alone and calls `checkedRound()`, which resets the unverified streak. `unverifiedRound` itself returns when not free, so nothing is ever reported or logged. The settle answers T12 ("a dbpanel agent command holds the lock"), which is false.
- Path to harm: `/var/lib/dbpanel/agent` or `lift.lock` cannot be opened or created (no inodes or read-only filesystem at first creation, descriptor exhaustion in the agent, a directory chown'ed by mistake). Then MySQL comes up writable (start lock bypassed) or a privileged MySQL account turns the flag off. Before this plan the guard relocked on every transition unconditionally; now the server stays writable with no alert.
- It needs a filesystem fault, hence Important, but it is a fail-open in the last line of defence, and an old master that just failed is where such faults occur.
- Fix: let `tryGuard` distinguish "could not open" from "held". On "could not open" with `holders == 0`, the guard relocks anyway and logs (no agent lift can be open, since `begin` needs the same file). The settle returns an error naming the file, not T12.

A-I2. **P1 prints a command that names a different database.**
- Where: `cli_dropdb.go:302` (`shellArg(pname)`, with `pname = printable(name)`).
- What is wrong: for a name with a newline, control or format character, `printable` changes the name, so the suggested `sudo dbpanel agent drop-database '<printable name>' --tanpa-cadangan` targets another name. `cli_dropdb_test.go:930-936` asserts exactly this: database `"a\nb\x1bc"` yields `drop-database 'a b c' --tanpa-cadangan`.
- Path to harm: such names are the ones `mysqldump` cannot dump, so P1 is their normal outcome. If no database `a b c` exists, the operator gets "tidak ada di server ini" and has no instruction that works. If one exists (a normal MySQL user could have created the twin before the quarantine), the pasted command backs it up, shows the same-looking name, asks for it to be typed, and drops the wrong database. A backup exists unless that dump fails too.
- Fix: when `printable(name) != name`, do not print a command line; say to run the same command again with `--tanpa-cadangan` added. Add the control-character twin to the test.

A-I3. **`mysql.leftover` answers OK while recording nothing, and Amankan then loops on "Periksa dulu".**
- Where: `leftover.go:74-84` (quarantine id unreadable or not writable); the same class in `salvage.go` `recordLeftover` (a failed write is only logged); the refusal at `salvage.go:236-239`.
- What is wrong: this plan removed the in-memory copy of the set (plan note 14), so a failed write, which used to leave Amankan working until the next agent restart, is now a dead end. The cause is only in the agent's journal.
- Path to harm: the old master's `/var/lib` is full or faulty, a likely reason it stopped being master. Periksa shows transactions, Amankan says "Jalankan Periksa data tertinggal dulu", and this repeats, during the one window in which leftover data can still be saved.
- Fix: when the result has a non-empty set and the record (id or file) could not be kept, fail the command with a text naming the file and cause. An empty set needs no record and can stay OK. This adds one text to the owner's list.

A-I4. **The three root commands act on whatever MySQL discovery finds when `agent.yaml` cannot be read.**
- Where: `cli_rootsql.go:198-201` (root session, all three commands); `cli_admin.go:274-277`, `:455-458`.
- What is wrong: a failed `config.Load` becomes `config.Config{}`, dropping a `mysql_socket` override. The pattern is copied from `cliReleaseStartLock`, which only touches files; here it leads to `DROP DATABASE`, `GRANT` and `ALTER USER`.
- Path to harm: a host with two MySQL instances, the agent's reached through the override, and a corrupt or half-written `agent.yaml`. The role pin still says quarantined; if the default instance is read-only (a replica), the 1 1 check passes. `drop-database` then drops a same-named database there, and `reset-admin` creates `dbpanel_admin@localhost` with every privilege there.
- Fix: refuse on a `config.Load` error at the three sites (`textNoLayout` fits).

## Minor

A-M1. `mysql.setReplicationFilter` skips the T18 check (`replication.go:100-120`; `SetReplicationFilter` sends `STOP REPLICA … START REPLICA` on every channel). `refuseForeignChannel` was added only to `startReplica` and `startUntil`, as spec 5.7 words it. With a valid approval the panel can still start a stale foreign channel on a quarantined server. Fix: one call in the handler.
A-M2. `adminReplaced()` is set after the mark is cleared on the success path (`snapshot.go:91-93`; the deferred `hold.end()` in `dumpFrom` runs first). Refused guard logins counted at the end of the load can reach three and be reported "unverified" in that gap, which sets `writableSince` and makes the settle answer T7 for a good copy. Very narrow. Fix: also call `mark.adminReplaced()` in `dumpFrom`'s `if replaced` branch (`dump.go:206`), as the failure path already does.
A-M3. The settle answers T7 for a transient read failure (`quarantine.go:632-637`). T7 tells the operator to repeat the whole copy, though nothing is recorded and "Ulangi dari tahap ini" would work. It also answers T12 while an agent lift in the same process is open.
A-M4. A failed first `OFF` is not followed by a restore attempt (`lift.go:293-300`). The old `liftReadOnly` called `restore()` here. On a plain replica (no guard) a cancel landing on that statement can leave `super_read_only` off; `read_only` stays on.
A-M5. A root-owned directory counts as safe whatever its group/other write bits (`svcuid.go:74-83`). In a `root:mysql 0770` binlog directory the service account can plant a symlink and the tool runs as root. Fix: treat uid 0 with `perm & 022` as not root's, or refuse.
A-M6. `drop-database` finds `mysqldump` through `PATH` (`cli_dropdb.go:144`); the agent uses the one beside `mysqld` (`findClientTool`). On a MySQL not installed by dbpanel, in its own basedir, the backup ends at P1b ("tidak bisa dijalankan") with no hint.
A-M7. `reset-admin` refuses when the stored credential cannot be read (`cli_admin.go:451-454`), which is the state it exists to repair. The operator has no next step but removing the file by hand.
A-M8. A full dump that is not the first opener sets `super_read_only = ON` in its stream under another open hold (`dump.go` `loadTrailer(…, hold.superWasOn())`). The other job's statements then fail with 1290. The panel's jobs are sequential, so this is theoretical.

## Deferred-list triage (agent side)

| Line | Verified at head | Bin | Reason / smallest fix |
|---|---|---|---|
| Task 4: `tryGuard` "not free" when the lock file cannot be opened | True | Fix before merge | A-I1. |
| Task 8: P1's command built from `printable(name)` | True, and asserted by the test | Fix before merge | A-I2. |
| Task 9: failed `config.Load` falls back to the default config | True, and `drop-database` has it too through `openRootSession` | Fix before merge | A-I4. |
| Task 10: `mysql.leftover` OK but nothing recorded | True, and a failed `recordLeftover` write is the same case | Fix before merge | A-I3. |
| Task 8: `putBack`/`finalCheck` not deferred | True (`cli_rootsql.go:431-447`) | Can wait | A panic in `do` ends as SIGKILL does: lock freed, guard relocks within a round, `read_only` on throughout. |
| Task 8: Ctrl-C read as a failed dump | No longer true as written: a `mysqldump` ended by a signal is `causeKilled` and refused with P1b (`cli_dropdb.go:694-696`, commit f7bfb09) | Can wait | What remains is wording and a signal swallowed during hashing, where nothing is lifted. |
| Task 9: `grant-admin` prints done without recomputing | True (`cli_admin.go:361-362`) | Can wait | Every `GRANT` returned success, and each consumer recomputes. |
| Task 9: `textResetFailed` after a good login but a failed store write | True (`cli_admin.go:553-556`) | Owner decides | Keep: the cause is in the parentheses and the next line says the stored account is unchanged and to run again. Change: a line of its own saying the account was recreated and only the save failed. Reviewer would add the line; need not block the merge. |
| Task 3: a first `begin` whose `OFF` errors after applying | True | Can wait | A-M4. |
| Task 7: `tail` on a FIFO holds `promoteMu` 5 s per round | True | Can wait | Only while MySQL is down; needs the service account. |
| Task 4: settle answers T12/T7 in the two side cases | True | Can wait | A-M3. |
| Task 10: race between `set-role quarantined` and Periksa keeps an id | True | Can wait | Needs root's command and Periksa within microseconds. |
| Task 8: `LIBMYSQL_*` reaches the backup's `mysqldump` | True | Can wait | Root's own environment, reset by `sudo`. |

Every other line of Tasks 3 to 10: can wait, harmless (test-strength notes, wording, dead fields, or bounds already limited by the guard's round).

## Declined to judge

- `mysql.quarantine` on an already quarantined server and `mysql.setReadOnly{on:true}` persist the flags without the lift lock and can fail an open load with 1290. Older than this plan, and tightening only.
- The older paths that send `read_only = OFF` (promote, unlock-master, `SetReadOnly` off): unchanged here and refused on a quarantined server by the role table.
- `--tanpa-cadangan` offered after any connected dump that exits 2 or 3, including one broken view in an otherwise dumpable database: the owner approved the option and note 10 narrowed it; a product decision.
- The guard stays quiet while refused logins are expected after a load replaced the accounts (R-5), even if a privileged account from the new master turns the flags off meanwhile: recorded design.
- A SIGKILLed `drop-database` whose `DROP` still completes in MySQL without a syslog line: spec section 8's named remaining risk.
- The gid of the directory given to the service-account child (Debian `mysql:adm`): spec-mandated, belongs in the spec's remaining risks.
- The agent's own `mysqldump`/`mysql` runs do not set `MYSQL_TEST_LOGIN_FILE`: the unit's `ProtectHome=yes` hides `/root`.
- A hostile new master lying in the database listing: the new master is the data being copied and is trusted for it.
- MySQL 8.4 behaviour of the new statements: not measured (spec section 8).
- How the panel words T7, T12 and the new refusals: the other reviewer's side.
