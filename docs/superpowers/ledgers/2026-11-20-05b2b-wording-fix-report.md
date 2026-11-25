# Wording fix: Ulangi skips the gate for any job that drops no data

Commit c8bc7bd (pushed).

## Code checked
- internal/panel/orchestrator/runner.go L653-668 `dropsData`: true for resync; create_replica with leftovers, rebuild, WaitWipe/WaitClone, or copy not made; remove_databases with DropData; add_databases with AddDrop/WaitAddExisting; false for every other kind (promote, setup master).
- L705-723 `gateActionForJob`: promote never re-judged (K4); kinds without a gated start return ok=false.
- L726-743 `JobGateInput`: gate judged on Retry/Approve only when dropsData and gateActionForJob are both true; Retry calls `refuseRedJobGate` (L~997).

## Old -> new
1. README "Prasyarat": "Satu pengecualian: **Ulangi dari tahap ini** pada promote terencana yang gagal melanjutkan promote itu tanpa menilai lagi daftar **Prasyarat**." -> "**Ulangi dari tahap ini** pada pekerjaan yang tidak membuang data tidak menilai lagi daftar **Prasyarat**; itu termasuk promote terencana yang gagal."
2. README guide "Memperbaiki baris merah": "Satu pengecualian: **Ulangi dari tahap ini** pada promote terencana yang gagal tidak menilai lagi barisnya." -> "**Ulangi dari tahap ini** pada pekerjaan yang tidak membuang data tidak menilai lagi barisnya; itu termasuk promote terencana yang gagal."
3. web/templates/panduan.html L91: same as 2 (HTML).
4. README limits list: "...saat tindakan itu dimulai; satu pengecualian: **Ulangi dari tahap ini** pada promote terencana yang gagal tidak menilai lagi barisnya." -> "...saat tindakan itu dimulai. **Ulangi dari tahap ini** pada pekerjaan yang tidak membuang data tidak menilai lagi barisnya; itu termasuk promote terencana yang gagal."

Plan: the three owner-text entries (L1229-1231) now quote the new sentences; new sub-section "Perbaikan kata sesudah audit (keputusan pemilik 2026-11-25)" appended.

## Checks
- `grep -rn -i 'satu pengecualian' README.md web/templates/` -> no output.
- `go test ./internal/panel/web/ -run 'Guide|Panduan|Layout|Header|Inline' -count=1` -> `ok github.com/husmin-fajrin/db-panel/internal/panel/web 0.846s`
- diff: README.md, web/templates/panduan.html, plan file only; `graphify update .` run.
