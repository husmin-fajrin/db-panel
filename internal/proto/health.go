package proto

// HealthSnapshot is one reading of a server's health: what mysqld answers
// to a few in-memory SHOW commands, plus the host's disk, memory and load.
// When MySQL does not answer, MySQLOK is false, MySQLError says why, and
// only the host fields and AgentVersion are filled.
type HealthSnapshot struct {
	MySQLOK    bool   `json:"mysql_ok"`
	MySQLError string `json:"mysql_error,omitempty"`

	Version           string `json:"version"`
	ServerUUID        string `json:"server_uuid"`
	UptimeSec         int64  `json:"uptime_sec"`
	ThreadsConnected  int    `json:"threads_connected"`
	MaxConnections    int    `json:"max_connections"`
	ThreadsRunning    int    `json:"threads_running"`
	ReadOnly          bool   `json:"read_only"`
	SuperReadOnly     bool   `json:"super_read_only"`
	LogBin            bool   `json:"log_bin"`
	GTIDExecuted      string `json:"gtid_executed"`
	BinlogDumpThreads int    `json:"binlog_dump_threads"` // replicas currently reading this server's binlog

	BinlogTotalBytes uint64 `json:"binlog_total_bytes"` // sum of SHOW BINARY LOGS File_size
	// BinlogOldestAgeSec is how long ago the oldest binary log file was
	// last written; nil when the files cannot be read.
	BinlogOldestAgeSec *int64 `json:"binlog_oldest_age_sec,omitempty"`
	BinlogExpireSec    int64  `json:"binlog_expire_sec"` // binlog_expire_logs_seconds

	Replica *ReplicaStatus `json:"replica,omitempty"` // nil when the server is not a replica

	DiskTotal    uint64  `json:"disk_total"` // the data directory's file system, bytes
	DiskFree     uint64  `json:"disk_free"`
	MemTotal     uint64  `json:"mem_total"` // bytes
	MemAvailable uint64  `json:"mem_available"`
	Load1        float64 `json:"load1"`

	AgentVersion string `json:"agent_version"`
}
