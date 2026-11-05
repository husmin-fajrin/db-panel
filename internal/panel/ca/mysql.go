package ca

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

const (
	mysqlCACertFile = "mysql-ca.crt"
	mysqlCAKeyFile  = "mysql-ca.key"
	mysqlCNPrefix   = "dbpanel-mysql-"
)

// MySQLCA is the certificate authority for the MySQL server certificates of
// managed servers. It is separate from the agent CA, so a MySQL certificate
// can never authenticate an agent.
type MySQLCA struct {
	ca *CA
}

// LoadOrCreateMySQLCA loads dir/mysql-ca.crt and dir/mysql-ca.key, or creates
// them (both 0600) when neither exists.
func LoadOrCreateMySQLCA(dir string) (*MySQLCA, error) {
	tmpl := &x509.Certificate{
		Subject: pkix.Name{CommonName: "dbpanel-mysql-ca"},
		// Signs MySQL server certificates only.
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	c, err := loadOrCreate(dir, mysqlCACertFile, mysqlCAKeyFile, tmpl, 0o600)
	if err != nil {
		return nil, err
	}
	return &MySQLCA{ca: c}, nil
}

// IssueMySQLCert signs a MySQL server certificate for server serverID. Only
// the CSR's public key (ECDSA P-256) is used: the CN is always
// "dbpanel-mysql-<serverID>" and the SANs come from hosts alone (IPs as IP
// SANs, everything else as DNS names). The serial is 32 lowercase hex digits.
func (m *MySQLCA) IssueMySQLCert(csrPEM []byte, serverID int64, hosts []string) (certPEM []byte, serial string, notAfter time.Time, err error) {
	if serverID <= 0 {
		return nil, "", time.Time{}, errors.New("id server tidak valid")
	}
	if len(hosts) == 0 {
		return nil, "", time.Time{}, errors.New("sertifikat MySQL butuh minimal satu host")
	}
	var dns []string
	var ips []net.IP
	for _, h := range hosts {
		if h == "" {
			return nil, "", time.Time{}, errors.New("host sertifikat MySQL kosong")
		}
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			dns = append(dns, h)
		}
	}
	csr, err := ParseAgentCSR(csrPEM)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	certPEM, sn, notAfter, err := m.ca.issueServerLeaf(mysqlCNPrefix+strconv.FormatInt(serverID, 10), dns, ips, csr.PublicKey)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return certPEM, fmt.Sprintf("%032x", sn), notAfter, nil
}

// CAPEM returns the MySQL CA certificate in PEM form.
func (m *MySQLCA) CAPEM() []byte {
	return m.ca.CAPEM()
}
