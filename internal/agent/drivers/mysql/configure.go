package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	// restartReadyTimeout bounds the wait for MySQL after a restart.
	restartReadyTimeout = 120 * time.Second
	// binlogExpireSeconds keeps binary logs for seven days.
	binlogExpireSeconds = 604800
	// errUDFExists is ER_UDF_EXISTS, INSTALL PLUGIN's answer for an
	// installed plugin.
	errUDFExists = 1125
)

// gtidSteps are the gtid_mode values in the order MySQL allows them to be
// raised online, one step at a time.
var gtidSteps = []string{"OFF", "OFF_PERMISSIVE", "ON_PERMISSIVE", "ON"}

// optionNameRe is a server variable name read from the managed block.
var optionNameRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

func (d *Driver) registerConfigure(reg *commands.Registry) {
	reg.Register("mysql.configure", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.ConfigureParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if p.Role = strings.TrimSpace(p.Role); p.Role != roleMaster && p.Role != roleReplica {
			return nil, &commands.ParamError{Msg: "peran harus master atau replica"}
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		res, err := Configure(ctx, h, p)
		if err != nil {
			return nil, err
		}
		if d.Logger != nil {
			d.Logger.Info("mysql configured", "role", p.Role, "server_id", res.ServerID, "config", res.ConfigPath,
				"applied", res.Applied, "needs_restart", res.NeedsRestart)
		}
		return res, nil
	})

	reg.Register("mysql.restart", func(ctx context.Context, _ json.RawMessage) (any, error) {
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		res, err := Restart(ctx, h)
		if err != nil {
			return nil, err
		}
		if d.Logger != nil {
			d.Logger.Info("mysql restarted", "took_ms", res.TookMs)
		}
		return res, nil
	})

	reg.Register("mysql.setReadOnly", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.ReadOnlyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		if err := SetReadOnly(ctx, h, p.On); err != nil {
			return nil, err
		}
		if d.Logger != nil {
			d.Logger.Info("mysql read-only set", "on", p.On)
		}
		return nil, nil
	})
}

// Configure prepares the server for p.Role: it writes the managed config
// block (validated by mysqld, rolled back otherwise), then makes what can
// change online take effect now with SET PERSIST.
func Configure(ctx context.Context, h Host, p proto.ConfigureParams) (proto.ConfigureResult, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return proto.ConfigureResult{}, err
	}
	defer db.Close()
	st, err := Status(ctx, db)
	if err != nil {
		return proto.ConfigureResult{}, err
	}

	res := proto.ConfigureResult{
		Applied:        []string{},
		RestartReasons: restartReasons(p.Role, st),
		ServerID:       chooseServerID(p.ServerID, st.ServerID),
	}
	res.NeedsRestart = len(res.RestartReasons) > 0
	// A master bound to loopback only must listen beyond it; once the block
	// has made it do so, keeping the line keeps it that way.
	prev, err := managedOptions(h.Layout())
	if err != nil {
		return res, err
	}
	bindAll := p.Role == roleMaster && (isLocalBind(st.BindAddress) || prev["bind_address"] == "0.0.0.0")
	bl, err := binlogSettings(ctx, db, st.LogBin, prev)
	if err != nil {
		return res, err
	}
	if res.ConfigPath, err = writeValidConfig(ctx, h, renderConfig(p.Role, res.ServerID, bindAll, bl)); err != nil {
		return res, err
	}

	// Values are constants and integers, never text from the request.
	persist := func(name, value string) error {
		if _, err := db.ExecContext(ctx, "SET PERSIST "+name+" = "+value); err != nil {
			return fmt.Errorf("SET PERSIST %s = %s: %w", name, value, err)
		}
		res.Applied = append(res.Applied, name+"="+value)
		return nil
	}
	if st.ServerID != res.ServerID {
		if err := persist("server_id", strconv.FormatUint(uint64(res.ServerID), 10)); err != nil {
			return res, err
		}
	}
	if !strings.EqualFold(st.BinlogFormat, "ROW") {
		if err := persist("binlog_format", "ROW"); err != nil {
			return res, err
		}
	}
	if p.GTID {
		if !st.EnforceGTID {
			if err := persist("enforce_gtid_consistency", "ON"); err != nil {
				return res, err
			}
		}
		if err := raiseGTIDMode(ctx, db, st.GTIDMode, persist); err != nil {
			return res, err
		}
	}
	if p.EnableClone {
		installed, err := installClonePlugin(ctx, db, func(string, string, ...any) {})
		if err != nil {
			return res, err
		}
		if installed {
			res.Applied = append(res.Applied, "plugin clone")
		}
	}
	return res, nil
}

