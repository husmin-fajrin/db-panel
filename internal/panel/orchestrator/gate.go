package orchestrator

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// The precondition gate (spec 5.7-5.10): before an action changes a
// server, Gate judges a list of rows from what the panel already holds
// (the store, the Monitor's last reports, the hub's connections). It calls
// no agent and writes nothing; the only thing it reaches outside the panel
// is the probe of the master's MySQL port (gate_port.go). A red row holds
// the action (Task 10, GateError); a yellow one is shown and the action
// may go on; nothing passes a red row.

// GateStatus is a row's colour.
type GateStatus int

const (
	GateGreen GateStatus = iota
	GateYellow
	GateRed
)

// GateAction is the action a gate is judged for.
type GateAction string

const (
	GatePromotePlanned   GateAction = "promote_planned"
	GatePromoteEmergency GateAction = "promote_emergency" // rows shown, never refused
	GateCreateReplica    GateAction = "create_replica"
	GateRebuildOldMaster GateAction = "rebuild_old_master"
	GateResync           GateAction = "resync"
	GateAddDatabases     GateAction = "add_databases"
	GateRemoveWithDrop   GateAction = "remove_databases_drop"
	GatePrepareMaster    GateAction = "prepare_master"
)

// GateRow is one precondition as the operator reads it.
type GateRow struct {
	Key    string // agent_version, master_pin, clock, tls, disk, account, perf_schema, backup, approval, master_port, audit_forward, panel_key
	Label  string
	Status GateStatus
	Detail string // "" when green
	Fix    string // "" when green
	// NeedsAck: the backup row with no backup folder set, on a planned
	// promote and a rebuild: the operator ticks that the master is backed
	// up some other way.
	NeedsAck bool
}

// GateInput names the action and its servers.
type GateInput struct {
	Action   GateAction
	MasterID int64 // topology master; for prepare_master the server itself
	TargetID int64 // candidate, target or replica; 0 for prepare_master
	Approval *bool // rebuild only: nil = not asked yet, else the agent's answer
}

// GateResult is the rows of one action, in the order they are shown.
type GateResult struct{ Rows []GateRow }

// Red is the red rows.
func (g GateResult) Red() []GateRow {
	var out []GateRow
	for _, r := range g.Rows {
		if r.Status == GateRed {
			out = append(out, r)
		}
	}
	return out
}

// RedKeys is the keys of the red rows.
func (g GateResult) RedKeys() []string {
	var out []string
	for _, r := range g.Red() {
		out = append(out, r.Key)
	}
	return out
}

// NeedsBackupAck reports whether a row asks for the backup tick.
func (g GateResult) NeedsBackupAck() bool {
	for _, r := range g.Rows {
		if r.NeedsAck {
			return true
		}
	}
	return false
}

// ErrGateRed: a precondition row is red. ErrBackupAck: the backup tick a
// row asks for was not given.
var (
	ErrGateRed   = errors.New("prasyarat belum terpenuhi")
	ErrBackupAck = errors.New("centang cadangan belum diisi")
)

// GateError is returned by Start*, Retry and Approve; Unwrap gives
// ErrGateRed.
type GateError struct{ Rows []GateRow }

// textG0 names the red rows of a refused action.
const textG0 = "prasyarat belum terpenuhi: %s"

func (e *GateError) Error() string {
	var labels []string
	for _, r := range (GateResult{Rows: e.Rows}).Red() {
		labels = append(labels, r.Label)
	}
	return fmt.Sprintf(textG0, strings.Join(labels, ", "))
}

func (e *GateError) Unwrap() error { return ErrGateRed }

// ReplicaUnverified reports whether rep is set up and running (ready or
// attention) while its replication does not verify the master's
// certificate. A replica being created, resynced or failed is set when its
// replication is connected, so it is not counted.
func ReplicaUnverified(rep store.Replica) bool {
	return !rep.TLSVerified && (rep.State == store.ReplicaReady || rep.State == store.ReplicaAttention)
}

