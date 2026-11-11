package proto

import "time"

// DatabaseInfo describes one MySQL database found on a server.
type DatabaseInfo struct {
	Name    string   `json:"name"`
	Bytes   uint64   `json:"bytes"`
	Tables  int      `json:"tables"`
	Engines []string `json:"engines,omitempty"`
}

// ConnectParams is the MySQL admin account the agent uses. Port 0 with Host
// "" or "localhost" means the local socket.
type ConnectParams struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

// ConnectResult is the answer to mysql.connect and mysql.inventory.
type ConnectResult struct {
	Status        MySQLStatus    `json:"status"`
	Databases     []DatabaseInfo `json:"databases"`      // without mysql, sys, information_schema, performance_schema
	MissingGrants []string       `json:"missing_grants"` // GRANT statements for what the account lacks; empty = enough
	Account       string         `json:"account"`        // the effective 'user'@'host' (CURRENT_USER())
}

// MySQLStatus is a snapshot of a MySQL server's replication-relevant state.
type MySQLStatus struct {
	Version           string         `json:"version"`
	Series            string         `json:"series"` // "8.0" | "8.4" | "9"; "" when the version is unsupported
	ServerID          uint32         `json:"server_id"`
	ServerUUID        string         `json:"server_uuid"`
	LogBin            bool           `json:"log_bin"`
	GTIDMode          string         `json:"gtid_mode"`
	EnforceGTID       bool           `json:"enforce_gtid"`
	BinlogFormat      string         `json:"binlog_format"`
	ReadOnly          bool           `json:"read_only"`
	SuperReadOnly     bool           `json:"super_read_only"`
	GTIDExecuted      string         `json:"gtid_executed"`
	DataDir           string         `json:"data_dir"`
	Socket            string         `json:"socket"`
	Port              int            `json:"port"`
	BindAddress       string         `json:"bind_address"`
	Replica           *ReplicaStatus `json:"replica,omitempty"` // nil when the server is not a replica
	UptimeSec         int64          `json:"uptime_sec"`
	ThreadsRunning    int            `json:"threads_running"`
	BinlogDumpThreads int            `json:"binlog_dump_threads"` // replicas currently reading this server's binlog
	// TLSAvailable: the server accepts encrypted connections (have_ssl, or
	// tls_version with a loaded certificate where have_ssl is gone).
	TLSAvailable bool `json:"tls_available"`
	// LowerCaseTableNames is @@lower_case_table_names; it is fixed when the
	// data directory is initialised and must match between master and
	// replica.
	LowerCaseTableNames int `json:"lower_case_table_names"`
	// CloneState and CloneError are performance_schema.clone_status of the
	// last CLONE into this server ("" when none ran or the plugin is off).
	CloneState string `json:"clone_state,omitempty"`
	CloneError string `json:"clone_error,omitempty"`
}

// ReplicaStatus is the default channel's row of SHOW REPLICA STATUS.
type ReplicaStatus struct {
	IORunning     string   `json:"io_running"`  // Yes | No | Connecting
	SQLRunning    string   `json:"sql_running"` // Yes | No
	SourceHost    string   `json:"source_host"`
	SourcePort    int      `json:"source_port"`
	LastIOError   string   `json:"last_io_error"`
	LastSQLError  string   `json:"last_sql_error"`
	LastIOErrno   int      `json:"last_io_errno"`
	LastSQLErrno  int      `json:"last_sql_errno"`
	SecondsBehind *int64   `json:"seconds_behind"` // nil when unknown (threads stopped)
	RetrievedGTID string   `json:"retrieved_gtid"`
	ExecutedGTID  string   `json:"executed_gtid"`
	Filters       []string `json:"filters"` // REPLICATE_WILD_DO_TABLE rules, e.g. "db.%"
	// SSLVerifyServerCert: the source is configured with
	// SOURCE_SSL_VERIFY_SERVER_CERT=1, so a connected receiver (IORunning
	// Yes) verified the master's certificate. False from agents before it.
	SSLVerifyServerCert bool `json:"ssl_verify_server_cert,omitempty"`
}

