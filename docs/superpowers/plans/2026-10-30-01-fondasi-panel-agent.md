# Rencana Implementasi 1: Fondasi Panel dan Agent

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Satu binary Go `dbpanel` yang bisa dipasang sebagai panel web
(login, daftar server) dan sebagai agent (pairing, koneksi mTLS, perintah
`probe`), lengkap dengan skrip instalasi satu perintah untuk keduanya.

**Architecture:** Panel = HTTP server untuk UI (port 8080, template HTML
dirender server + Alpine.js, SSE untuk data hidup) + listener mTLS untuk
agent (port 8443, WebSocket) + SQLite. Agent = proses root yang membuka
koneksi keluar ke panel, menerima perintah dari daftar tetap (registry),
menjawab dengan korelasi id. Host panel memakai agent tertanam
(in-process) lewat antarmuka `hub.Conn` yang sama dengan agent jarak jauh.

**Tech Stack:** Go 1.24 (CGO_ENABLED=0), `net/http`, `html/template`,
`embed`, `github.com/coder/websocket v1.8.15`, `modernc.org/sqlite v1.61.0`,
`golang.org/x/crypto v0.58.0` (bcrypt), `gopkg.in/yaml.v3 v3.0.1`;
UI: daisyUI 5 + Tailwind CSS 4 (standalone CLI), Alpine.js 3.

**Spec:** `docs/superpowers/specs/2026-10-30-db-replica-panel-design.md`
(bagian 6.1–6.5, 7.1, 7.2, 8, 9.1, 9.2, 9.9, 9.11, 9.14).

## Global Constraints

- Go 1.24, `CGO_ENABLED=0`, target `linux/amd64` dan `linux/arm64`; semua
  dependensi pure Go.
- Modul: `github.com/husmin-fajrin/db-panel`. Struktur: `cmd/dbpanel`,
  `internal/panel`, `internal/agent`, `internal/proto`, `web/`, `scripts/`.
- Tidak ada Node.js di mana pun. `web/static/app.css` hasil kompilasi
  Tailwind dan file vendor (`alpine.min.js`) dikomit.
- Semua aset UI tertanam di binary (`embed`), UI berbahasa Indonesia.
- Agent hanya menjalankan perintah dari registry dengan parameter
  terstruktur; tidak pernah menjalankan string perintah dari panel.
- Agent membuka koneksi keluar; server database tidak membuka port untuk
  panel. Koneksi agent↔panel mTLS dengan CA internal panel.
- Path default: binary `/usr/local/bin/dbpanel`, config `/etc/dbpanel/`,
  data panel `/var/lib/dbpanel/`. Service systemd `dbpanel-panel` dan
  `dbpanel-agent`.
- Password dan kunci privat tidak pernah ditulis ke log.
- Setiap task diakhiri `go vet ./... && go test ./...` hijau dan commit.

## Review Focus

1. Token pairing yang sudah dipakai atau kedaluwarsa harus ditolak dengan
   HTTP 403, bukan diterima atau 500 (Task 4 dan Task 7 menguji).
2. Agent yang menyodorkan sertifikat untuk server yang sudah dihapus dari
   panel harus ditolak saat upgrade WebSocket (Task 7 menguji).
3. Agent yang putus koneksi harus terlihat offline dalam ≤ 30 detik dan
   kembali online sendiri tanpa restart (Task 7 menguji reconnect).
4. Dua koneksi untuk server yang sama (VM di-clone) tidak boleh membuat
   dua entri; koneksi baru menggantikan yang lama (Task 6 menguji).
5. Login dengan password salah berulang harus dibatasi (rate limit) dan
   `probe` di server tanpa MySQL harus sukses dengan `Installed=false`,
   bukan error (Task 8 dan Task 3 menguji).

---

### Task 1: Modul Go dan kerangka CLI

**Files:**
- Create: `go.mod`, `cmd/dbpanel/main.go`, `internal/version/version.go`,
  `.gitignore`, `Makefile`
- Test: `cmd/dbpanel/main_test.go`

**Interfaces:**
- Produces: `version.Version string` (diisi lewat `-ldflags -X`, default
  `"dev"`); `func run(args []string, stdout, stderr io.Writer) int` di
  `main.go` dengan subperintah `panel`, `agent`, `version`.
  Subperintah `panel` dan `agent` di task ini hanya placeholder yang
  mencetak "belum diimplementasikan" dan mengembalikan 2; task berikutnya
  mengisinya.

- [ ] **Step 1: Buat `go.mod`** dengan `module github.com/husmin-fajrin/db-panel`
  dan `go 1.24`. `.gitignore`: `/dist/`, `/.tools/`, `*.db`, `/tmp/`.
  `Makefile` target `build` (`CGO_ENABLED=0 go build -ldflags "-X
  github.com/husmin-fajrin/db-panel/internal/version.Version=$(VERSION)"
  -o dist/dbpanel ./cmd/dbpanel`), `test` (`go vet ./... && go test ./...`).

- [ ] **Step 2: Tulis test yang gagal** di `cmd/dbpanel/main_test.go`:

```go
func TestRunVersion(t *testing.T) {
    var out bytes.Buffer
    code := run([]string{"version"}, &out, io.Discard)
    if code != 0 || !strings.Contains(out.String(), "dbpanel dev") { t.Fatalf("got %d %q", code, out.String()) }
}
func TestRunUnknownCommand(t *testing.T) {
    var errb bytes.Buffer
    code := run([]string{"apa"}, io.Discard, &errb)
    if code != 2 || !strings.Contains(errb.String(), "Penggunaan:") { t.Fatalf("got %d %q", code, errb.String()) }
}
func TestRunNoArgsPrintsUsage(t *testing.T) { /* sama seperti unknown, code 2 */ }
```

- [ ] **Step 3: Jalankan test, pastikan gagal** — `go test ./cmd/...`
  Expected: FAIL (run undefined).

- [ ] **Step 4: Implementasi** `run` dan `main()` (`os.Exit(run(os.Args[1:],
  os.Stdout, os.Stderr))`). Teks usage berbahasa Indonesia, diawali
  `Penggunaan: dbpanel <panel|agent|version>`.