// chooseServerID is the server_id to use: the requested one, else the
// current one unless it is 0 or the default 1, else a new random one.
func chooseServerID(requested, current uint32) uint32 {
	switch {
	case requested != 0:
		return requested
	case current > 1:
		return current
	}
	for {
		if id := newServerID(); id != current {
			return id
		}
	}
}

// restartReasons says why the config written for role needs a restart, in
// words for the UI; empty when everything takes effect online.
func restartReasons(role string, st proto.MySQLStatus) []string {
	reasons := []string{}
	if role == roleReplica {
		// read_only, super_read_only, relay_log, log_replica_updates and
		// log_bin come from the config block at startup.
		return append(reasons, "pengaturan replika (read_only, super_read_only, relay_log, log_replica_updates, log_bin) berlaku setelah restart")
	}
	if !st.LogBin {
		reasons = append(reasons, "log_bin masih OFF; butuh restart")
	}
	if isLocalBind(st.BindAddress) {
		reasons = append(reasons, fmt.Sprintf("bind-address hanya localhost (%s); replika tidak bisa terhubung sebelum restart", st.BindAddress))
	}
	return reasons
}

// binlogBlock is what the managed block says about the binary log.
type binlogBlock struct {
	// LogBin is the log_bin value: "binlog" for a server whose binary log
	// is off, else its current base name, so the next restart neither
	// renames the logs (stranding the old files and breaking replicas that
	// still need them) nor turns them off.
	LogBin string
	// ExpireSeconds is binlog_expire_logs_seconds, "" to leave retention as
	// the server has it. Only a server that never expires its logs gets
	// dbpanel's seven days; a value the block wrote before is kept.
	ExpireSeconds string
}

// binlogBaseRe is a binary log base name dbpanel writes back into the
// config: an absolute or relative path without spaces, quotes or #.
var binlogBaseRe = regexp.MustCompile(`^[A-Za-z0-9_./+-]{1,512}$`)

// binlogSettings reads the binary log's base name and retention and decides
// the block's lines; prev is the managed block written before.
func binlogSettings(ctx context.Context, db *sql.DB, logBin bool, prev map[string]string) (binlogBlock, error) {
	b := binlogBlock{LogBin: "binlog"}
	if logBin {
		var base sql.NullString
		if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.log_bin_basename").Scan(&base); err != nil {
			return b, fmt.Errorf("baca log_bin_basename: %w", err)
		}
		if !binlogBaseRe.MatchString(base.String) {
			return b, fmt.Errorf("nama berkas binary log %q tidak didukung panel; ubah log_bin di konfigurasi MySQL ke nama tanpa spasi atau tanda kutip", base.String)
		}
		b.LogBin = base.String
	}
	vars, err := nameValues(ctx, db, `SHOW GLOBAL VARIABLES WHERE Variable_name IN ('binlog_expire_logs_seconds', 'expire_logs_days')`)
	if err != nil {
		return b, fmt.Errorf("baca variabel MySQL: %w", err)
	}
	switch v := prev["binlog_expire_logs_seconds"]; {
	case v != "" && isDigits(v):
		b.ExpireSeconds = v
	case (vars["binlog_expire_logs_seconds"] == "0" || vars["binlog_expire_logs_seconds"] == "") &&
		(vars["expire_logs_days"] == "0" || vars["expire_logs_days"] == ""):
		b.ExpireSeconds = strconv.Itoa(binlogExpireSeconds)
	}
	return b, nil
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// renderConfig is the managed [mysqld] block for role. bindAll makes a
// master listen on every address (its bind-address is loopback only).
func renderConfig(role string, serverID uint32, bindAll bool, bl binlogBlock) string {
	lines := []string{"[mysqld]", "log_bin=" + bl.LogBin}
	if bl.ExpireSeconds != "" {
		lines = append(lines, "binlog_expire_logs_seconds="+bl.ExpireSeconds)
	}
	lines = append(lines,
		"gtid_mode=ON",
		"enforce_gtid_consistency=ON",
		"server_id="+strconv.FormatUint(uint64(serverID), 10),
		"binlog_format=ROW",
	)
	if bindAll {
		lines = append(lines, "bind_address=0.0.0.0")
	}
	if role == roleReplica {
		lines = append(lines, "read_only=ON", "super_read_only=ON", "relay_log=relay", "log_replica_updates=ON")
	}
	return strings.Join(lines, "\n") + "\n"
}

