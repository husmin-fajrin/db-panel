# Review of c8bc7bd (Ulangi skips the gate for any job that drops no data)

Scope: 391e344..c8bc7bd. Read-only; no tests run. Graph queried first
(`graphify query` on Retry/Approve/JobGateInput/dropsData/gateActionForJob).

## Code the sentence describes

- `dropsData` runner.go:653-668: true for resync (always); create_replica with
  WipeList, RebuildOldMaster, WaitWipe, WaitClone, or copy not made
  (`SnapshotGTID == "" && !AdminReplaced`); remove_databases with DropData;
  add_databases with AddDrop or WaitAddExisting. False for every other kind:
  prepare_master, mysql_tls, tighten_tls, promote_planned, promote_emergency,
  leftover, salvage (kinds at runner.go:47-60).
- `gateActionForJob` runner.go:709-724 knows create_replica (incl. rebuild),
  resync, add_databases, remove_databases: exactly the four kinds `dropsData`
  can return true for. So no kind drops data and escapes the gate for want of
  a gate action. prepare_master (has a gated start, gate.go:42) is unknown to
  it but never drops data.
- `JobGateInput` runner.go:733-743: judged only when drops && known action.
- `Retry` runner.go:967-1031: `promoteRetryRefused` (L994, promote.go:1725-1734)
  before `refuseRedJobGate` (L997). `Approve` runner.go:907-934 calls
  `refuseRedJobGate` (L927).
- No other data-dropping job steps found in oldmaster.go / prepare_master.go /
  tls.go / promote.go (SalvageDelete oldmaster.go:401 is a direct call, not a job).

## a. Truth of the sentence in the four places — OK, with two Rendah and one Info

True in all four (README:1527-1531, README:3444-3447, panduan.html:91,
README:4290-4294). Every job whose remaining steps drop data is re-judged on
Ulangi and Setujui; every other job is not. "itu termasuk promote terencana
yang gagal" is true (promote not in `gateActionForJob`, `promoteRetryRefused`
false for planned). Nothing wrongly suggests the emergency promote has Ulangi:
the sentence names the planned promote only, and is also (vacuously) true for
the emergency one (refused before PromoteSent by L994, not judged after).
"Promote darurat tidak pernah ditolak oleh sebuah baris" stays true
(gate.go:36).

- Rendah (A1): "pekerjaan yang membuang data" means the job's *remaining* steps
  (dropsData), as README:4176-4180 defines it ("Buat replika yang salinannya
  belum dibuat ..."). The guide page (panduan.html:91, the only occurrence of
  "membuang data" on that page) and README:1527/3445 give no such definition
  or pointer. An operator reading "Buat replika" as a data-dropping job would
  expect its Ulangi after the copy (or WaitReconnect) to be re-judged; it is
  not. Consequence small: those remaining steps drop nothing.
- Rendah (A2): the guide paragraph and limits list mention only Ulangi. Setujui
  of a job that drops no data is likewise not re-judged (Approve L927 via
  JobGateInput), e.g. prepare_master WaitRestart, create_replica WaitReconnect.
  README:1525-1527 implies this ("lagi pada Ulangi dan Setujui pekerjaan yang
  membuang data"); 3444-3447 and 4290-4294 do not. An operator could conclude
  a red row still blocks such a Setujui.
- Info (A3): a job whose params cannot be read counts as dropping
  (jobDropsData L750) but is not judged (JobGateInput L735 returns false on
  err); newRun fails it before any step, so nothing is dropped. Not worth text.

## b. Guide page vs README guide; "only one" — OK

panduan.html:91 and README:3444-3447 say the same three sentences word for
word (bold/strong only). `grep -i 'pengecualian'` on README.md and
web/templates finds nothing; the remaining "satu-satunya"/"hanya satu" hits
(README:1404, 1501, 1911, 2051, 3041, 4302; panduan.html:32) are unrelated.
README:2901-2904 (Promote) speaks only of the planned promote and does not
say "only".

## c. Commit scope and plan quotes — OK (Info)

`git show --stat c8bc7bd`: README.md, panduan.html, plan file only. The diff
(content same as the review package; the package just has wider context) only
changes the four sentences, their rewrapping, plan entries L1229-1231 and the
new sub-section "Perbaikan kata sesudah audit" at plan L1233-1240, which is
the end of "## Teks baru untuk dibaca pemilik" (L990; end of file). Old
quotes match 391e344 exactly (bold markup dropped, as elsewhere in the plan);
new quotes match the new text. Its intro ("Ulangi menilai lagi daftar
Prasyarat hanya untuk pekerjaan yang membuang data, dan promote tidak pernah
dinilai lagi") is true.
- Info: L1229-1231 now quote the current text as the outcome of the earlier
  audit wave instead of what that wave wrote; the new sub-section records the
  step, so the history is still readable.

## d. Indonesian wording — OK

"**Ulangi dari tahap ini** pada pekerjaan yang tidak membuang data tidak
menilai lagi daftar **Prasyarat** [barisnya]; itu termasuk promote terencana
yang gagal." Two negations in one clause, but the subject is clear and "itu"
reads as "pekerjaan yang tidak membuang data". Nothing misleading beyond A1
(the undefined term on the guide page).