// Row keys and labels.
const (
	keyAgentVersion = "agent_version"
	keyMasterPin    = "master_pin"
	keyClock        = "clock"
	keyTLS          = "tls"
	keyDisk         = "disk"
	keyAccount      = "account"
	keyPerfSchema   = "perf_schema"
	keyBackup       = "backup"
	keyApproval     = "approval"
	keyMasterPort   = "master_port"
	keyAuditForward = "audit_forward"
	keyPanelKey     = "panel_key"

	labelAgentVersion = "Versi agent"
	labelMasterPin    = "Pin master"
	labelClock        = "Jam (clock)"
	labelTLS          = "Pemeriksaan sertifikat TLS"
	labelDisk         = "Ruang disk"
	labelAccount      = "Akun panel"
	labelPerfSchema   = "performance_schema"
	labelBackup       = "Cadangan (backup)"
	labelApproval     = "Persetujuan di server"
	labelMasterPort   = "Port MySQL master"
	labelAuditForward = "Log audit"
	labelPanelKey     = "Cadangan panel.key"
)

// Operator texts of plan 5b-2b (G1-G13), word for word. The first %s is
// a server's name unless said otherwise.
const (
	textG1a = "Agent di %s versi %s, panel versi %s."
	textG1b = "Versi agent di %s belum diketahui."
	textG1f = "Perbarui agent di server itu (Pasang ulang agent di halaman server), lalu muat ulang halaman ini."
	textG2a = "Agent master %s tidak melaporkan pin master (yang dilaporkan: %s)."
	textG2f = "Jalankan Siapkan master di halaman server itu. Bila baru saja dijalankan, klik Periksa sekarang di halaman " +
		"server, lalu muat ulang halaman ini."
	textG3a = "Jam server %s berbeda %s dari jam host panel (batas 30 detik)."
	textG3f = "Selaraskan jam server itu dan host panel dengan NTP (di server: timedatectl set-ntp true), klik Periksa " +
		"sekarang di halaman server, lalu muat ulang halaman ini."
	textG4a = "Master %s belum punya sertifikat MySQL dari panel."
	textG4f = "Pasang sertifikat MySQL di halaman server master, lalu jalankan Wajibkan pemeriksaan sertifikat TLS di " +
		"halaman master."
	textG4b = "Replika %s belum memeriksa sertifikat master." // the names, joined with ", "
	textG4g = "Jalankan Wajibkan pemeriksaan sertifikat TLS di halaman master."
	textG5a = "Disk %s terpakai %d%% (ambang kritis %d%%)."
	textG5f = "Kosongkan ruang di server itu, klik Periksa sekarang di halaman server, lalu muat ulang halaman ini."
	textG6a = "Agent di %s masuk ke MySQL sebagai root."
	// textG6f's %s is the label of the button that posts
	// /servers/{id}/mysql/refresh (server_detail.html).
	textG6f = "Buat akun MySQL khusus panel lalu hubungkan lagi (Panduan, \"Memperbaiki baris merah\"). Bila akunnya " +
		"sudah diganti di server, klik %s di kartu MySQL halaman server itu."
	textG7a  = "performance_schema mati di master %s."
	textG7f  = "Hapus performance_schema=OFF dari konfigurasi MySQL master, lalu restart MySQL master di jam sepi."
	textG8f  = "Jalankan cadangan master, atau betulkan folder cadangan di halaman server master."
	textG8u  = "Folder cadangan master %s belum diisi, jadi panel tidak bisa melihat umur cadangan."
	textG9u  = "Persetujuan di server %s belum diperiksa."
	textG9a  = "Belum disetujui di server %s."
	textG9f  = "Jalankan perintah di kotak Persetujuan di server pada halaman ini, lalu klik Periksa lagi."
	textG10a = "%s belum diketahui: agent %s sedang tidak tersambung." // %s: the row's label, then the server
	textG10b = "%s belum diketahui: agent %s belum melaporkannya."     // %s: the row's label, then the server
	// Fixes of two yellow rows (Ruling R28): G8u, and G10a. G10b has none
	// (but see textG10bf).
	textG8uf  = "Isi folder cadangan di halaman server master, atau pastikan sendiri ada cadangan terbaru."
	textG10af = "Pastikan agent di server itu berjalan dan tersambung ke panel, lalu muat ulang halaman ini."
	textG11a  = "Port MySQL master %s bisa dijangkau dari host panel; sebaiknya hanya terbuka untuk replika."
	textG11f  = "Batasi port itu di firewall master hanya untuk alamat replika."
	textG12a  = "Log audit belum ditandai diteruskan ke server lain."
	textG12f  = "Teruskan journal host panel ke server lain (systemd-journal-upload), lalu tandai di Pengaturan."
	textG13a  = "panel.key belum ditandai sudah dicadangkan."
	textG13f  = "Cadangkan panel.key (README, \"Cadangkan kunci panel\"), lalu tandai di Pengaturan."

	// textG11w is T6 (spec 5.1): the address changed within the master's
	// ten-minute probe slot; %s is when the slot ends.
	textG11w = "Alamat master baru saja diubah; port diperiksa lagi paling cepat pukul %s."
)