// PreflightParams are the parameters of mysql.preflight.
type PreflightParams struct {
	Role           string   `json:"role"`             // "master" | "replica"
	PeerHost       string   `json:"peer_host"`        // replica: the master's address as seen from this server
	PeerPort       int      `json:"peer_port"`        // replica: the master's MySQL port
	PeerVersion    string   `json:"peer_version"`     // replica: the master's @@version
	PeerServerUUID string   `json:"peer_server_uuid"` // replica: the master's @@server_uuid; the target must differ
	PeerDataBytes  uint64   `json:"peer_data_bytes"`  // replica: size of the master's selected databases
	PeerDatabases  []string `json:"peer_databases"`   // replica: every user database on the master
	ReplUser       string   `json:"repl_user"`        // master: the replication user the panel uses
	Databases      []string `json:"databases"`        // databases to replicate; empty = all
	ConfirmWipe    bool     `json:"confirm_wipe"`     // replica: the user agreed to drop leftover databases
	// PeerLowerCaseTableNames is the master's lower_case_table_names
	// (replica); nil when unknown.
	PeerLowerCaseTableNames *int `json:"peer_lower_case_table_names,omitempty"`
	// Resync (replica) marks the target as a registered replica of this
	// master being synced again. The target's databases named in WipeList,
	// which the user agreed to drop, are then neither leftovers nor foreign,
	// CLONE may be chosen when every target database is on the list, and
	// ReclaimBytes (the replica's current data size) counts as free space
	// in the disk check. Without Resync, WipeList and ReclaimBytes change
	// no check (WipeList's names are still validated).
	Resync       bool     `json:"resync"`
	WipeList     []string `json:"wipe_list"`
	ReclaimBytes uint64   `json:"reclaim_bytes"`
}

// Preflight check statuses, from least to most severe: ok, warn,
// needs_confirm, needs_restart, fail.
const (
	CheckOK           = "ok"
	CheckWarn         = "warn"
	CheckFail         = "fail"
	CheckNeedsRestart = "needs_restart"
	CheckNeedsConfirm = "needs_confirm"
)

// Check is one preflight finding. FixSQL are statements that would fix it
// online; FixConfig is option-file content ([mysqld] lines) that fixes it
// after a restart.
type Check struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Title     string   `json:"title"`
	Detail    string   `json:"detail"`
	FixSQL    []string `json:"fix_sql,omitempty"`
	FixConfig string   `json:"fix_config,omitempty"`
}

// PreflightResult is the answer to mysql.preflight.
type PreflightResult struct {
	Status    MySQLStatus `json:"status"`
	Checks    []Check     `json:"checks"`
	DataBytes uint64      `json:"data_bytes"` // master: size of the selected databases
	Worst     string      `json:"worst"`      // the most severe Check.Status; ok when there are none
	Method    string      `json:"method"`     // replica: "clone" | "dump"; "" for a master
}

// ConfigureParams are the parameters of mysql.configure.
type ConfigureParams struct {
	Role        string `json:"role"`         // "master" | "replica"
	ServerID    uint32 `json:"server_id"`    // 0 = keep a non-default server_id, else pick a random one
	EnableClone bool   `json:"enable_clone"` // install the clone plugin when it is not active
	GTID        bool   `json:"gtid"`         // raise enforce_gtid_consistency and gtid_mode to ON online
}

// ConfigureResult is the answer to mysql.configure.
type ConfigureResult struct {
	Applied        []string `json:"applied"`         // online changes made, e.g. "gtid_mode=ON_PERMISSIVE"
	NeedsRestart   bool     `json:"needs_restart"`   // the written config takes full effect only after a restart
	RestartReasons []string `json:"restart_reasons"` // why, for the UI
	ConfigPath     string   `json:"config_path"`     // the option file written
	ServerID       uint32   `json:"server_id"`       // the server_id in effect and in the config
}