- [ ] **Step 5: Jalankan test, pastikan lulus** — `go test ./cmd/...`
  Expected: PASS. Lalu `make build` menghasilkan `dist/dbpanel`.

- [ ] **Step 6: Commit** — `git add go.mod .gitignore Makefile cmd internal/version && git commit -m "feat: scaffold dbpanel CLI"`

---

### Task 2: Protokol pesan panel↔agent

**Files:**
- Create: `internal/proto/messages.go`, `internal/proto/codec.go`,
  `internal/proto/probe.go`
- Test: `internal/proto/codec_test.go`

**Interfaces:**
- Produces (semua di package `proto`):

```go
type Request  struct { ID string `json:"id"`; Cmd string `json:"cmd"`; Params json.RawMessage `json:"params,omitempty"` }
type Error    struct { Code string `json:"code"`; Message string `json:"message"` }
type Response struct { ID string `json:"id"`; OK bool `json:"ok"`; Result json.RawMessage `json:"result,omitempty"`; Error *Error `json:"error,omitempty"` }
type Event    struct { Type string `json:"type"`; JobID string `json:"job_id,omitempty"`; Payload json.RawMessage `json:"payload,omitempty"` }
type Envelope struct { Kind string `json:"kind"`; Request *Request `json:"request,omitempty"`; Response *Response `json:"response,omitempty"`; Event *Event `json:"event,omitempty"` }
const ( KindRequest = "request"; KindResponse = "response"; KindEvent = "event" )
func Encode(e Envelope) ([]byte, error)
func Decode(b []byte) (Envelope, error)          // error bila kind tidak dikenal atau field yang sesuai nil
func NewResponse(id string, result any) (Response, error)
func ErrorResponse(id, code, msg string) Response
const ( CodeUnknownCommand = "unknown_command"; CodeBadParams = "bad_params"; CodeInternal = "internal"; CodeOffline = "offline"; CodeTimeout = "timeout" )
```

  dan di `probe.go`:

```go
type DiskInfo  struct { Mount string; TotalMB, UsedMB int64 }
type MySQLInfo struct { Installed bool; Version string; ServiceName string; ServiceActive bool; Port int; DataDir string }
type ProbeResult struct {
    Hostname, OS, OSFamily, Arch, Kernel string   // OSFamily: "debian" | "rhel" | "unknown"
    CPUCores int; MemTotalMB int64
    Disks []DiskInfo
    MySQL MySQLInfo
    AgentVersion string
}
```
  Semua field pakai tag JSON snake_case.

- [ ] **Step 1: Tulis test** `TestEncodeDecodeRoundTrip` (request, response,
  event), `TestDecodeRejectsUnknownKind` (`{"kind":"x"}` → error),
  `TestDecodeRejectsMissingBody` (`{"kind":"request"}` → error),
  `TestErrorResponseHasOKFalse`.

- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/proto/`

- [ ] **Step 3: Implementasi** sesuai signature di atas.

- [ ] **Step 4: Jalankan, pastikan lulus.**

- [ ] **Step 5: Commit** — `git commit -m "feat(proto): message envelope and probe types"`

---

### Task 3: Registry perintah agent dan perintah `probe`

**Files:**
- Create: `internal/agent/commands/registry.go`,
  `internal/agent/probe/probe.go`, `internal/agent/probe/system.go`
- Test: `internal/agent/commands/registry_test.go`,
  `internal/agent/probe/probe_test.go`
- Testdata: `internal/agent/probe/testdata/{os-release-ubuntu,os-release-rocky,meminfo,cpuinfo}`

**Interfaces:**
- Consumes: `proto.*` dari Task 2.
- Produces:

```go
// package commands
type Handler func(ctx context.Context, params json.RawMessage) (any, error)
type Registry struct{ /* map[string]Handler, mutex */ }
func NewRegistry() *Registry
func (r *Registry) Register(name string, h Handler)        // panic bila nama ganda
func (r *Registry) Names() []string                         // terurut
func (r *Registry) Dispatch(ctx context.Context, req proto.Request) proto.Response
// Dispatch: unknown → ErrorResponse(CodeUnknownCommand); handler error → CodeInternal dengan err.Error();
// handler panic → ditangkap, CodeInternal; hasil di-marshal ke Result.
type ParamError struct{ Msg string }                        // handler mengembalikan ini → CodeBadParams
func (e *ParamError) Error() string

