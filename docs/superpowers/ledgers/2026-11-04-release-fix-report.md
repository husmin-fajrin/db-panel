# Release fixes after Plan 3: report

Branch: current (not pushed). Four commits on top of 74de5f1:

- a57dbfa fix(agent): a pinned master creates dbpanel_repl once, only at '%'
- ff0cd73 fix(monitor): the fresh-report hold never hides Agent offline, and is capped
- c6ab3ba fix(web): settings_update audits the whole panel base URL and chat id
- 9eeb6e4 docs: an empty panel base URL means messages carry no link

I wrote each test first and saw it fail for the expected reason before making the fix.

## R1 (I-1, SEC-42): host pin for the replication account

**Changes**
- `internal/agent/drivers/mysql/pins.go` (`refusedByRole`): on a pinned master, `mysql.createReplUser` is refused unless `User == "dbpanel_repl"` and `FromHost == "%"`. `validateReplUser` turns an empty host into "%" before `allow` runs, so prepare_master's call is unaffected.
- `internal/agent/drivers/mysql/users.go`:
  - `createAccount(keepExisting)` now refuses when the user has a row at any host. The new helper `accountHosts` reads them.
  - Before, it refused only when the exact user@host row existed.
  - `accountExistsOnMaster` now names every existing row. The wording "sudah ada di master yang dipin" is unchanged, so prepare_master's match still works.
  - This also applies to `createDumpUser` on a pinned master. No legitimate flow is affected, because each dump job uses a random `dbpanel_dump_<8hex>` name.
- `internal/panel/orchestrator/prepare_master.go`: the "account already exists although the password is new" error now ends with "Pesan agent: <err>". The agent's text names the existing rows, which helps when a row sits at a host other than '%'. The re-run flow is unchanged: if the account exists and was not just created, it is kept and a log line says so.

**Tests**
- `TestMasterRefusesAccountTakeover` (no MySQL) adds refused cases for `dbpanel_repl` with hosts `10.0.0.%`, `10.0.0.5` and `localhost`, plus a past-pins case for an explicit `%`.
- `TestPinnedMasterKeepsAccountsAndRestartsOnce` (real mysqld) adds one case:
  1. root drops `dbpanel_repl@'%'` and creates `dbpanel_repl@'10.9.9.9'`;
  2. `createReplUser{%}` is then refused with "sudah ada di master yang dipin" and "10.9.9.9";
  3. `dbpanel_repl@'%'` is still absent afterwards.
- The existing case (a second create at '%' after one exists is refused) still passes.
- Before the fix:
  - 3 host cases failed as "MySQL tidak ditemukan" (they got past the pins);
  - the real-mysqld case created the account.
- After the fix: `go test ./internal/agent/drivers/mysql/ -run 'Pins|Takeover|Master' -count=1` gives ok (85 s).

## R2 (M2): the hold no longer hides "Agent offline", and its bound is capped

**Changes** (`internal/panel/monitor/state.go`)
- `derive`: when the hold is active, the status is still computed. It replaces the held one only if `heldThrough(code)`, meaning `StatusAgentOffline`, `StatusUnreachable` or `StatusNeedsUpgrade`. Any other result returns, keeping the held status as before.
- While the hold is active, the stale report does not move the gap clock (`!held &&` guard). This keeps the old behaviour, where derive returned before the gap update.
- `freshBound` is now `min(max(60 s, 2×CheckEvery), 10 min)`; it was `2×CheckEvery + configureTimeout`.
- The field comment on `awaitFresh` is updated.

**Tests** (`internal/panel/monitor/fresh_test.go`, new helper `pausedThenStarted`)
- `TestHoldDoesNotHideAgentOffline`: the hold is active, then the hub detaches the agent for 31 s and a tick runs. The status is Agent offline. Before the fix it was "Dihentikan".
- `TestHoldBound`: the bound is 60 s, 60 s, 90 s, 10 min and 10 min at CheckEvery 10 s, 15 s, 45 s, 5 min and 3600 s.
- `TestHoldExpiresAtCap`: CheckEvery and SummaryEvery are 3600 s.
  - At 10 min the status is still Dihentikan.
  - At 10 min + 1 s it becomes Berhenti (di luar panel).
  - Before the fix it stayed held.
- The existing fresh tests (`TestResyncReadyHoldsUntilFreshReport`, `TestStartFromPausedOpensNoAlert`, `TestFreshReportBoundFallsBack`) pass unchanged.

## R3 (M3): full base URL in the audit

**Changes**
- `internal/panel/web/audit.go`: new `auditFullKeys` = {panel_base_url, telegram_chat_id}. `limit` skips the 128-rune cut for a string value under those keys, at any map depth. The 2 KiB detail cap still applies; a 200-byte URL fits easily. The `audit` doc comment is updated.

**Tests**
- `TestSettingsUpdateAuditHasNoToken` (web_settings_test.go) saves a 200-byte base URL (`https://panel-rs1.internal.lan.<labels>.evil.example/`). The test asserts the detail holds the whole value and contains no "dipotong".
- Before the fix it was cut at 128 runes with "…(dipotong)". It passes now.

## R4 (M4): docs for an empty base URL

**Changes**
- `internal/panel/monitor/settings.go`: the `PanelBaseURL` comment now says that "" means messages carry no link, and that the settings page only suggests the public URL as the placeholder.
- `web/templates/settings.html`: the help text adds "Alamat abu-abu di kolom hanya saran (alamat publik panel), bukan nilai yang dipakai: ketik atau salin alamatnya bila pesan perlu tautan."
- `README.md`:
  - Telegram setup step 6 now says an empty field means no link and that the placeholder is only a suggestion;
  - the audit paragraph says the base URL value is recorded "utuh".

**Tests**
- The settings page test (web_settings_test.go, around line 149) asserts both sentences. It failed before the template change and passes after.

## Verification

Raw output: /tmp/claude-0/-home-user-db-panel/26bbb5e6-6a34-5a2d-a807-f83dee84386a/scratchpad/release-fix-plan3/verify.txt and e2e.txt in the same folder.

```
gofmt -l ./internal ./cmd                         -> (empty)
go vet ./...                                      -> ok
go test -race ./internal/panel/monitor/ ./internal/panel/web/ -count=1
  ok  internal/panel/monitor 13.532s
  ok  internal/panel/web     27.896s
go test ./internal/agent/drivers/mysql/ -run 'Pins|Takeover|Master' -count=1
  ok  internal/agent/drivers/mysql 85.185s
go test ./internal/panel/orchestrator/ -count=1
  ok  internal/panel/orchestrator 4.172s
make test-e2e
  ok  internal/panel 295.295s   (exit 0)
```

`graphify update .` was run after the changes. graphify-out is not tracked, so nothing is committed for it.

## Concerns (minor)

1. The any-host refusal in `createAccount` applies to dump accounts as well as `dbpanel_repl`. That is stricter than the brief asked for but harmless, because the names are random per job.
2. If a `dbpanel_repl` row exists only at a host other than '%' (created outside the panel), a re-run of "Siapkan master" whose topology password already exists still logs "User replikasi … sudah ada di master; sandinya tidak diubah". In that case no '%' account exists for the replicas. This edge only arises from out-of-band changes. The fresh-password path now includes the agent's message, which names the row.
3. With `heldThrough`, "Tidak terjangkau" from a stale pre-change report also replaces a held status, as the brief asked. In practice the fresh report fetched in the background corrects it.