// textG10bf is the account row's fix while a connected server's
// account is not known yet (Ruling R40): stored before the panel read it
// (an upgrade), it is read on Segarkan. %s is refreshButton.
const textG10bf = "Klik %s di kartu MySQL halaman server itu, lalu muat ulang halaman ini."

// refreshButton is the label of the MySQL card's button that posts
// /servers/{id}/mysql/refresh (server_detail.html).
const refreshButton = "Segarkan"

// maxGateValue bounds an agent's value in a row (a version, a role pin).
const maxGateValue = 32

// maxClockSkew: a clock further than this from the panel host's is red.
const maxClockSkew = 30 * time.Second

// rootUser is the MySQL user the account row refuses.
const rootUser = "root"

// gateRows is the rows of each action, in the order they are shown.
var gateRows = map[GateAction][]string{
	GatePromotePlanned: {keyAgentVersion, keyMasterPin, keyClock, keyTLS, keyAccount, keyBackup, keyMasterPort,
		keyAuditForward, keyPanelKey},
	GatePromoteEmergency: {keyAgentVersion, keyMasterPin, keyClock, keyTLS, keyAccount, keyBackup, keyMasterPort,
		keyAuditForward, keyPanelKey},
	GateCreateReplica: {keyAgentVersion, keyMasterPin, keyDisk, keyAccount, keyPerfSchema},
	GateRebuildOldMaster: {keyAgentVersion, keyTLS, keyDisk, keyAccount, keyPerfSchema, keyBackup, keyApproval,
		keyMasterPort, keyAuditForward, keyPanelKey},
	GateResync:         {keyAgentVersion, keyMasterPin, keyDisk, keyAccount, keyPerfSchema},
	GateAddDatabases:   {keyAgentVersion, keyMasterPin, keyDisk, keyAccount, keyPerfSchema},
	GateRemoveWithDrop: {keyAgentVersion, keyAccount},
	GatePrepareMaster:  {keyAgentVersion, keyAccount},
}

// gateServer is one judged server: its row, the Monitor's view of it and
// whether its agent is connected now.
type gateServer struct {
	srv    store.Server
	view   monitor.View
	online bool
}

// gateCtx is what the rules of one Gate call read.
type gateCtx struct {
	r       *Runner
	in      GateInput
	master  *gateServer   // nil for remove_databases_drop
	servers []*gateServer // judged, the master first
	topo    *store.Topology
}

