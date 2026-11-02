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

// EventHealth is the event an agent's monitor sends with a HealthEvent
// payload. The monitor.configure and monitor.snapshot commands answer with
// a HealthEvent too.
const EventHealth = "monitor.snapshot"

// HealthEvent is one health report of an agent's monitor.
type HealthEvent struct {
	Seq uint64 `json:"seq"` // rises by one per report within one agent process
	// TakenAgoMS is how long before sending the snapshot was taken, by the
	// agent's clock; the panel places the report at its own now minus this.
	TakenAgoMS int64 `json:"taken_ago_ms"`
	// Backlog marks a report kept while the panel was unreachable and sent
	// once it was back.
	Backlog  bool           `json:"backlog"`
	Snapshot HealthSnapshot `json:"snapshot"`
}

// MonitorConfigureParams are the parameters of monitor.configure. Each
// interval is 10..3600 seconds; LagThresholdSec 0 keeps the current one.
type MonitorConfigureParams struct {
	CheckEverySec   int `json:"check_every_sec"`
	SummaryEverySec int `json:"summary_every_sec"`
	LagThresholdSec int `json:"lag_threshold_sec"`
	// RoleHint is the role the panel records for the server: "master",
	// "replica" or "" when it has none.
	RoleHint string `json:"role_hint,omitempty"`
}
