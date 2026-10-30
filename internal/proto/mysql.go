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