// writeValidConfig writes content with Host.WriteConfig and has mysqld
// validate the result. When validation fails the file is put back as it
// was, so MySQL never starts from a config it refuses.
func writeValidConfig(ctx context.Context, h Host, content string) (string, error) {
	path, _, err := managedConfigPath(h.Layout())
	if err != nil {
		return "", err
	}
	old, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return "", fmt.Errorf("baca %s: %w", path, readErr)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	written, err := h.WriteConfig(content)
	if err != nil {
		return "", err
	}
	if verr := h.ValidateConfig(ctx); verr != nil {
		var rerr error
		if readErr == nil {
			rerr = writeFileAtomic(written, old, mode)
		} else {
			rerr = os.Remove(written)
		}
		if rerr != nil {
			return "", fmt.Errorf("%w; mengembalikan %s juga gagal: %v", verr, written, rerr)
		}
		return "", verr
	}
	return written, nil
}

// raiseGTIDMode steps gtid_mode from current up to ON, one value at a time.
// Before the last step it waits, as long as ctx allows, for transactions
// without a GTID to finish.
func raiseGTIDMode(ctx context.Context, db *sql.DB, current string, persist func(name, value string) error) error {
	from := -1
	for i, m := range gtidSteps {
		if strings.EqualFold(current, m) {
			from = i
		}
	}
	if from < 0 {
		return fmt.Errorf("gtid_mode %q tidak dikenal", current)
	}
	for _, mode := range gtidSteps[from+1:] {
		if mode == "ON" {
			if err := waitAnonymousDone(ctx, db); err != nil {
				return err
			}
		}
		if err := persist("gtid_mode", mode); err != nil {
			return err
		}
	}
	return nil
}

// waitAnonymousDone polls until no transaction without a GTID is running.
func waitAnonymousDone(ctx context.Context, db *sql.DB) error {
	for {
		vals, err := nameValues(ctx, db, `SHOW GLOBAL STATUS WHERE Variable_name = 'Ongoing_anonymous_transaction_count'`)
		if err != nil {
			return fmt.Errorf("baca transaksi anonim: %w", err)
		}
		n := vals["Ongoing_anonymous_transaction_count"]
		if n == "0" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("masih ada %s transaksi tanpa GTID yang berjalan; gtid_mode belum bisa ON: %w", n, ctx.Err())
		case <-time.After(ReadyPollInterval):
		}
	}
}

// installClonePlugin installs the clone plugin unless it is active and
// reports whether it did. A plugin that is installed but not ACTIVE (for
// example disabled with clone=OFF) is an error: CLONE would not work.
// INSTALL PLUGIN writes mysql.plugin, which super_read_only refuses (a
// target an earlier attempt left read-only): read-only is lifted around it
// and put back.
func installClonePlugin(ctx context.Context, db *sql.DB, logf func(level, format string, args ...any)) (bool, error) {
	status, err := clonePluginStatus(ctx, db)
	if err != nil || status == "ACTIVE" {
		return false, err
	}
	restore, err := liftReadOnly(ctx, logf, db)
	if err != nil {
		return false, err
	}
	defer restore()
	installed := true
	if _, err := db.ExecContext(ctx, "INSTALL PLUGIN clone SONAME 'mysql_clone.so'"); err != nil {
		var me *drv.MySQLError
		if !errors.As(err, &me) || me.Number != errUDFExists {
			return false, fmt.Errorf("pasang plugin clone: %w", err)
		}
		installed = false
	}
	if status, err = clonePluginStatus(ctx, db); err != nil {
		return false, err
	}
	if status != "ACTIVE" {
		if status == "" {
			status = "tidak ada"
		}
		return false, fmt.Errorf("plugin clone terpasang tetapi tidak aktif (status %s); periksa opsi clone di konfigurasi MySQL", status)
	}
	return installed, nil
}

// clonePluginStatus is the clone plugin's PLUGIN_STATUS; "" when it is not
// installed.
func clonePluginStatus(ctx context.Context, db *sql.DB) (string, error) {
	var status string
	err := db.QueryRowContext(ctx, `SELECT PLUGIN_STATUS FROM information_schema.PLUGINS WHERE PLUGIN_NAME = 'clone'`).Scan(&status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("baca status plugin clone: %w", err)
	}
	return status, nil
}