// package probe
type System interface {
    ReadFile(path string) ([]byte, error)
    LookPath(file string) (string, error)
    Output(ctx context.Context, name string, args ...string) ([]byte, error)
    Hostname() (string, error)
    Statfs(path string) (totalBytes, freeBytes uint64, err error)
    Arch() string
}
func RealSystem() System
func Collect(ctx context.Context, sys System, agentVersion string) (proto.ProbeResult, error)
func Register(r *commands.Registry, sys System, agentVersion string)   // mendaftarkan "probe"
```

Aturan `Collect`:
- OS dari `/etc/os-release` (`PRETTY_NAME`), `OSFamily` dari `ID`/`ID_LIKE`
  (mengandung `debian`/`ubuntu` → "debian"; `rhel`/`fedora`/`centos`/`rocky`/
  `almalinux` → "rhel"; lainnya "unknown"). Kernel dari `uname -r`.
- CPU: hitung baris `processor` di `/proc/cpuinfo`. Mem: `MemTotal` di
  `/proc/meminfo` (kB → MB).
- Disks: `Statfs` untuk `/` dan, bila MySQL terpasang dan `DataDir`
  diketahui, `DataDir`; satu entri per mount unik.
- MySQL: `Installed` bila `LookPath("mysqld")` sukses. `Version` dari
  `mysqld --version` (ambil token setelah `Ver `, mis. `8.4.3`).
  `ServiceName`: yang pertama dari `mysql`, `mysqld` yang
  `systemctl is-active <n>` mengembalikan `active` atau `inactive` (bukan
  `unknown`/error); `ServiceActive` bila `active`. `Port` dan `DataDir`
  dari `mysqld --verbose --help` baris `port` dan `datadir` (nilai kolom
  kedua); default port 3306, DataDir kosong bila gagal.
- Kegagalan satu sub-pemeriksaan tidak menggagalkan `Collect`; field
  dibiarkan nol/kosong. `Collect` hanya error bila `Hostname()` gagal.

- [ ] **Step 1: Test registry**: `TestDispatchUnknownCommand` (Code ==
  CodeUnknownCommand), `TestDispatchMarshalsResult`, `TestDispatchParamError`
  (Code == CodeBadParams), `TestDispatchRecoversPanic`,
  `TestRegisterDuplicatePanics`.

- [ ] **Step 2: Test probe** dengan `fakeSystem` (map path→bytes, map
  cmd→output): `TestCollectUbuntuWithMySQL` (OSFamily "debian", Version
  "8.4.3", ServiceName "mysql", ServiceActive true, Port 3306, DataDir
  "/var/lib/mysql", 2 disk), `TestCollectRockyWithoutMySQL` (OSFamily
  "rhel", `MySQL.Installed == false`, tidak error),
  `TestCollectToleratesMissingProcFiles` (CPUCores 0, MemTotalMB 0, tidak
  error).

- [ ] **Step 3: Jalankan, pastikan gagal.**

- [ ] **Step 4: Implementasi** registry, `RealSystem` (os, exec, syscall.Statfs,
  runtime.GOARCH), `Collect`, `Register`.

- [ ] **Step 5: Jalankan, pastikan lulus.** Tambahan manual: `go run
  ./cmd/dbpanel` belum perlu; cukup test.

- [ ] **Step 6: Commit** — `git commit -m "feat(agent): command registry and probe"`

---

### Task 4: Penyimpanan panel (SQLite)

**Files:**
- Create: `internal/panel/store/store.go`, `internal/panel/store/migrate.go`,
  `internal/panel/store/migrations/0001_init.sql`,
  `internal/panel/store/servers.go`, `internal/panel/store/tokens.go`,
  `internal/panel/store/users.go`, `internal/panel/store/settings.go`
- Test: `internal/panel/store/store_test.go`

**Interfaces:**
- Produces:

```go
type Store struct{ db *sql.DB }
func Open(path string) (*Store, error)     // DSN modernc "file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"; jalankan migrasi (tabel schema_migrations)
func (s *Store) Close() error

const ( RoleUnassigned = "unassigned"; RoleMaster = "master"; RoleReplica = "replica" )
type Server struct {
    ID int64; Name string; Address string; Role string
    Paired bool; CertSerial string
    ProbeJSON string              // JSON proto.ProbeResult, "" bila belum ada
    AgentVersion string
    LastSeenAt *time.Time; CreatedAt time.Time
}
var ErrNotFound = errors.New("tidak ditemukan")
func (s *Store) CreateServer(name string) (Server, error)        // nama unik, error bila kosong/ganda
func (s *Store) ListServers() ([]Server, error)                  // urut nama
func (s *Store) GetServer(id int64) (Server, error)
func (s *Store) MarkPaired(id int64, certSerial string, address string) error
func (s *Store) UpdateProbe(id int64, probe proto.ProbeResult, agentVersion string, seen time.Time) error
func (s *Store) TouchSeen(id int64, seen time.Time) error
func (s *Store) DeleteServer(id int64) error

type PairToken struct { Token string; ServerID int64; ExpiresAt time.Time; UsedAt *time.Time }
var ErrTokenInvalid = errors.New("token tidak valid atau kedaluwarsa")
func (s *Store) CreatePairToken(serverID int64, now time.Time, ttl time.Duration) (PairToken, error)  // token "XXXX-XXXX-XXXX" Crockford base32 dari crypto/rand; membatalkan token lama server itu
func (s *Store) PeekPairToken(token string, now time.Time) (PairToken, error)     // valid & belum dipakai, tidak mengubah
func (s *Store) ConsumePairToken(token string, now time.Time) (PairToken, error)  // atomik: set used_at; ErrTokenInvalid bila sudah dipakai/kedaluwarsa/tidak ada
func (s *Store) ActivePairToken(serverID int64, now time.Time) (PairToken, bool, error)

type User struct { ID int64; Username string; PasswordHash string }
func (s *Store) CreateUser(username, hash string) error
func (s *Store) GetUser(username string) (User, error)     // ErrNotFound
func (s *Store) SetPasswordHash(username, hash string) error
func (s *Store) CountUsers() (int, error)

