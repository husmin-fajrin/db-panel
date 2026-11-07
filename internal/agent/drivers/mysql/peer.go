package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// peerTimeout bounds one ReadPeer or ProbePeer.
const peerTimeout = 5 * time.Second

// PeerState is what ReadPeer read from another server: its read-only
// state, gtid_executed and server_uuid. Reachable is false when the
// server could not be reached, refused the login or did not answer the
// query; Err then says why (never with the password).
type PeerState struct {
	Reachable               bool
	SuperReadOnly, ReadOnly bool
	GTIDExecuted            string
	ServerUUID              string
	Err                     string
}

// readPeer and probePeer are ReadPeer and ProbePeer; tests replace them.
var (
	readPeer  = ReadPeer
	probePeer = ProbePeer
)

// ReadPeer logs in to c's server and reads @@global.super_read_only,
// read_only, gtid_executed and server_uuid, which need no privilege, all
// within timeout. With c.Verify the server's certificate is verified
// against c.SSLCA (chain and host name); otherwise TLS is still required,
// without verification (as the replication channel's SOURCE_SSL=1): a
// server, or someone in between, that offers no TLS is refused rather
// than read in the clear.
func ReadPeer(ctx context.Context, c proto.PeerCred, timeout time.Duration) PeerState {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fail := func(err error) PeerState {
		return PeerState{Err: fmt.Sprintf("%s: %v", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), err)}
	}
	cfg := drv.NewConfig()
	cfg.User, cfg.Passwd = c.User, c.Password
	cfg.Net, cfg.Addr = "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = timeout, timeout, timeout
	if c.Verify {
		pem, err := os.ReadFile(c.SSLCA)
		if err != nil {
			return fail(fmt.Errorf("baca berkas CA: %w", err))
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return fail(fmt.Errorf("berkas CA %s tidak berisi sertifikat", c.SSLCA))
		}
		name := c.Host
		if c.ServerName != "" {
			name = c.ServerName
		}
		cfg.TLS = &tls.Config{RootCAs: pool, ServerName: name, MinVersion: tls.VersionTLS12}
	} else {
		cfg.TLSConfig = "skip-verify"
	}
	conn, err := drv.NewConnector(cfg)
	if err != nil {
		return fail(err)
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	db.SetMaxOpenConns(1)
	var st PeerState
	err = db.QueryRowContext(ctx, `SELECT @@global.super_read_only, @@global.read_only,
		@@global.gtid_executed, @@global.server_uuid`).Scan(&st.SuperReadOnly, &st.ReadOnly, &st.GTIDExecuted, &st.ServerUUID)
	if err != nil {
		return fail(err)
	}
	st.Reachable = true
	return st
}

// mysqlProtocolV10 is the first payload byte of a MySQL server greeting;
// mysqlErrPacket starts the error packet a server sends instead when it
// refuses the connection (too many connections, blocked host).
const (
	mysqlProtocolV10 = 0x0a
	mysqlErrPacket   = 0xff
)

// ProbePeer connects to host:port and reads the first packet the server
// sends, without logging in, within timeout. A MySQL greeting (protocol
// 10), or the error packet a MySQL server sends when it refuses the
// connection, means a MySQL server answered: nil. Anything else, or no
// packet in time, is an error.
func ProbePeer(ctx context.Context, host string, port int, timeout time.Duration) error {
	return probeGreeting(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
}

// probeGreeting is ProbePeer over network ("tcp" or "unix") to addr.
func probeGreeting(ctx context.Context, network, addr string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	// Header: 3-byte little-endian payload length, sequence id; then the
	// payload's first byte.
	var b [5]byte
	if _, err := io.ReadFull(conn, b[:]); err != nil {
		return fmt.Errorf("%s: salam MySQL tidak diterima: %w", addr, err)
	}
	n := uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16
	if n == 0 || b[3] != 0 || (b[4] != mysqlProtocolV10 && b[4] != mysqlErrPacket) {
		return fmt.Errorf("%s: yang menjawab bukan server MySQL", addr)
	}
	return nil
}

// validatePeer checks a peer account taken from the panel; what names the
// peer (host, port, user, CA path) passes the same rules as a replication
// source. The password is never part of an error.
func validatePeer(c *proto.PeerCred, what string) error {
	c.Host, c.User = strings.TrimSpace(c.Host), strings.TrimSpace(c.User)
	bad := func(format string, args ...any) error {
		return &commands.ParamError{Msg: what + " " + fmt.Sprintf(format, args...)}
	}
	switch {
	case !sourceHostRe.MatchString(c.Host), strings.Contains(c.Host, ":") && net.ParseIP(c.Host) == nil:
		return bad("alamat tidak valid: %q", c.Host)
	case c.Port < 1 || c.Port > 65535:
		return bad("port harus antara 1 dan 65535")
	case !userNameRe.MatchString(c.User):
		return bad("nama user tidak valid: %q", c.User)
	case c.Password == "":
		return bad("password wajib diisi")
	case strings.ContainsFunc(c.Password, unicode.IsControl), utf8.RuneCountInString(c.Password) > maxSourcePasswordLen:
		return bad("password tidak valid")
	}
	if err := validateVerify(c.Verify, true, &c.SSLCA); err != nil {
		var pe *commands.ParamError
		if errors.As(err, &pe) {
			return bad("%s", pe.Msg)
		}
		return err
	}
	return nil
}

// peerKey is the host:port a peer is named by in the lock and the pins.
func peerKey(c proto.PeerCred) string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

// gtidHasUUID reports whether the GTID set holds at least one transaction
// of uuid (any interval, any tag). It is computed here in Go rather than
// with GTID_SUBTRACT on this server, so that it needs no connection: an
// entry is "uuid:..." with at least one part after the UUID.
func gtidHasUUID(set, uuid string) bool {
	for _, entry := range strings.Split(set, ",") {
		u, rest, ok := strings.Cut(strings.TrimSpace(entry), ":")
		if ok && rest != "" && strings.EqualFold(u, uuid) {
			return true
		}
	}
	return false
}