// Restart restarts MySQL after checking its config, waits until it accepts
// connections, and fails when a value the managed block sets is not in
// effect (a persisted value or a later option file overrides it).
func Restart(ctx context.Context, h Host) (proto.RestartResult, error) {
	want, err := managedOptions(h.Layout())
	if err != nil {
		return proto.RestartResult{}, err
	}
	if err := h.ValidateConfig(ctx); err != nil {
		return proto.RestartResult{}, err
	}
	start := time.Now()
	if err := h.Service().Restart(ctx); err != nil {
		return proto.RestartResult{}, fmt.Errorf("restart MySQL: %w", err)
	}
	if err := h.Service().WaitReady(ctx, restartReadyTimeout); err != nil {
		return proto.RestartResult{}, err
	}
	res := proto.RestartResult{TookMs: time.Since(start).Milliseconds()}
	if len(want) == 0 {
		return res, nil
	}

	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	names := make([]string, 0, len(want))
	for k := range want {
		if optionNameRe.MatchString(k) {
			names = append(names, quoteString(k))
		}
	}
	if len(names) == 0 {
		return res, nil
	}
	have, err := nameValues(ctx, db, "SHOW GLOBAL VARIABLES WHERE Variable_name IN ("+strings.Join(names, ", ")+")")
	if err != nil {
		return res, fmt.Errorf("baca variabel MySQL: %w", err)
	}
	if bad := notInEffect(want, have); len(bad) > 0 {
		return res, fmt.Errorf("MySQL sudah direstart tetapi pengaturan dbpanel belum berlaku: %s; periksa nilai yang di-PERSIST (mysqld-auto.cnf) atau file konfigurasi yang dibaca sesudahnya",
			strings.Join(bad, "; "))
	}
	return res, nil
}

// managedOptions reads the options in the managed config block (or file);
// none when it was never written.
func managedOptions(l Layout) (map[string]string, error) {
	path, block, err := managedConfigPath(l)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("baca %s: %w", path, err)
	}
	content := string(b)
	if block {
		var in []string
		inside := false
		for _, ln := range strings.Split(content, "\n") {
			switch strings.TrimSpace(ln) {
			case blockBegin:
				inside = true
				continue
			case blockEnd:
				inside = false
			}
			if inside {
				in = append(in, ln)
			}
		}
		content = strings.Join(in, "\n")
	}
	return parseOptions(content), nil
}

// parseOptions maps the options in option-file content to their values,
// with names in server variable form (underscores, lower case). A bare
// option is "ON".
func parseOptions(content string) map[string]string {
	opts := map[string]string{}
	for _, ln := range strings.Split(content, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, ";") || strings.HasPrefix(ln, "[") || strings.HasPrefix(ln, "!") {
			continue
		}
		k, v, ok := strings.Cut(ln, "=")
		if !ok {
			v = "ON"
		}
		k = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(k)), "-", "_")
		opts[k] = strings.TrimSpace(v)
	}
	return opts
}

// notInEffect lists, sorted, the options in want whose server variable in
// have holds another value. Options that are not server variables (such as
// skip_log_bin) are not checked. log_bin is a file name in the config and
// ON in the variable.
func notInEffect(want, have map[string]string) []string {
	var bad []string
	for k, w := range want {
		h, ok := have[k]
		if !ok {
			continue
		}
		if k == "log_bin" {
			w = "ON"
		}
		if !strings.EqualFold(normBool(w), normBool(h)) {
			bad = append(bad, fmt.Sprintf("%s=%s (diminta %s)", k, h, w))
		}
	}
	sort.Strings(bad)
	return bad
}

// normBool spells the boolean forms of a variable value as ON or OFF.
func normBool(v string) string {
	switch strings.ToUpper(strings.TrimSpace(v)) {
	case "ON", "1", "TRUE", "YES":
		return "ON"
	case "OFF", "0", "FALSE", "NO":
		return "OFF"
	}
	return strings.TrimSpace(v)
}

// SetReadOnly turns read_only and super_read_only on or off together and
// persists them. read_only goes on first and comes off last.
//
// Turning it off is refused on a server replicas read from: a master is
// never made read-only by dbpanel, so only a mistake would ask that there.
func SetReadOnly(ctx context.Context, h Host, on bool) error {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return err
	}
	defer db.Close()
	stmts := []string{"SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON"}
	if !on {
		if err := refuseOnSource(ctx, db); err != nil {
			return err
		}
		stmts = []string{"SET PERSIST super_read_only = OFF", "SET PERSIST read_only = OFF"}
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", q, err)
		}
	}
	return nil
}