func (s *Store) GetSetting(key string) (string, error)     // "" bila tidak ada
func (s *Store) SetSetting(key, value string) error
```

Skema `0001_init.sql`: `servers(id, name UNIQUE, address, role, paired, cert_serial, probe_json, agent_version, last_seen_at, created_at)`, `pair_tokens(token PK, server_id FK ON DELETE CASCADE, expires_at, used_at)`, `users(id, username UNIQUE, password_hash)`, `settings(key PK, value)`, `schema_migrations(version PK)`. Waktu disimpan sebagai RFC3339 UTC teks.

- [ ] **Step 1: Tulis test** (store di `t.TempDir()`):
  `TestOpenRunsMigrationsIdempotently` (Open dua kali), `TestServerCRUD`,
  `TestCreateServerRejectsDuplicateName`, `TestPairTokenLifecycle`
  (create → peek ok → consume ok → consume lagi ErrTokenInvalid),
  `TestPairTokenExpires` (now > ExpiresAt → ErrTokenInvalid),
  `TestCreatePairTokenReplacesOld` (token lama jadi invalid),
  `TestDeleteServerCascadesTokens`, `TestUsersAndSettings`.

- [ ] **Step 2: Jalankan, pastikan gagal.**

- [ ] **Step 3: Implementasi.** `go get modernc.org/sqlite@v1.61.0`.
  Migrasi dibaca dari `embed.FS` urut nama file.

- [ ] **Step 4: Jalankan, pastikan lulus.** Juga `CGO_ENABLED=0 go build ./...`.

- [ ] **Step 5: Commit** — `git commit -m "feat(panel): sqlite store with servers, tokens, users, settings"`

---

### Task 5: CA internal dan sertifikat

**Files:**
- Create: `internal/panel/ca/ca.go`
- Test: `internal/panel/ca/ca_test.go`

**Interfaces:**
- Produces:

```go
const PanelServerName = "dbpanel-panel"      // SAN DNS sertifikat server panel; agent set tls.Config.ServerName ini
type CA struct { Cert *x509.Certificate; Key *ecdsa.PrivateKey }
func LoadOrCreate(dir string) (*CA, error)   // dir/ca.crt + dir/ca.key (0600); ECDSA P-256, CN "dbpanel-ca", 20 tahun
func (c *CA) CAPEM() []byte
func (c *CA) IssueAgentCert(csrPEM []byte, serverID int64) (certPEM []byte, serial string, err error)
//   CN wajib "agent-<serverID>" (ditimpa dari serverID, bukan dari CSR), ExtKeyUsageClientAuth, 10 tahun, serial acak 128-bit hex
func (c *CA) IssueServerCert() (certPEM, keyPEM []byte, err error)   // SAN DNS PanelServerName, ExtKeyUsageServerAuth, 10 tahun
func (c *CA) AgentListenerTLS() (*tls.Config, error)   // IssueServerCert + ClientAuth RequireAndVerifyClientCert + ClientCAs = CA; MinVersion TLS1.3
func ServerIDFromCert(cert *x509.Certificate) (int64, error)   // parse CN "agent-<id>"
func NewAgentKeyAndCSR(hostname string) (keyPEM, csrPEM []byte, err error)  // ECDSA P-256, CN diisi hostname (akan ditimpa)
func AgentClientTLS(certPEM, keyPEM, caPEM []byte) (*tls.Config, error)      // RootCAs = CA, ServerName = PanelServerName
```

- [ ] **Step 1: Tulis test**: `TestLoadOrCreatePersists` (dua kali Load →
  serial CA sama; file ca.key mode 0600), `TestIssueAgentCertSetsCN`
  (CSR dengan CN "apa-saja" → cert CN "agent-42", `ServerIDFromCert` 42),
  `TestServerIDFromCertRejectsOther` (CN "x" → error),
  `TestMutualTLSHandshake` (httptest server dengan `AgentListenerTLS`,
  klien dengan `AgentClientTLS` → 200; klien tanpa cert → error; klien
  dengan cert dari CA lain → error).

- [ ] **Step 2: Jalankan, pastikan gagal.**

- [ ] **Step 3: Implementasi** dengan `crypto/x509`, `crypto/ecdsa`,
  `encoding/pem`.

- [ ] **Step 4: Jalankan, pastikan lulus.**

- [ ] **Step 5: Commit** — `git commit -m "feat(panel): internal CA for agent mTLS"`

---

### Task 6: Hub koneksi agent di panel

**Files:**
- Create: `internal/panel/hub/hub.go`, `internal/panel/hub/conn_ws.go`,
  `internal/panel/hub/conn_local.go`
- Test: `internal/panel/hub/hub_test.go`, `internal/panel/hub/conn_ws_test.go`

**Interfaces:**
- Consumes: `proto.*`, `commands.Registry` (Task 3).
- Produces:

```go
type Conn interface {
    Call(ctx context.Context, cmd string, params any, result any) error  // marshal params, unmarshal Result ke result (boleh nil); error bertipe *proto.Error bila OK=false
    Close() error
}
type Listener interface {
    OnOnline(serverID int64)
    OnOffline(serverID int64)
    OnEvent(serverID int64, ev proto.Event)
}
type Hub struct{ /* map[int64]Conn, mutex, listener */ }
func New(l Listener) *Hub
func (h *Hub) Attach(serverID int64, c Conn)    // bila sudah ada koneksi lama: Close() yang lama, tanpa OnOffline; OnOnline dipanggil untuk yang baru
func (h *Hub) Detach(serverID int64, c Conn)    // hanya bila c adalah koneksi aktif; lalu OnOffline
func (h *Hub) Call(ctx context.Context, serverID int64, cmd string, params any, result any) error   // ErrOffline bila tidak ada
func (h *Hub) IsOnline(serverID int64) bool
func (h *Hub) OnlineIDs() []int64
var ErrOffline = errors.New("agent tidak terhubung")

// conn_ws.go — sisi panel
func NewWSConn(ws *websocket.Conn, onEvent func(proto.Event)) *WSConn
//   goroutine baca: Response → pending[ID] channel; Event → onEvent. Call: ID = 16 byte hex acak, tulis Envelope request, tunggu channel atau ctx.Done (→ proto.Error CodeTimeout). Tulis diserialisasi dengan mutex. Close: tutup ws, gagalkan semua pending dengan CodeOffline.
func (c *WSConn) Wait() <-chan struct{}        // ditutup saat loop baca berakhir (koneksi putus)

// conn_local.go
func NewLocalConn(reg *commands.Registry, onEvent func(proto.Event)) Conn   // Dispatch langsung; Close no-op
```

- [ ] **Step 1: Test hub** dengan `fakeConn` (merekam Call, Close):
  `TestAttachReplacesExistingAndClosesOld` (OnOnline 2x, OnOffline 0x,
  Close lama 1x), `TestDetachOnlyActive` (Detach conn lama → tidak
  OnOffline), `TestCallOfflineReturnsErrOffline`, `TestLocalConnDispatch`
  (registry dengan handler "echo" → result sama).

- [ ] **Step 2: Test WSConn** dengan `httptest.Server` yang meng-upgrade
  websocket dan berperan sebagai agent palsu: `TestWSConnCallRoundTrip`,
  `TestWSConnEventDelivered`, `TestWSConnCallTimeout` (agent palsu diam,
  ctx 100 ms → CodeTimeout), `TestWSConnCloseFailsPending` (CodeOffline,
  dan `Wait()` tertutup).

- [ ] **Step 3: Jalankan, pastikan gagal.**

- [ ] **Step 4: Implementasi.** `go get github.com/coder/websocket@v1.8.15`.
  Batas ukuran pesan 4 MiB (`ws.SetReadLimit`).

- [ ] **Step 5: Jalankan, pastikan lulus** (`go test -race ./internal/panel/hub/`).

- [ ] **Step 6: Commit** — `git commit -m "feat(panel): agent connection hub"`

---

### Task 7: Runtime agent, pairing, dan endpoint agent di panel

**Files:**
- Create: `internal/agent/config/config.go`, `internal/agent/pair.go`,
  `internal/agent/runtime.go`, `internal/agent/cli.go`,
  `internal/panel/agentapi/pair.go`, `internal/panel/agentapi/ws.go`,
  `internal/panel/agentapi/listener.go`
- Modify: `cmd/dbpanel/main.go` (subperintah `agent` → `agent.Main`)
- Test: `internal/agent/config/config_test.go`,
  `internal/panel/agentapi/agentapi_test.go` (integrasi panel↔agent)

**Interfaces:**
- Consumes: Task 2–6.
- Produces:

```go
// package config (agent)
type Config struct {
    PanelAddr string `yaml:"panel_addr"`   // host:port listener mTLS, mis. "192.168.1.10:8443"
    ServerID  int64  `yaml:"server_id"`
    CertFile string `yaml:"cert_file"`   // default <dir>/agent.crt
    KeyFile  string `yaml:"key_file"`    // default <dir>/agent.key
    CAFile   string `yaml:"ca_file"`     // default <dir>/ca.crt
    MySQLRootPassword string `yaml:"mysql_root_password,omitempty"`  // dipakai rencana 2
}
const DefaultDir = "/etc/dbpanel"
func Load(dir string) (Config, error)          // dir/agent.yaml; ErrNotPaired bila tidak ada
func Save(dir string, c Config) error          // 0600, mkdir 0700
var ErrNotPaired = errors.New("agent belum dipasangkan (jalankan: dbpanel agent pair)")

