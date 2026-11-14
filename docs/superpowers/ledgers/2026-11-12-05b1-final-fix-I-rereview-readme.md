# Re-review of final fix wave part I: README (78a8c9f..7e65a81)

Scope: the README passages changed in cf83f05 and 7e65a81, plus the plan's new subsection "Bukti langkah tangan, tinjauan ulang H (bagian I)" (9d8a422). README line numbers are from 7e65a81. Code was read at head (graphify first, then the located lines). Nothing was run. `git diff --stat 78a8c9f..7e65a81` touches only README.md and the plan file, as the writer reports.

## Finding Verdicts

| Item | Verdict | README (7e65a81) | Evidence |
|---|---|---|---|
| **I-H1** "tidak mengubah apa pun" | ADDRESSED | 2013-2022, plus 1976-1978 (Batalkan qualifier) and 2319-2322 ("Menjadi master lagi") | create_replica.go:753-760: `mysql.dropDatabases(WipeList)` runs before `copySnapshot`. `markCopySent` is in snapshot.go:54/60/106, so the drop precedes `CopySent`. The WipeList is the target's databases the master also has, approved in a job wait (create_replica.go:494-522). The drop runs with `sql_log_bin = 0` (databases.go), so `mysql.leftover`/`mysql.salvage` still read the binlog. Every clause of the new bullet is true. See m-I1 for the "Menjadi master lagi" note. |
| **m-H1** after-rebuild comparison | ADDRESSED | 1835-1841 | **Segarkan** = server_detail.html:263-264 (refresh form inside the connected MySQL box). The stale-list note is true: no refresh after a plain dump copy (create_replica.go:594, :636, :883). "Jangan promote … minta bantuan admin MySQL" gives a defined end. |
| **m-H2** `0 0` / no answer | ADDRESSED | 2072-2079, 2219-2226 | Service names match README 2229/2280 (`dbpanel-agent`, `mysql`/`mysqld`). The guard persists read-only on its first round after the agent starts, and when MySQL answers after not answering (quarantine.go:252-311; first round immediate, then every 5 s, monitor.go:118, 334-343). "Tunggu 10 detik" covers that. There is a final stop ("minta bantuan"), not a loop. The systemctl steps were not run (stated). |
| **m-H3** existence check and moving aside | ADDRESSED | 2104-2120 | Evidence rows: `mv -n` to a free name gives exit 0; to an existing name, "not replacing", exit 1, both files intact. The `ls -l` check rather than the exit code is right for coreutils < 9.2. The quoting is correct: single quotes around the name keep `$`, and only `$(date …)` is in double quotes (row with `db$x`). |
| **m-H4** copy elsewhere, with a check | ADDRESSED (Minor m-I3, m-I4) | 2137-2142 | `sha256sum` row (copy equal, truncated different). `scp` not run (stated). The example syntax is correct: the file lands under its basename in the remote home directory, which matches the remote `sha256sum '<nama>-cadangan.sql'`. |
| **m-H5** when the copy counts as started | ADDRESSED (Minor m-I2) | 1991-1994 | dump.go:142-146 resets the binlog only when `gtidMode == "ON"`. `dumpGTIDMode` (dump.go:238-243) returns COMMENTED for a partial dump into a server with replication configured and a non-empty gtid_executed. clone.go:122-123 emits "CLONE dimulai" before CLONE INSTANCE. The parenthesis is right; the main clause still overclaims (m-I2). |
| **m-H6** ERROR 1396 in the interactive variant | ADDRESSED (Minor m-I4 on ordering) | 1918-1922 | Evidence rows: interactive mysql continues after 1396, and GRANT hits the existing account; the instructed branch (`SET GLOBAL super_read_only=ON;`, `exit`) ends `1 1` with grants unchanged. The README uses the interactive form (1903-1906), which is the measured form. |
| **m-H7** `event_scheduler` persisted value | ADDRESSED | 2407-2410 | Evidence rows: persisted ON beats the option line (`VARIABLE_SOURCE` PERSISTED); after RESET PERSIST the line applies (EXPLICIT). |
| **m-H8** Batalkan refusal sentence | ADDRESSED | 1981-1983 | `latestRebuildEnded` (controls.go:185-201) and `errNotFailedRebuild` (controls.go:148). |

