package proto

// DatabaseInfo describes one MySQL database found on a server.
type DatabaseInfo struct {
	Name    string   `json:"name"`
	Bytes   uint64   `json:"bytes"`
	Tables  int      `json:"tables"`
	Engines []string `json:"engines,omitempty"`
}
