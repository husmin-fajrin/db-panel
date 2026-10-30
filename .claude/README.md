# Superpowers skills (vendored)

Skill dari [obra/superpowers](https://github.com/obra/superpowers) (MIT, lihat
`skills/LICENSE-superpowers`) disalin ke `.claude/skills/` supaya bisa dipakai
di semua jenis sesi, termasuk Claude Code on the web (cloud) yang tidak punya
`/plugin`.

## Cara pakai

Skill di-load otomatis saat sesi dimulai. Panggil dengan nama polos, misalnya:

- `/brainstorming` sebelum membangun fitur baru
- `/writing-plans` untuk memecah pekerjaan jadi task kecil
- `/executing-plans` atau `/subagent-driven-development` untuk mengeksekusi plan
- `/test-driven-development`, `/systematic-debugging`, `/requesting-code-review`
- `/finishing-a-development-branch` untuk menutup branch

Claude juga akan memanggil skill ini sendiri bila relevan.

Catatan: di versi plugin, skill dirujuk sebagai `superpowers:<nama>`. Di salinan
ini referensi tersebut diubah ke nama polos `<nama>` agar cocok dengan nama
project skill.

## Opsional: hook SessionStart

Plugin aslinya menyuntikkan `using-superpowers` ke konteks di setiap awal sesi.
Untuk meniru itu, tambahkan ke `.claude/settings.json`:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "startup|clear|compact",
        "hooks": [
          {
            "type": "command",
            "command": "bash \"$CLAUDE_PROJECT_DIR/.claude/hooks/superpowers-session-start.sh\"",
            "timeout": 10
          }
        ]
      }
    ]
  }
}
```

## Alternatif: install sebagai plugin (hanya di mesin lokal)

```
claude plugin install superpowers@claude-plugins-official
```

atau `/plugin install superpowers@claude-plugins-official` di dalam sesi.
Jangan gabungkan keduanya di satu mesin, karena skill akan muncul dua kali
(`/brainstorming` dan `/superpowers:brainstorming`).

## Update

```
git clone --depth 1 https://github.com/obra/superpowers /tmp/superpowers
rm -rf .claude/skills && mkdir .claude/skills
cp -R /tmp/superpowers/skills/. .claude/skills/
cp /tmp/superpowers/LICENSE .claude/skills/LICENSE-superpowers
grep -rl 'superpowers:[a-z-]' .claude/skills | xargs sed -i -E 's/superpowers:([a-z][a-z-]*)/\1/g'
```
