package proto

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

// ReplUserParams are the parameters of mysql.createReplUser.
type ReplUserParams struct {
	User     string `json:"user"`
	Password string `json:"password"`
	FromHost string `json:"from_host"` // host part of the account; "" = "%"
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