// Gate judges the preconditions of in.Action from the panel's own data.
// It calls no agent and writes nothing. Its error is for an unknown
// action, server or topology, or a store failure; a red row is not an
// error (GateError is the Start functions').
func (r *Runner) Gate(in GateInput) (GateResult, error) {
	keys, ok := gateRows[in.Action]
	if !ok {
		return GateResult{}, fmt.Errorf("prasyarat: tindakan %q tidak dikenal", in.Action)
	}
	if r.gateFault != nil {
		if err := r.gateFault(in); err != nil {
			return GateResult{}, err
		}
	}
	c := &gateCtx{r: r, in: in}
	if in.Action != GateRemoveWithDrop {
		m, err := r.gateServer(in.MasterID)
		if err != nil {
			return GateResult{}, err
		}
		c.master = m
		c.servers = append(c.servers, m)
	}
	if in.Action != GatePrepareMaster {
		t, err := r.gateServer(in.TargetID)
		if err != nil {
			return GateResult{}, err
		}
		c.servers = append(c.servers, t)
	}
	if in.Action == GatePromotePlanned || in.Action == GatePromoteEmergency || in.Action == GateRebuildOldMaster {
		topo, err := r.d.Store.GetTopologyByMaster(in.MasterID)
		if err != nil {
			return GateResult{}, fmt.Errorf("prasyarat: topologi master #%d: %w", in.MasterID, err)
		}
		c.topo = &topo
	}
	rules := map[string]func() (GateRow, error){
		keyAgentVersion: c.agentVersionRow,
		keyMasterPin:    c.masterPinRow,
		keyClock:        c.clockRow,
		keyTLS:          c.tlsRow,
		keyDisk:         c.diskRow,
		keyAccount:      c.accountRow,
		keyPerfSchema:   c.perfSchemaRow,
		keyBackup:       c.backupRow,
		keyApproval:     c.approvalRow,
		keyMasterPort:   c.masterPortRow,
		keyAuditForward: func() (GateRow, error) {
			return c.markRow(keyAuditForward, labelAuditForward, store.SettingAuditForwardedAt, textG12a, textG12f)
		},
		keyPanelKey: func() (GateRow, error) {
			return c.markRow(keyPanelKey, labelPanelKey, store.SettingPanelKeyBackedUpAt, textG13a, textG13f)
		},
	}
	res := GateResult{Rows: make([]GateRow, 0, len(keys))}
	for _, k := range keys {
		row, err := rules[k]()
		if err != nil {
			return GateResult{}, err
		}
		res.Rows = append(res.Rows, row)
	}
	return res, nil
}

// gateServer reads server id with the Monitor's view of it and whether
// its agent is connected (the hub's map, no call).
func (r *Runner) gateServer(id int64) (*gateServer, error) {
	srv, err := r.d.Store.GetServer(id)
	if err != nil {
		return nil, fmt.Errorf("prasyarat: server #%d: %w", id, err)
	}
	v, _ := r.view(id)
	return &gateServer{srv: srv, view: v, online: r.d.Hub != nil && r.d.Hub.IsOnline(id)}, nil
}

// verdict is one server's sentence in a row ("" when green).
type verdict struct {
	status GateStatus
	text   string
	away   bool // G10a: the server's agent is not connected
}

func red(format string, args ...any) verdict {
	return verdict{status: GateRed, text: fmt.Sprintf(format, args...)}
}
func yellow(format string, args ...any) verdict {
	return verdict{status: GateYellow, text: fmt.Sprintf(format, args...)}
}

// unknown is G10a for a server whose agent is away, else G10b.
func unknown(label string, s *gateServer) verdict {
	if !s.online {
		v := yellow(textG10a, label, s.srv.Name)
		v.away = true
		return v
	}
	return yellow(textG10b, label, s.srv.Name)
}

// combine makes one row of the verdicts of its servers: the worst status,
// the sentences of the red ones and then of the yellow ones joined by a
// space; fix when red, and when yellow the G10a fix if one of its servers'
// agent is away, else none (Ruling R28).
func combine(key, label string, vs []verdict, fix string) GateRow {
	row := GateRow{Key: key, Label: label}
	var texts []string
	away := false
	for _, want := range []GateStatus{GateRed, GateYellow} {
		for _, v := range vs {
			if v.status == want {
				row.Status = max(row.Status, want)
				texts = append(texts, v.text)
				away = away || v.away
			}
		}
	}
	row.Detail = strings.Join(texts, " ")
	switch {
	case row.Status == GateRed:
		row.Fix = fix
	case row.Status == GateYellow && away:
		row.Fix = textG10af
	}
	return row
}