// ReplUserParams are the parameters of mysql.createReplUser. The name
// must start with "dbpanel_".
type ReplUserParams struct {
	User       string `json:"user"`
	Password   string `json:"password"`
	FromHost   string `json:"from_host"`   // host part of the account; "" = "%"
	RequireSSL bool   `json:"require_ssl"` // the account may only connect over TLS
}

// DumpUserParams are the parameters of mysql.createDumpUser: a temporary
// account a replica dumps this master with. The name must start with
// "dbpanel_"; FromHost is the replica's address.
type DumpUserParams struct {
	User       string `json:"user"`
	Password   string `json:"password"`
	FromHost   string `json:"from_host"`
	RequireSSL bool   `json:"require_ssl"`
}

// DumpUserResult is the answer to mysql.createDumpUser. Warning is set
// when the account got RELOAD instead of FLUSH_TABLES because the admin
// account cannot grant FLUSH_TABLES; it names the GRANT that fixes it. An
// agent older than this field answers null.
type DumpUserResult struct {
	Warning string `json:"warning,omitempty"`
}

// DropUserParams are the parameters of mysql.dropUser. The name must start
// with "dbpanel_".
type DropUserParams struct {
	User     string `json:"user"`
	FromHost string `json:"from_host"`
}

// SweepDumpUsersParams are the parameters of mysql.sweepDumpUsers: drop the
// per-job dump accounts (dbpanel_dump_*) whose password is older than
// OlderThanSec seconds (at least 60).
type SweepDumpUsersParams struct {
	OlderThanSec int64 `json:"older_than_sec"`
}

// SweepDumpUsersResult is the answer to mysql.sweepDumpUsers: the accounts
// dropped, as 'user'@'host'.
type SweepDumpUsersResult struct {
	Dropped []string `json:"dropped"`
}

// ReadOnlyParams are the parameters of mysql.setReadOnly.
type ReadOnlyParams struct {
	On bool `json:"on"`
}

// RestartResult is the answer to mysql.restart.
type RestartResult struct {
	TookMs int64 `json:"took_ms"`
}

// SnapshotParams are the parameters of mysql.cloneFrom and mysql.dumpFrom:
// the master to copy from, as seen from this server, and the account to
// copy with.
type SnapshotParams struct {
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	User          string   `json:"user"`
	Password      string   `json:"password"`
	Databases     []string `json:"databases"`      // dump only; empty = every database
	EstimateBytes uint64   `json:"estimate_bytes"` // expected size, for progress
	DropExisting  bool     `json:"drop_existing"`  // dump: drop the local copies of Databases before loading
	SSL           bool     `json:"ssl"`            // the master offers TLS: the copy must be encrypted
	// VerifyIdentity: the master's certificate must chain to the CA file
	// SSLCA (a path on this server, normally <datadir>/dbpanel-tls/mysql-ca.crt)
	// and name Host. It implies SSL. CLONE checks the chain only (see
	// mysql.cloneFrom).
	SSLCA          string `json:"ssl_ca,omitempty"`
	VerifyIdentity bool   `json:"verify_identity,omitempty"`
}

// SnapshotResult is the job.done result of mysql.cloneFrom and mysql.dumpFrom.
type SnapshotResult struct {
	Method string `json:"method"` // "clone" | "dump"
	Bytes  uint64 `json:"bytes"`  // data cloned, or dump bytes loaded
	// GTIDExecuted is the master's GTID set at the snapshot. Clone and a
	// dump in ON mode: this server's @@gtid_executed afterwards (the same
	// set). A dump into a replica that keeps its own history (COMMENTED
	// mode): the set from the dump's header, which this server's
	// gtid_executed does not include yet.
	GTIDExecuted string `json:"gtid_executed"`
	TookMs       int64  `json:"took_ms"`
	// AdminReplaced: the copy brought the master's accounts (CLONE, or a
	// dump of every database) and the stored admin account no longer logs
	// in. The data is in place; the clone_status check (CLONE) and the
	// GTID set are left to the panel once the account is reconnected, and
	// GTIDExecuted may be empty.
	AdminReplaced bool `json:"admin_replaced,omitempty"`
}