## Quoted texts (changed passages)

| README line | Text | Source | Match |
|---|---|---|---|
| 1836 | **Segarkan**, box **MySQL** | server_detail.html:264, :67 | yes |
| 1919 | "ERROR 1396" | plan row (`ERROR 1396 (HY000): Operation CREATE USER failed for …`) | yes (code) |
| 1981-1982 | "server ini bukan master lama dengan percobaan bangun ulang yang gagal" | controls.go:148 | yes |
| 1974-1975 | quoted Batalkan sentence (unchanged) | handlers_oldmaster.go `msgCancelAttempt` | yes |
| 2117 | "not replacing" | plan row (`mv: not replacing '…'`, coreutils 9.4) | yes (fragment) |
| 2116 | "No such file or directory" | `ls` | yes |

No new panel or agent text is quoted.

## Commands (new or changed)

| README line | Command | Correct | Safe for the allowed names (`$`, space; quotes and backticks excluded at 2043-2045) | Does what the sentence says | Evidence |
|---|---|---|---|---|---|
| 2112 | `mv -n '/root/<nama>-cadangan.sql' '/root/<nama>-cadangan-'"$(date +%Y%m%d%H%M%S)"'.sql'` | yes | yes (row with `db$x`) | yes; never overwrites | plan rows 1-2 |
| 2113 | `ls -l '/root/<nama>-cadangan.sql'` | yes | yes | yes | same |
| 2139 | `scp '/root/<nama>-cadangan.sql' <pengguna>@<server lain>:` | yes | yes | yes | **not run** (no second host) |
| 2140-2141 | `sha256sum '<nama>-cadangan.sql'` (remote); the local command is implied, not written | yes | yes | yes | plan row 3 |
| 2074-2077, 2221-2224 | `systemctl restart dbpanel-agent`; `systemctl start dbpanel-agent`; `systemctl start mysql`/`mysqld` | yes | n/a | yes per the code | **not run** (no systemd unit here); unit names as scripts/install.sh and README elsewhere |
| 1918-1922 | interactive `SET GLOBAL super_read_only=ON;`, `exit` after 1396 | yes | n/a | yes, `1 1` | plan row 5 |
| 2407-2410 | option line `event_scheduler=OFF` vs persisted | yes | n/a | yes | plan rows 7-8 |

## Literal walks of the changed steps

- **Step 1, `0 0` branch (both sections).**
  - Rebuild running: the operator waits. If the rebuild fails, the load's `restore()` brings back `1 1`. If it succeeds, the pin becomes `replica`, and a re-check stops at the role line.
  - No rebuild running: restart the agent and wait 10 s; the guard persists ON, giving `1 1`.
  - Stored account broken: still `0 0` → "berhenti dan minta bantuan". That is a defined end.
- **MySQL not answering.**
  - Start the agent, then MySQL. The start lock and the guard put read-only on, giving `1 1`.
  - MySQL won't start: the final stop.
  - "Not answering" was really a forgotten `-p`: the starts do nothing harmful, and `-u root -p` is named at the section start.
- **Step 4, file exists.**
  - The `LIKE` query shows an exact match: `mv -n` to a timestamped name, `ls` shows no file, then the dump. Both backups are kept.
  - The same second collides: "not replacing" → wait and repeat.
  - No exact match: skip steps 4 and 5.
  - No path overwrites a backup.
- **Copy off.**
  - `scp` succeeds and the hashes are equal → step 5.
  - The hashes differ → copy again.
  - No second server: "media lain" has no command, but the `/root` copy remains, so nothing is lost.