// perServer judges each server with judge and makes one row of it.
func (c *gateCtx) perServer(key, label, fix string, judge func(*gateServer) verdict) GateRow {
	var vs []verdict
	for _, s := range c.servers {
		vs = append(vs, judge(s))
	}
	return combine(key, label, vs, fix)
}

// agentVersionRow: each server's agent at the panel's version (G1); red
// when unknown. A panel without a version skips it.
func (c *gateCtx) agentVersionRow() (GateRow, error) {
	return c.perServer(keyAgentVersion, labelAgentVersion, textG1f, func(s *gateServer) verdict {
		switch v := s.srv.AgentVersion; {
		case c.r.d.Version == "" || v == c.r.d.Version:
			return verdict{}
		case v == "":
			return red(textG1b, s.srv.Name)
		default:
			return red(textG1a, s.srv.Name, agentPart(v, maxGateValue), c.r.d.Version)
		}
	}), nil
}

// masterPinRow: the master's last report carries its role pin "master"
// (G2); red when there is no report.
func (c *gateCtx) masterPinRow() (GateRow, error) {
	m := c.master
	pin := ""
	if m.view.Snap != nil {
		pin = m.view.Snap.RolePin
	}
	if pin == pinMaster {
		return combine(keyMasterPin, labelMasterPin, nil, ""), nil
	}
	shown := agentPart(pin, maxGateValue)
	if shown == "" {
		shown = "tidak ada"
	}
	return combine(keyMasterPin, labelMasterPin, []verdict{red(textG2a, m.srv.Name, shown)}, textG2f), nil
}

// pinMaster is the role pin of a master's agent.
const pinMaster = "master"

// clockRow: each server's clock within 30 seconds of the panel host's
// (G3).
func (c *gateCtx) clockRow() (GateRow, error) {
	return c.perServer(keyClock, labelClock, textG3f, func(s *gateServer) verdict {
		skew, ok := monitor.ClockSkew(s.view)
		switch {
		case !s.online || !ok:
			return unknown(labelClock, s)
		case skew.Abs() > maxClockSkew:
			return red(textG3a, s.srv.Name, clockDiffWords(skew))
		}
		return verdict{}
	}), nil
}

// clockDiffWords is a clock difference in G3a, without its direction
// (Ruling R24): whole seconds rounded up below 2 minutes, whole minutes
// rounded down below 2 hours, then whole hours rounded down.
func clockDiffWords(d time.Duration) string {
	d = d.Abs()
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%d detik", int64(math.Ceil(d.Seconds())))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d menit", int64(d/time.Minute))
	}
	return fmt.Sprintf("%d jam", int64(d/time.Hour))
}

// tlsRow: the master holds the panel's MySQL certificate (G4a) and, on a
// promote, every running replica verifies it (G4b).
func (c *gateCtx) tlsRow() (GateRow, error) {
	var vs []verdict
	fix := textG4g
	if _, err := c.r.d.Store.GetServerTLS(c.master.srv.ID); errors.Is(err, store.ErrNotFound) {
		vs = append(vs, red(textG4a, c.master.srv.Name))
		fix = textG4f
	} else if err != nil {
		return GateRow{}, fmt.Errorf("prasyarat: sertifikat master: %w", err)
	}
	if c.in.Action == GatePromotePlanned || c.in.Action == GatePromoteEmergency {
		reps, err := c.r.d.Store.ListReplicas(c.topo.ID)
		if err != nil {
			return GateRow{}, fmt.Errorf("prasyarat: replika: %w", err)
		}
		var names []string
		for _, rep := range reps {
			if !ReplicaUnverified(rep) {
				continue
			}
			srv, err := c.r.d.Store.GetServer(rep.ServerID)
			if err != nil {
				return GateRow{}, fmt.Errorf("prasyarat: server #%d: %w", rep.ServerID, err)
			}
			names = append(names, srv.Name)
		}
		if len(names) > 0 {
			vs = append(vs, red(textG4b, strings.Join(names, ", ")))
		}
	}
	return combine(keyTLS, labelTLS, vs, fix), nil
}