// ReplicationSourceParams are the parameters of mysql.configureReplication:
// the master as seen from this server, the replication account, and the
// databases to replicate.
type ReplicationSourceParams struct {
	Host      string   `json:"host"`
	Port      int      `json:"port"`
	User      string   `json:"user"`
	Password  string   `json:"password"`
	SSL       bool     `json:"ssl"`       // connect to the master over SSL
	Databases []string `json:"databases"` // filter; empty = every database
	// VerifyServerCert: the master's certificate must chain to the CA file
	// SSLCA (a path on this server, normally <datadir>/dbpanel-tls/mysql-ca.crt)
	// and name Host. It needs SSL and SSLCA.
	SSLCA            string `json:"ssl_ca,omitempty"`
	VerifyServerCert bool   `json:"verify_server_cert,omitempty"`
}

// ReplicationFilterParams are the parameters of mysql.setReplicationFilter.
type ReplicationFilterParams struct {
	Databases []string `json:"databases"` // empty = every database
}

// DropDatabasesParams are the parameters of mysql.dropDatabases.
type DropDatabasesParams struct {
	Databases []string `json:"databases"`
}

// DropDatabasesResult is the answer to mysql.dropDatabases.
type DropDatabasesResult struct {
	Dropped []string `json:"dropped"` // the databases that existed and were dropped
}

// WaitSyncedParams are the parameters of mysql.waitSynced.
type WaitSyncedParams struct {
	GTIDSet    string `json:"gtid_set"` // usually the master's gtid_executed
	TimeoutSec int    `json:"timeout_sec"`
}

// WaitSyncedResult is the answer to mysql.waitSynced.
type WaitSyncedResult struct {
	Synced   bool          `json:"synced"`   // GTIDSet is executed on this server
	Executed string        `json:"executed"` // this server's @@gtid_executed, as MySQL returns it
	Status   ReplicaStatus `json:"status"`   // zero when no replication source is configured
}

// StartUntilParams are the parameters of mysql.startUntil.
type StartUntilParams struct {
	GTIDSet    string `json:"gtid_set"` // the applier stops after these transactions
	TimeoutSec int    `json:"timeout_sec"`
}

// InstallParams are the parameters of mysql.install.
type InstallParams struct {
	Series string `json:"series"` // "8.0" | "8.4" | "9"
	// ExactVersion is the version the panel expects; a different one is
	// only a warning in the job log.
	ExactVersion string `json:"exact_version,omitempty"`
}

// InstallResult is the job.done result of mysql.install.
type InstallResult struct {
	Version string `json:"version"` // @@version of the installed server
	Source  string `json:"source"`  // "distro" | "mysql-repo"
}

// TLSRequestResult is the answer to mysql.tlsRequest: a CSR for the new
// key the agent keeps in <datadir>/dbpanel-tls/server.key.new. Only the
// public key counts; the panel sets the subject and the names.
type TLSRequestResult struct {
	CSRPEM   string `json:"csr_pem"`
	Hostname string `json:"hostname"`
}

// TLSInstallParams are the parameters of mysql.tlsInstall: the server
// certificate the panel signed for the requested key, its CA, and the name
// a fresh connection checks the certificate against (the server's address
// in the panel).
type TLSInstallParams struct {
	CertPEM    string `json:"cert_pem"`
	CAPEM      string `json:"ca_pem"`
	VerifyHost string `json:"verify_host"`
}

// TLSInstallResult is the answer to mysql.tlsInstall.
type TLSInstallResult struct {
	Serial   string    `json:"serial"` // 32 lowercase hex digits, as the panel's CA prints it
	NotAfter time.Time `json:"not_after"`
	Reloaded bool      `json:"reloaded"` // MySQL serves the certificate now (ALTER INSTANCE RELOAD TLS)
}

// TLSTrustParams are the parameters of mysql.tlsTrust: the CA a replica
// verifies its source's certificate with.
type TLSTrustParams struct {
	CAPEM string `json:"ca_pem"`
}

// TLSTrustResult is the answer to mysql.tlsTrust: where the CA file is,
// for SSLCA.
type TLSTrustResult struct {
	CAPath string `json:"ca_path"`
}