// package agent
type PairRequest  struct { Token string `json:"token"`; Hostname string `json:"hostname"`; Arch string `json:"arch"`; CSRPEM string `json:"csr_pem"` }
type PairResponse struct { ServerID int64 `json:"server_id"`; CertPEM string `json:"cert_pem"`; CAPEM string `json:"ca_pem"`; AgentAddr string `json:"agent_addr"` }
func Pair(ctx context.Context, panelURL, token, dir string, client *http.Client) (config.Config, error)
//   POST {panelURL}/agent/pair; 403 → error "token ditolak"; simpan agent.key (dari NewAgentKeyAndCSR), agent.crt, ca.crt, agent.yaml
type Agent struct{ /* cfg, reg, dial func, logger */ }
func New(cfg config.Config, reg *commands.Registry, logger *slog.Logger) *Agent
func (a *Agent) Run(ctx context.Context) error
//   loop: dial wss://{PanelAddr}/agent/ws dengan AgentClientTLS; kirim Event{Type:"hello", Payload:{"agent_version":...}}; baca Request → Dispatch di goroutine → tulis Response (mutex); ping tiap 15 s; putus → backoff 1,2,4,…,30 s; berhenti saat ctx selesai
func (a *Agent) Emit(ev proto.Event) error     // kirim event ke panel (ErrOffline bila tidak terhubung); dipakai rencana 2
func Main(args []string, stdout, stderr io.Writer) int
//   "agent" (jalankan; flag --dir), "agent pair --panel URL --token T [--dir]", "agent version"

// package agentapi (panel)
type Deps struct { Store *store.Store; CA *ca.CA; Hub *hub.Hub; AgentAddr func() string; AgentVersion string; Now func() time.Time; Logger *slog.Logger }
func PairHandler(d Deps) http.Handler
//   POST JSON PairRequest: ConsumePairToken → 403 {"error":"token ditolak"}; IssueAgentCert; MarkPaired(serverID, serial, remoteIP); 200 PairResponse. Non-POST → 405. Body > 64 KiB → 413.
func WSHandler(d Deps) http.Handler
//   r.TLS.PeerCertificates[0] → ServerIDFromCert; GetServer gagal atau CertSerial != serial cert → 403 sebelum upgrade; upgrade; NewWSConn; Hub.Attach; menunggu Wait(); Hub.Detach
func ListenAndServeAgents(ctx context.Context, addr string, tlsCfg *tls.Config, h http.Handler) error   // http.Server TLS, ReadHeaderTimeout 10 s, shutdown saat ctx selesai
```

- [ ] **Step 1: Test config**: `TestSaveLoadRoundTrip` (mode file 0600),
  `TestLoadMissingReturnsErrNotPaired`.

- [ ] **Step 2: Test integrasi** di `agentapi_test.go`, semuanya di
  127.0.0.1 port acak, dir temp, store temp, `fakeListener` merekam
  online/offline/event:
  - `TestPairThenConnectThenProbe`: CreateServer + CreatePairToken →
    `agent.Pair` → `MarkPaired` tercatat dengan serial → jalankan
    `Agent.Run` dengan registry yang punya "probe" stub → tunggu OnOnline
    ≤ 5 s → `Hub.Call(id,"probe")` mengembalikan stub → event "hello"
    diterima.
  - `TestPairRejectsUsedToken`: pair kedua dengan token sama → error,
    HTTP 403.
  - `TestWSRejectsDeletedServer`: setelah pair, `DeleteServer` → Run →
    tidak pernah OnOnline dalam 2 s, agent tetap mencoba (tidak panic).
  - `TestWSRejectsStaleCert`: pair dua kali (token baru) → cert pertama
    ditolak, cert kedua diterima.
  - `TestAgentReconnects`: setelah online, tutup koneksi dari sisi panel
    (`Hub` Detach + Close) → OnOffline → agent online lagi ≤ 5 s
    (backoff pertama 1 s).

- [ ] **Step 3: Jalankan, pastikan gagal.**

- [ ] **Step 4: Implementasi** semua file. `go get gopkg.in/yaml.v3@v3.0.1`.
  Di `cmd/dbpanel/main.go` arahkan `agent` ke `agent.Main`. Logger
  `slog` teks ke stderr.

- [ ] **Step 5: Jalankan, pastikan lulus** (`go test -race ./internal/agent/...
  ./internal/panel/agentapi/`).

- [ ] **Step 6: Commit** — `git commit -m "feat(agent): runtime, pairing and panel agent endpoints"`

---

### Task 8: Autentikasi panel

**Files:**
- Create: `internal/panel/auth/password.go`, `internal/panel/auth/session.go`,
  `internal/panel/auth/ratelimit.go`, `internal/panel/auth/middleware.go`
- Test: `internal/panel/auth/auth_test.go`

**Interfaces:**
- Consumes: `store.Store` (users).
- Produces:

```go
func HashPassword(pw string) (string, error)          // bcrypt cost 12
func CheckPassword(hash, pw string) bool
func RandomPassword() string                           // 16 karakter dari [a-zA-Z0-9], crypto/rand
type Sessions struct{ /* map token→session, mutex, ttl */ }
func NewSessions(ttl time.Duration, now func() time.Time) *Sessions
func (s *Sessions) Create(username string) (token string)   // 32 byte hex
func (s *Sessions) Get(token string) (username string, ok bool)   // memperpanjang masa berlaku (sliding)
func (s *Sessions) Delete(token string)
type RateLimiter struct{ /* per key: window, count */ }
func NewRateLimiter(max int, window time.Duration, now func() time.Time) *RateLimiter
func (r *RateLimiter) Allow(key string) bool          // max percobaan per window; key = IP klien
func (r *RateLimiter) Reset(key string)
const CookieName = "dbpanel_session"
func RequireLogin(s *Sessions, loginPath string) func(http.Handler) http.Handler   // tanpa sesi → redirect 303 ke loginPath?next=<path>; set username di context
func UsernameFrom(ctx context.Context) string
func SetSessionCookie(w http.ResponseWriter, token string, secure bool)   // HttpOnly, SameSite=Lax, Path=/
func ClearSessionCookie(w http.ResponseWriter)
```

- [ ] **Step 1: Tulis test**: `TestHashAndCheck`, `TestSessionsExpire`
  (fake now), `TestSessionsSlide`, `TestRateLimiterBlocksAfterMax` (max 5
  per 15 menit: percobaan ke-6 false; setelah window lewat true),
  `TestRequireLoginRedirects` (303 ke `/login?next=/servers`),
  `TestRequireLoginPassesWithCookie`.

- [ ] **Step 2: Jalankan, pastikan gagal.**

- [ ] **Step 3: Implementasi.** `go get golang.org/x/crypto@v0.58.0`.

- [ ] **Step 4: Jalankan, pastikan lulus.**

- [ ] **Step 5: Commit** — `git commit -m "feat(panel): sessions, password hashing, login rate limit"`

---

### Task 9: Aset UI, template, dan halaman panel

**Files:**
- Create: `web/embed.go`, `web/src/app.css`, `web/static/app.css`
  (hasil kompilasi, dikomit), `web/static/vendor/alpine.min.js`,
  `scripts/tailwind.sh`, `scripts/vendor.sh`,
  `web/templates/layout.html`, `web/templates/login.html`,
  `web/templates/dashboard.html`, `web/templates/servers.html`,
  `web/templates/server_new.html`, `web/templates/server_detail.html`,
  `internal/panel/web/render.go`, `internal/panel/web/handlers_auth.go`,
  `internal/panel/web/handlers_servers.go`, `internal/panel/web/sse.go`,
  `internal/panel/web/router.go`
- Test: `internal/panel/web/web_test.go`

**Interfaces:**
- Consumes: store, auth, hub, agentapi.
- Produces:

```go
// package web (embed)
//go:embed templates/* static/*
var FS embed.FS