// diskRow: each server's data directory under the critical threshold
// (G5).
func (c *gateCtx) diskRow() (GateRow, error) {
	set, err := monitor.LoadSettings(c.r.d.Store)
	if err != nil {
		return GateRow{}, fmt.Errorf("prasyarat: pengaturan: %w", err)
	}
	crit := set.Thresholds.DiskCrit
	return c.perServer(keyDisk, labelDisk, textG5f, func(s *gateServer) verdict {
		pct, ok := monitor.DiskPercent(s.view.Snap)
		switch {
		case !s.online || !ok:
			return unknown(labelDisk, s)
		case pct >= crit:
			return red(textG5a, s.srv.Name, pct, crit)
		}
		return verdict{}
	}), nil
}

// accountRow: no agent logs in to MySQL as root (G6). The target of Buat
// replika without a connected inventory (its MySQL not connected yet) is
// not judged; any other server without one is not known yet (G10b, Ruling
// R30), and so is a connected inventory without the account; a yellow row
// with such an inventory gets textG10bf as its fix (Ruling R40).
func (c *gateCtx) accountRow() (GateRow, error) {
	var vs []verdict
	refresh := false // a connected server's account is not known yet
	for _, s := range c.servers {
		skip := c.in.Action == GateCreateReplica && s.srv.ID == c.in.TargetID
		inv, err := c.r.d.Store.GetInventory(s.srv.ID)
		if errors.Is(err, store.ErrNotFound) {
			if !skip {
				vs = append(vs, yellow(textG10b, labelAccount, s.srv.Name))
			}
			continue
		}
		if err != nil {
			return GateRow{}, fmt.Errorf("prasyarat: inventaris %s: %w", s.srv.Name, err)
		}
		switch {
		case !inv.Connected && skip:
		case !inv.Connected:
			vs = append(vs, yellow(textG10b, labelAccount, s.srv.Name))
		case inv.Account == "":
			vs = append(vs, yellow(textG10b, labelAccount, s.srv.Name))
			refresh = true
		case gateAccountUser(inv.Account) == rootUser:
			vs = append(vs, red(textG6a, s.srv.Name))
		}
	}
	row := combine(keyAccount, labelAccount, vs, fmt.Sprintf(textG6f, refreshButton))
	if row.Status == GateYellow && refresh {
		row.Fix = fmt.Sprintf(textG10bf, refreshButton)
	}
	return row, nil
}

// gateAccountUser is the user of a MySQL account user@host: the part
// before the last @ (a user name may hold @, a host may not).
func gateAccountUser(account string) string {
	if i := strings.LastIndex(account, "@"); i >= 0 {
		return account[:i]
	}
	return account
}

// perfSchemaRow: performance_schema on at the master (G7).
func (c *gateCtx) perfSchemaRow() (GateRow, error) {
	m := c.master
	var v verdict
	switch {
	case !m.online || m.view.Snap == nil || m.view.Snap.PerfSchema == nil:
		v = unknown(labelPerfSchema, m)
	case !*m.view.Snap.PerfSchema:
		v = red(textG7a, m.srv.Name)
	}
	return combine(keyPerfSchema, labelPerfSchema, []verdict{v}, textG7f), nil
}