// PeerCred is how an agent logs in to another MySQL server of the
// topology (the replication account) to read its globals during a
// promotion. The agent never stores it.
type PeerCred struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	Password string `json:"password"`
	// SSLCA, with Verify, is the CA file the server's certificate is
	// verified against (chain and host name); without Verify the
	// connection uses TLS when the server offers it, unverified.
	SSLCA  string `json:"ssl_ca,omitempty"`
	Verify bool   `json:"verify,omitempty"`
	// ServerName, set by the agent only (never sent), is the name the
	// certificate is verified against when Host is an address the agent
	// resolved that name to; "" means Host.
	ServerName string `json:"-"`
}

// LockForPromoteParams are the parameters of mysql.lockForPromote: the
// replica about to be promoted and how long the lock lasts before the
// agent's guard checks on it (60 to 3600 seconds).
type LockForPromoteParams struct {
	Candidate  PeerCred `json:"candidate"`
	TimeoutSec int      `json:"timeout_sec"`
}

// LockForPromoteResult is the answer to mysql.lockForPromote: when the
// master was locked, the lock's deadline, and the master's identity and
// final gtid_executed.
type LockForPromoteResult struct {
	LockedAt     time.Time `json:"locked_at"`
	Deadline     time.Time `json:"deadline"`
	ServerUUID   string    `json:"server_uuid"`
	GTIDExecuted string    `json:"gtid_executed"`
}

// UnlockPromoteParams are the parameters of mysql.unlockPromote: the
// lock's candidate, which must still be a replica.
type UnlockPromoteParams struct {
	Candidate PeerCred `json:"candidate"`
}

// Promotion modes of mysql.promote.
const (
	// PromotePlanned: the old master answers and is locked read-only.
	PromotePlanned = "planned"
	// PromoteEmergency: the old master is down.
	PromoteEmergency = "emergency"
)

// PromoteLockGrace is how long a master's promote lock guard waits, after
// reading the candidate read-only at or past the lock's deadline, before
// reading it again and opening the master. The panel adds it to the
// deadline when it decides whether the lock may still be held.
const PromoteLockGrace = 60 * time.Second

// PromoteParams are the parameters of mysql.promote, run on the replica
// being promoted: the mode, the master it follows (OldSource, which must
// be its pinned source), the deadline of the old master's promote lock
// (planned; nil when unknown) and the replication account this server
// keeps for its own replicas.
type PromoteParams struct {
	Mode         string     `json:"mode"`
	OldSource    PeerCred   `json:"old_source"`
	LockDeadline *time.Time `json:"lock_deadline,omitempty"`
	// LockRemainingSec is how many seconds were left until LockDeadline
	// when the panel sent the request (its clock). The agent measures the
	// lock's margin with it on its own clock; LockDeadline is only logged.
	LockRemainingSec *int   `json:"lock_remaining_sec,omitempty"`
	ReplUser         string `json:"repl_user"`
	ReplPassword     string `json:"repl_password"`
	// NotBefore is when the panel's promotion started. A server already
	// pinned master by a promotion from OldSource recorded at or after it
	// answers with that promotion (Resumed) instead of refusing, so a
	// retry after the agent's own job or resume marker is gone still
	// completes. Zero: never.
	NotBefore time.Time `json:"not_before,omitzero"`
}

// PromoteResult is the result of the mysql.promote job: this server's
// gtid_executed and server_uuid as the new master, how many relay log
// transactions it applied before opening writes (and which it gave up
// on), and whether it resumed a
// promotion that had already opened writes.
type PromoteResult struct {
	GTIDExecuted string `json:"gtid_executed"`
	ServerUUID   string `json:"server_uuid"`
	Applied      int    `json:"applied"`
	// Unapplied are the incomplete transactions of the relay log given
	// up on (a master that died sending one); "" when none.
	Unapplied string `json:"unapplied,omitempty"`
	Resumed   bool   `json:"resumed"`
	// Mode is the promotion's mode as recorded: on a resume, the mode of
	// the run that opened writes (its marker), whatever was asked.
	Mode string `json:"mode"`
}