// package web (internal/panel/web)
type Deps struct {
    Store *store.Store; Hub *hub.Hub; Sessions *auth.Sessions; Limiter *auth.RateLimiter
    PublicURL func() string      // mis. "http://192.168.1.10:8080", dipakai di perintah pairing
    Events *Broker; Now func() time.Time; Version string; Logger *slog.Logger
}
func NewRouter(d Deps) http.Handler
type Renderer struct{ /* template set dari web.FS */ }
func NewRenderer() (*Renderer, error)
func (r *Renderer) Render(w http.ResponseWriter, name string, data any)   // name = "login", "dashboard", ... ; layout + page
// sse.go
type Broker struct{ /* subscribers */ }
func NewBroker() *Broker
func (b *Broker) Publish(event string, data any)       // data di-marshal JSON
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request)   // "text/event-stream", kirim "event: <event>\ndata: <json>\n\n", ping tiap 20 s
```

Rute (semua kecuali login/logout/SSE di balik `RequireLogin`):
- `GET /login` form; `POST /login` (`username`, `password`): limiter key
  IP; gagal → render ulang dengan pesan "Nama pengguna atau kata sandi
  salah" (HTTP 200); terblokir → "Terlalu banyak percobaan, coba lagi
  dalam 15 menit" (HTTP 429); sukses → cookie, redirect `next` (hanya
  path lokal yang diawali `/`) atau `/`.
- `POST /logout` → hapus sesi, redirect `/login`.
- `GET /` dashboard: jumlah server, online, belum dipasangkan.
- `GET /servers` tabel: nama, alamat, status (Online/Offline/Belum
  dipasangkan), versi MySQL, OS, terakhir terlihat.
- `GET /servers/new` form nama; `POST /servers` → CreateServer +
  CreatePairToken(24 jam) → redirect `/servers/{id}`.
- `GET /servers/{id}`: bila belum paired → blok perintah
  `curl -sSL {PublicURL}/agent/install?token={token} | sudo bash` dengan
  tombol salin dan tombol "Buat token baru" (`POST /servers/{id}/token`);
  bila paired → kartu info probe (OS, arsitektur, CPU, RAM, disk, MySQL)
  dan badge online/offline yang diperbarui lewat SSE.
- `POST /servers/{id}/delete` → konfirmasi di modal daisyUI, hapus,
  redirect `/servers`.
- `GET /events` SSE: event `server_status` `{"id":..,"online":bool}`,
  `server_probe` `{"id":..}`.
- `GET /static/*` dari embed (Cache-Control 1 jam).

Teks UI: semua Bahasa Indonesia. Layout: navbar daisyUI dengan menu
Dashboard, Server, Keluar; tema `data-theme="corporate"`.

`scripts/tailwind.sh`: unduh ke `.tools/` (bila belum ada) Tailwind
standalone CLI (`https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION:-v4.1.14}/tailwindcss-linux-x64`)
dan plugin daisyUI (`https://github.com/saadeghi/daisyui/releases/download/${DAISYUI_VERSION:-v5.1.29}/daisyui.js` dan `daisyui-theme.js`),
lalu `tailwindcss -i web/src/app.css -o web/static/app.css --minify`.
`web/src/app.css`:
```css
@import "tailwindcss";
@source "../templates";
@plugin "../../.tools/daisyui.js";
```
`scripts/vendor.sh`: unduh Alpine `https://cdn.jsdelivr.net/npm/alpinejs@3.14.9/dist/cdn.min.js` ke `web/static/vendor/alpine.min.js`.
Bila versi di atas tidak ada lagi, pakai rilis stabil terbaru dan catat di skrip.

- [ ] **Step 1: Jalankan `scripts/vendor.sh` dan `scripts/tailwind.sh`**,
  pastikan `web/static/app.css` dan `alpine.min.js` ada dan dikomit.

- [ ] **Step 2: Tulis test** (`httptest`, store temp, hub dengan fake
  listener): `TestLoginPageRenders` (200, mengandung "Masuk"),
  `TestLoginWrongPassword` (200, pesan salah, tanpa cookie),
  `TestLoginRateLimited` (6 kali salah → 429), `TestLoginSuccessSetsCookie`
  (303 ke `/`, cookie HttpOnly), `TestLoginRejectsExternalNext`
  (`next=http://evil` → redirect `/`), `TestServersRequireLogin` (303),
  `TestCreateServerShowsPairingCommand` (POST → 303 → GET detail
  mengandung `/agent/install?token=`), `TestServerDetailPairedShowsProbe`
  (store dengan probe JSON → mengandung versi MySQL), `TestSSEStreamsStatus`
  (Publish → body mengandung `event: server_status`),
  `TestDeleteServer`.

- [ ] **Step 3: Jalankan, pastikan gagal.**

- [ ] **Step 4: Implementasi** template dan handler. Template `layout.html`
  mendefinisikan `{{define "layout"}}` dengan blok `title` dan `content`;
  tiap halaman `{{define "content"}}`. Renderer meng-clone set per halaman.

- [ ] **Step 5: Jalankan, pastikan lulus.**

- [ ] **Step 6: Commit** — `git add web scripts internal/panel/web && git commit -m "feat(panel): web UI shell, login, servers pages"`

---

### Task 10: Perakitan panel: perintah `panel`, agent tertanam, admin awal

**Files:**
- Create: `internal/panel/panel.go`, `internal/panel/cli.go`,
  `internal/panel/localagent.go`, `internal/panel/listener_impl.go`
- Modify: `cmd/dbpanel/main.go` (subperintah `panel` → `panel.Main`)
- Test: `internal/panel/panel_test.go`

**Interfaces:**
- Consumes: semua task sebelumnya.
- Produces:

```go
type Options struct {
    DataDir string      // default /var/lib/dbpanel  (panel.db, bin/)
    ConfigDir string    // default /etc/dbpanel      (ca.crt, ca.key, admin-initial-password)
    HTTPAddr string     // default ":8080"
    AgentAddr string    // default ":8443"
    PublicHost string   // IP/host yang dipakai agent & perintah pairing; default: IP non-loopback pertama (net.Interfaces)
    Logger *slog.Logger; Version string
}
type Panel struct{ /* Store, CA, Hub, Sessions, Broker, http servers */ }
func New(o Options) (*Panel, error)
//   Open store; LoadOrCreate CA; bila CountUsers()==0: CreateUser("admin", hash(RandomPassword())) dan tulis password ke ConfigDir/admin-initial-password (0600) + log "Kata sandi admin awal ditulis ke ..."; buat/ambil server lokal: settings "local_server_id" → bila kosong CreateServer(hostname) + MarkPaired(id,"local","127.0.0.1") lalu simpan id
func (p *Panel) Run(ctx context.Context) error
//   jalankan: HTTP UI (web.NewRouter + agentapi.PairHandler di /agent/pair + GET /agent/install + GET /agent/bin/{arch} — handler install/bin diisi Task 11, di task ini cukup 404), listener agent mTLS (agentapi.WSHandler), LocalConn Attach untuk server lokal, loop probe: setiap server online di-"probe" saat OnOnline dan tiap 60 s → UpdateProbe + Broker.Publish("server_probe"); OnOnline/OnOffline → TouchSeen + Publish("server_status")
func Main(args []string, stdout, stderr io.Writer) int
//   "panel" (flag --data-dir --config-dir --http --agent-listen --public-host), "panel reset-password [--username admin]" (cetak password baru)
```

- [ ] **Step 1: Tulis test**: `TestNewCreatesAdminAndLocalServer` (dir temp
  → file admin-initial-password ada, 16 karakter; ListServers punya 1
  server paired dengan CertSerial "local"), `TestNewIsIdempotent` (New
  dua kali → tetap 1 user, 1 server), `TestRunServesLoginAndProbesLocal`
  (Run di port acak → GET /login 200; dalam ≤ 5 s server lokal punya
  ProbeJSON tidak kosong dan `Hostname` sama dengan `os.Hostname()`),
  `TestResetPassword` (Main reset-password → login dengan password baru
  berhasil lewat CheckPassword).

- [ ] **Step 2: Jalankan, pastikan gagal.**

- [ ] **Step 3: Implementasi.** `ListenerImpl` menggabungkan hub.Listener →
  store + broker + pemicu probe.

- [ ] **Step 4: Jalankan, pastikan lulus.** Lalu uji manual: `make build &&
  ./dist/dbpanel panel --data-dir /tmp/dbp/data --config-dir /tmp/dbp/etc`
  → buka `http://127.0.0.1:8080`, login admin, lihat server lokal online
  dengan info probe.