// backupRow: the topology master's backup folder holds a fresh file (G8,
// monitor.JudgeBackup). No folder set is judged first, from the store,
// whether or not the agent is connected (Ruling R26): yellow, with its own
// fix, and it asks for the operator's tick on a planned promote and a
// rebuild. With a folder set and the agent away, the last report is not
// judged (Ruling R25).
func (c *gateCtx) backupRow() (GateRow, error) {
	m := c.master
	if m.srv.BackupDir == "" {
		row := combine(keyBackup, labelBackup, []verdict{yellow(textG8u, m.srv.Name)}, textG8f)
		row.Fix = textG8uf
		row.NeedsAck = c.in.Action == GatePromotePlanned || c.in.Action == GateRebuildOldMaster
		return row, nil
	}
	if !m.online {
		return combine(keyBackup, labelBackup, []verdict{unknown(labelBackup, m)}, textG8f), nil
	}
	b := monitor.JudgeBackup(m.view, m.srv, c.r.now())
	switch b.Status {
	case monitor.BackupStatusOK:
		return combine(keyBackup, labelBackup, nil, ""), nil
	case monitor.BackupStatusStale, monitor.BackupStatusEmptyFile, monitor.BackupStatusNone, monitor.BackupStatusUnreadable:
		maxAge := m.srv.BackupMaxAgeHours
		if maxAge <= 0 {
			maxAge = monitor.DefaultBackupMaxAgeHours
		}
		return combine(keyBackup, labelBackup, []verdict{{status: GateRed, text: monitor.BackupMessage(b, m.srv.Name, maxAge)}}, textG8f), nil
	}
	return combine(keyBackup, labelBackup, []verdict{unknown(labelBackup, m)}, textG8f), nil
}

// approvalRow: root on the old master approved its rebuild (G9).
func (c *gateCtx) approvalRow() (GateRow, error) {
	name := c.servers[len(c.servers)-1].srv.Name
	row := GateRow{Key: keyApproval, Label: labelApproval}
	switch {
	case c.in.Approval == nil:
		row.Status, row.Detail, row.Fix = GateYellow, fmt.Sprintf(textG9u, name), textG9f
	case !*c.in.Approval:
		row.Status, row.Detail, row.Fix = GateRed, fmt.Sprintf(textG9a, name), textG9f
	}
	return row, nil
}

// markRow is a yellow-only row for a settings mark (Pengaturan) not set.
func (c *gateCtx) markRow(key, label, setting, detail, fix string) (GateRow, error) {
	v, err := c.r.d.Store.GetSetting(setting)
	if err != nil {
		return GateRow{}, fmt.Errorf("prasyarat: pengaturan: %w", err)
	}
	row := GateRow{Key: key, Label: label}
	if v == "" {
		row.Status, row.Detail, row.Fix = GateYellow, detail, fix
	}
	return row, nil
}

// masterPortRow (yellow only, never red): the master's MySQL port answers
// the panel host, which is neither the master, the action's target nor one
// of its replicas (G11); or the address changed and waits for the master's
// next probe slot (T6).
func (c *gateCtx) masterPortRow() (GateRow, error) {
	row := GateRow{Key: keyMasterPort, Label: labelMasterPort}
	if c.topo.SourceHost == "" || c.topo.SourcePort == 0 {
		return row, nil
	}
	local := c.r.d.LocalServerID
	if local != 0 {
		// The master, or the action's target (Ruling R27: the old master of
		// a rebuild is about to become a replica).
		if local == c.master.srv.ID || local == c.in.TargetID {
			return row, nil
		}
		reps, err := c.r.d.Store.ListReplicas(c.topo.ID)
		if err != nil {
			return GateRow{}, fmt.Errorf("prasyarat: replika: %w", err)
		}
		for _, rep := range reps {
			if rep.ServerID == local {
				return row, nil
			}
		}
	}
	open, waitUntil := c.r.masterPortOpen(c.master.srv.ID, masterSourceAddr(*c.topo))
	switch {
	case !waitUntil.IsZero():
		// The address changed within the master's slot: not dialled until
		// it ends (spec 5.1). Yellow, and nothing for the user to fix.
		row.Status, row.Detail = GateYellow, fmt.Sprintf(textG11w, waitUntil.Local().Format("15:04"))
	case open:
		row.Status, row.Detail, row.Fix = GateYellow, fmt.Sprintf(textG11a, c.master.srv.Name), textG11f
	}
	return row, nil
}
