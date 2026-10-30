package proto

// DiskInfo describes one mounted filesystem.
type DiskInfo struct {
	Mount   string `json:"mount"`
	TotalMB int64  `json:"total_mb"`
	UsedMB  int64  `json:"used_mb"`
}

// MySQLInfo describes the MySQL installation found on the host.
type MySQLInfo struct {
	Installed     bool   `json:"installed"`
	Version       string `json:"version"`
	ServiceName   string `json:"service_name"`
	ServiceActive bool   `json:"service_active"`
	Port          int    `json:"port"`
	DataDir       string `json:"data_dir"`
}

// ProbeResult is what an agent reports when the panel asks it to describe
// its host.
type ProbeResult struct {
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	OSFamily     string     `json:"os_family"` // "debian" | "rhel" | "unknown"
	Arch         string     `json:"arch"`
	Kernel       string     `json:"kernel"`
	CPUCores     int        `json:"cpu_cores"`
	MemTotalMB   int64      `json:"mem_total_mb"`
	Disks        []DiskInfo `json:"disks"`
	MySQL        MySQLInfo  `json:"mysql"`
	AgentVersion string     `json:"agent_version"`
}
