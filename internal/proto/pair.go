package proto

// PairRequest is the body the agent POSTs to {panel}/agent/pair. CSRPEM
// carries only the agent's public key.
type PairRequest struct {
	Token        string `json:"token"`
	Hostname     string `json:"hostname"`
	Arch         string `json:"arch"`
	CSRPEM       string `json:"csr_pem"`
	AgentVersion string `json:"agent_version"`
}

// PairResponse is the panel's answer to a successful pairing.
type PairResponse struct {
	ServerID  int64  `json:"server_id"`
	CertPEM   string `json:"cert_pem"`
	CAPEM     string `json:"ca_pem"`
	AgentAddr string `json:"agent_addr"`
}