// QuarantineParams are the parameters of mysql.quarantine, run on the old
// master after a promotion: the new master, which must be writable and
// hold this server's transactions.
type QuarantineParams struct {
	NewMaster PeerCred `json:"new_master"`
}

// QuarantineResult is the answer to mysql.quarantine. Applied is false
// when MySQL was down: the pins are written and read-only is put on once
// MySQL answers again (monitor.quarantined then). Already is true when the
// server was quarantined before.
//
// StartLocked says whether the start lock (an init_file in the managed
// config that turns read-only on before any client can log in) will hold
// the next time MySQL starts; StartLockReason says why not, in words for
// the UI. MACConfined: mysqld runs under AppArmor or SELinux.
// OperatorInitFiles are other init_file options read before the agent's
// line, which MySQL does not run while the lock is in place. An older
// agent sends none of these.
type QuarantineResult struct {
	Applied           bool     `json:"applied"`
	Already           bool     `json:"already"`
	StartLocked       bool     `json:"start_locked"`
	StartLockReason   string   `json:"start_lock_reason,omitempty"`
	MACConfined       bool     `json:"mac_confined,omitempty"`
	OperatorInitFiles []string `json:"operator_init_files,omitempty"`
}

// StartLockState is the start lock of a quarantined server as the agent
// last checked it. Present: the agent's init_file line is in the managed
// config. Locked: the line, the file, the agent's file being the last
// init_file mysqld reads, the file readable by mysqld's user and, under
// AppArmor or SELinux, in a place mysqld's profile allows. Reason says
// why Locked is false (Indonesian, for the UI).
type StartLockState struct {
	Present           bool     `json:"present"`
	Locked            bool     `json:"locked"`
	Reason            string   `json:"reason,omitempty"`
	MACConfined       bool     `json:"mac_confined,omitempty"`
	File              string   `json:"file,omitempty"`
	OperatorInitFiles []string `json:"operator_init_files,omitempty"`
}

// RepointParams are the parameters of mysql.repoint, run on a replica of
// a master that was replaced: the new master (a promoted replica of the
// pinned source; its account is also used to read the pinned source),
// whether the replication channel verifies its certificate, the CA to
// store for that when this server has none (CAPEM), and the databases to
// replicate (none = every database).
type RepointParams struct {
	NewMaster PeerCred `json:"new_master"`
	Verify    bool     `json:"verify"`
	CAPEM     string   `json:"ca_pem,omitempty"`
	Databases []string `json:"databases,omitempty"`
}

// RepointResult is the answer to mysql.repoint. NeedsRebuild: this
// replica holds transactions the new master lacks (Errant); its source
// pin moved to the new master, replication was left as it was, and it
// must be rebuilt from the new master. Otherwise it replicates from the
// new master and IORunning says whether its receiver connected in time
// (IOError: why not, while MySQL keeps retrying).
type RepointResult struct {
	NeedsRebuild bool   `json:"needs_rebuild"`
	Errant       string `json:"errant,omitempty"`
	IORunning    bool   `json:"io_running"`
	// SSLVerifyServerCert: the receiver connected (IORunning) with
	// SOURCE_SSL_VERIFY_SERVER_CERT=1. False from agents before it.
	SSLVerifyServerCert bool `json:"ssl_verify_server_cert,omitempty"`
	// IOError is the receiver's last error when it was still retrying
	// (network, login) at the end of the wait; "" otherwise.
	IOError string `json:"io_error,omitempty"`
}