- **New-account variant, account exists.** 1396 → `SET GLOBAL super_read_only=ON;` → `exit` → `1 1` → the operator must retype the block with another name in both the CREATE USER and GRANT lines (m-I4).
- **Failed-rebuild block.** The NOT STARTED bullet now matches the code (drop before the copy command, binlog intact). The Batalkan qualifier at 1976-1978 makes the panel's "data di server ini tidak diubah" read correctly.

## New false, unverifiable or dangerous statements

### Critical
None.

### Important
None.

### Minor

**m-I1. The "Menjadi master lagi" warning covers only the dump's drop, not a copy that already began.** README 2319-2322: "Bila bangun ulang dengan dump pernah gagal atau dibatalkan di server ini, database yang juga ada di master baru bisa sudah dihapus … periksa dulu daftar database-nya (`mysql -e 'SHOW DATABASES;'`)."
- Evidence: a rebuild that reached the load (dump: after the drop, binlog reset and partial load; CLONE: data directory replaced) leaves databases that exist but hold new-master or partial data. `SHOW DATABASES` cannot reveal that.
- Harm: an operator falling back to this server as master could check the list, see every name present, and put applications on half-replaced data.
- Corrected: "Bila bangun ulang pernah gagal atau dibatalkan di server ini, data MySQL-nya bisa sudah sebagian dihapus atau diganti salinan master baru (lihat "Data tertinggal sesudah bangun ulang"). Daftar database saja tidak membuktikan isinya utuh: bila panel pernah menulis "Bangun ulang sebelumnya sudah mulai menyalin data ke server ini" atau "… berhenti di tahap Salin data …" untuk server ini, jangan jadikan server ini master lagi tanpa admin MySQL."

**m-I2. "catatan transaksi lama sudah hilang" overclaims when the binlog is not reset.** README 1991-1994.
- Evidence: in COMMENTED mode (a partial dump into a server with replication configured, dump.go:238-243) the binlog is not reset, yet the panel counts the copy as started and refuses Periksa/Amankan.
- Harm: rare; the operator is told the transactions are gone while the binlog still holds them, so they lose a salvage the server could still give.
- Corrected: "… catatan transaksi lama bisa sudah hilang, dan panel tidak lagi memeriksanya. …"

**m-I3. The backup is sent to "server lain" with no word on who may hold it.** README 2137-2142.
- Evidence: the file contains a hospital database (patient data).
- Harm: an operator may copy patient data to any reachable machine or personal medium.
- Add: "Pakai server atau media milik rumah sakit yang aksesnya dibatasi; berkas itu berisi seluruh isi database, termasuk data pasien."

**m-I4. Small usability gaps in two of the new instructions.** README 1918-1922 and 2140.
- The 1396 branch says `exit` and then the normal path says "Ketik `exit`" again. "ulangi dengan nama akun lain" does not say that the name must be changed in both the `CREATE USER` and the `GRANT` line, and in **Hubungkan**.
- The local `sha256sum` command is implied, not written.
- Harm: hesitation only. Nothing is lost.
- Corrected: "… lalu ulangi blok itu dengan nama akun lain (ganti nama di baris `CREATE USER` dan `GRANT`, dan pakai nama itu di **Hubungkan ulang**)." And: "… jalankan `sha256sum '/root/<nama>-cadangan.sql'` di server ini dan `sha256sum '<nama>-cadangan.sql'` di server lain …"

## Contradictions created by the edit

None found.
- The Batalkan qualifier (1976-1978) reconciles the panel text with the NOT STARTED bullet.
- "Hapus server … tidak mengubah apa pun" (2258) describes the delete action itself and still holds.
- README ~1384 ("data itu tetap utuh di master lama") concerns databases absent from the new master, which are never in the WipeList, so it still holds.
- The panel's own wait text "database dari percobaan sebelumnya akan dihapus …" (create_replica.go:499) still mislabels an old master's production databases. That is code, out of scope, and the writer reported it.

## Merge judgment

Nothing found in this round should hold the merge. All nine items are addressed. The four new findings are Minor wording or usability gaps, and none leads to lost data, a writable old master or an operator without a next step.