- [ ] **Step 5: Commit** — `git commit -m "feat(panel): panel command, embedded local agent, initial admin"`

---

### Task 11: Skrip instalasi panel dan agent

**Files:**
- Create: `scripts/install.sh`, `scripts/systemd/dbpanel-panel.service`,
  `scripts/systemd/dbpanel-agent.service`,
  `web/templates/agent-install.sh.tmpl`,
  `internal/panel/agentapi/install.go`, `README.md`
- Modify: `internal/panel/panel.go` (daftarkan `/agent/install`,
  `/agent/bin/{arch}`)
- Test: `internal/panel/agentapi/install_test.go`, `scripts/install_test.sh`
  (bash, memakai `bash -n` dan, bila ada, `shellcheck`)

**Interfaces:**
- Produces:

```go
// package agentapi
func InstallScriptHandler(d Deps, publicURL func() string) http.Handler
//   GET /agent/install?token=T: PeekPairToken (tidak consume); invalid → 403 teks "token tidak valid"; render agent-install.sh.tmpl dengan {PanelURL, Token, AgentAddr} sebagai text/x-shellscript
func BinaryHandler(d Deps, binDir string) http.Handler
//   GET /agent/bin/{arch}?token=T: PeekPairToken; arch ∈ {amd64, arm64}; cari binDir/dbpanel_linux_{arch}; bila tidak ada dan arch == runtime.GOARCH → os.Executable(); tidak ada → 404 "binary untuk arsitektur ini belum tersedia di panel"
```