// ProbeSourceResult is the answer to mysql.probeSource: whether a MySQL
// server answers at the pinned source (Host:Port), and why not.
type ProbeSourceResult struct {
	Reachable bool   `json:"reachable"`
	Err       string `json:"err,omitempty"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
}

// BinlogTailResult is the answer to mysql.binlogTail: the GTID set this
// server executed, from MySQL when it runs (MySQLRunning) or else from its
// binary log files, and the last binary log file.
type BinlogTailResult struct {
	GTIDExecuted string `json:"gtid_executed"`
	File         string `json:"file"`
	MySQLRunning bool   `json:"mysql_running"`
}

// Events of the promote lock's guard on a master.
const (
	// EventQuarantined: the server quarantined itself under its new
	// master (QuarantinedEvent).
	EventQuarantined = "monitor.quarantined"
	// EventLockExpired: the promote lock reached its deadline
	// (LockExpiredEvent).
	EventLockExpired = "monitor.lockExpired"
)

// QuarantinedEvent is the payload of EventQuarantined.
type QuarantinedEvent struct {
	NewSource string `json:"new_source"` // host:port of the new master
}

// LockExpiredEvent is the payload of EventLockExpired.
type LockExpiredEvent struct {
	Action    string `json:"action"`    // "unlocked" | "kept"
	Candidate string `json:"candidate"` // host:port
	Reason    string `json:"reason,omitempty"`
}

// ConnectionsResult is the answer to mysql.connections: the client
// connections to this server grouped by host (without port), user and
// default database, most connections first, read at TakenAt. The panel's
// own accounts (dbpanel_*), replicas reading the binary log, MySQL's own
// threads, the agent's own connection and the sessions of the agent's
// admin account from this server itself (socket, localhost, 127.0.0.1,
// ::1) are left out.
type ConnectionsResult struct {
	Rows    []ConnectionRow `json:"rows"`
	TakenAt time.Time       `json:"taken_at"`
}

// ConnectionRow is one group of mysql.connections: Count connections of
// User from Host with DB as default database ("" for none).
type ConnectionRow struct {
	Host  string `json:"host"`
	User  string `json:"user"`
	DB    string `json:"db"`
	Count int    `json:"count"`
	// AdminAccount: User is the account the agent logs in as (the panel's
	// admin account), connected from another host.
	AdminAccount bool `json:"admin_account,omitempty"`
}

// LeftoverParams are the parameters of mysql.leftover, run on a
// quarantined old master: the new master, whose gtid_executed is
// subtracted from this server's.
type LeftoverParams struct {
	NewMaster PeerCred `json:"new_master"`
}

// LeftoverResult is the answer to mysql.leftover: the transactions this
// server executed that the new master lacks (GTIDSet; Transactions 0 when
// none), the tables they touched with their row counts (row events; a
// statement counts 0 rows), when the first and last of them were
// committed, and the binary log files they were read from. Row data never
// leaves the server here.
type LeftoverResult struct {
	GTIDSet      string       `json:"gtid_set"`
	Transactions int          `json:"transactions"`
	Tables       []TableTouch `json:"tables,omitempty"`
	From         *time.Time   `json:"from,omitempty"`
	To           *time.Time   `json:"to,omitempty"`
	Binlogs      []string     `json:"binlogs,omitempty"`
}

// TableTouch is one table a set of transactions changed and how many rows.
type TableTouch struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Rows   int    `json:"rows"`
}

// SalvageParams are the parameters of mysql.salvage: the transactions to
// extract from this server's binary log into a SQL file.
type SalvageParams struct {
	GTIDSet string `json:"gtid_set"`
}

// SalvageFile is one SQL file mysql.salvage wrote; it is removed at
// ExpiresAt (CreatedAt plus 7 days).
type SalvageFile struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// SalvageListResult is the answer to mysql.salvageList, newest first.
type SalvageListResult struct {
	Files []SalvageFile `json:"files"`
}

// SalvageReadParams are the parameters of mysql.salvageRead: a file of
// mysql.salvageList and where to read from.
type SalvageReadParams struct {
	Name   string `json:"name"`
	Offset int64  `json:"offset"`
}

// SalvageReadResult is one chunk of a salvage file (at most 512 KiB,
// base64 in JSON); EOF is true when it reaches the end of the file.
type SalvageReadResult struct {
	Data []byte `json:"data"`
	EOF  bool   `json:"eof"`
	Size int64  `json:"size"`
}

// SalvageDeleteParams are the parameters of mysql.salvageDelete.
type SalvageDeleteParams struct {
	Name string `json:"name"`
}