`agent-install.sh.tmpl` (bash, `set -euo pipefail`): wajib root
("Jalankan dengan sudo"), deteksi arch (`uname -m` → amd64/arm64), unduh
`{{.PanelURL}}/agent/bin/<arch>?token={{.Token}}` ke
`/usr/local/bin/dbpanel.new` lalu `mv` atomik, tulis
`/etc/systemd/system/dbpanel-agent.service` (ExecStart
`/usr/local/bin/dbpanel agent`, `Restart=always`, `RestartSec=3`,
`User=root`), `dbpanel agent pair --panel {{.PanelURL}} --token {{.Token}}`,
`systemctl daemon-reload && systemctl enable --now dbpanel-agent`, cetak
"Agent terpasang dan terhubung ke panel". Bila `pair` gagal: cetak pesan
dan keluar 1 tanpa mengaktifkan service.

`scripts/install.sh` (panel): wajib root; arch; sumber binary:
`DBPANEL_BIN` (file lokal) atau unduh
`https://github.com/husmin-fajrin/db-panel/releases/latest/download/dbpanel_linux_<arch>`
(`DBPANEL_VERSION` untuk memilih rilis); pasang ke `/usr/local/bin/dbpanel`;
buat `/etc/dbpanel` (0700) dan `/var/lib/dbpanel`; tulis unit
`dbpanel-panel.service` (ExecStart `/usr/local/bin/dbpanel panel`,
`Restart=always`); `systemctl enable --now dbpanel-panel`; tunggu ≤ 15 s
sampai `/etc/dbpanel/admin-initial-password` ada; cetak:
```
Panel terpasang.
Alamat : http://<ip>:8080
Pengguna: admin
Sandi   : <isi file>
```
dengan `<ip>` = `hostname -I | awk '{print $1}'`.

- [ ] **Step 1: Test Go**: `TestInstallScriptRendersToken` (200, body
  mengandung token dan `systemctl enable --now dbpanel-agent`, header
  `text/x-shellscript`), `TestInstallScriptRejectsBadToken` (403),
  `TestBinaryHandlerServesSelfForSameArch` (binDir kosong, arch =
  runtime.GOARCH → 200 dan `Content-Length` > 0; saat test, `os.Executable()`
  adalah binary test itu sendiri dan itu cukup), `TestBinaryHandlerServesFromBinDir`
  (file `dbpanel_linux_arm64` di binDir → 200 dengan isi file),
  `TestBinaryHandlerUnknownArch` (404), `TestBinaryHandlerRejectsBadToken` (403).

- [ ] **Step 2: Test shell**: `scripts/install_test.sh` menjalankan `bash -n`
  pada `scripts/install.sh` dan pada hasil render template (lewat `go run`
  helper kecil atau dengan mengganti `{{...}}` memakai `sed`), dan
  `shellcheck` bila terpasang. Tambahkan ke `Makefile` target `test`.

- [ ] **Step 3: Jalankan, pastikan gagal.**

- [ ] **Step 4: Implementasi** handler, template, skrip, README (cara pasang
  panel, cara tambah server, cara build dari sumber, cara ubah tampilan
  dengan `scripts/tailwind.sh`).

- [ ] **Step 5: Jalankan, pastikan lulus.** Uji manual end-to-end di dua
  kontainer/VM Linux bila tersedia: pasang panel dengan
  `DBPANEL_BIN=dist/dbpanel`, tambah server, jalankan perintah pairing di
  mesin kedua, lihat online + probe di panel. Catat hasilnya di PR.

- [ ] **Step 6: Commit** — `git commit -m "feat: one-line installers for panel and agent"`

---

### Task 12: Build lintas arsitektur dan direktori binary panel

**Files:**
- Modify: `Makefile` (target `release`: build `dist/dbpanel_linux_amd64`
  dan `dist/dbpanel_linux_arm64`), `scripts/install.sh` (salin kedua
  binary dari `DBPANEL_BIN_DIR` bila diberikan ke `/var/lib/dbpanel/bin/`)
- Test: `make release` menghasilkan dua file; `file dist/dbpanel_linux_arm64`
  menyebut "ARM aarch64".

- [ ] **Step 1: Tambahkan target `release`** dengan `GOOS=linux GOARCH=...
  CGO_ENABLED=0` dan ldflags versi.

- [ ] **Step 2: Jalankan `make release`**, verifikasi dua binary.

- [ ] **Step 3: Ubah `install.sh`** agar bila `DBPANEL_BIN_DIR` diberikan,
  menyalin `dbpanel_linux_*` ke `/var/lib/dbpanel/bin/` sehingga
  `BinaryHandler` bisa melayani arsitektur lain tanpa internet. Uji
  `bash -n`.

- [ ] **Step 4: Commit** — `git commit -m "build: cross-arch release binaries"`

---

## Catatan untuk pelaksana

- Rencana ini sengaja berhenti sebelum MySQL. Rencana 2 akan menambah
  driver `mysql` di agent (`internal/agent/drivers/mysql`), job engine,
  dan alur Buat replika, memakai `commands.Registry`, `Agent.Emit`, dan
  `hub.Call` dari rencana ini tanpa mengubah antarmukanya.
- Pesan error yang dilihat user harus berbahasa Indonesia; error internal
  (log) boleh Inggris.
