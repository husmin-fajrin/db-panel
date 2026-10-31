package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// ErrNotConnected is returned by Open when no admin account is stored and
// root cannot log in over the socket without a password.
var ErrNotConnected = errors.New("MySQL belum dihubungkan dan root tanpa sandi tidak tersedia")

const maxOpenConns = 4

// DSN builds the driver DSN for p; Port 0 with Host "" or "localhost" uses socket.
func DSN(p proto.ConnectParams, socket string) string {
	c := drv.NewConfig()
	c.User, c.Passwd = p.User, p.Password
	switch {
	case p.Port > 0 || (p.Host != "" && p.Host != "localhost"):
		host, port := p.Host, p.Port
		if host == "" || host == "localhost" {
			host = "127.0.0.1"
		}
		if port == 0 {
			port = 3306
		}
		c.Net, c.Addr = "tcp", net.JoinHostPort(host, strconv.Itoa(port))
	default:
		c.Net, c.Addr = "unix", socket
	}
	c.ParseTime = true
	c.Timeout = 5 * time.Second
	c.ReadTimeout = 10 * time.Minute
	c.WriteTimeout = 10 * time.Minute
	c.InterpolateParams = false
	return c.FormatDSN()
}

// OpenDB connects with the account in creds, or, when none is stored, as
// root over socket with rootPassword (usually empty: auth_socket or an
// insecure initialisation). The pool is pinged before it is returned.
func OpenDB(ctx context.Context, creds CredentialStore, socket, rootPassword string, logger *slog.Logger) (*sql.DB, error) {
	var p proto.ConnectParams
	stored := false
	if creds != nil {
		var err error
		p, stored, err = creds.Load()
		if err != nil {
			return nil, fmt.Errorf("baca kredensial MySQL: %w", err)
		}
	}
	if !stored {
		p = proto.ConnectParams{User: "root", Password: rootPassword}
	}
	db, err := openWith(ctx, socket, p)
	if err != nil {
		if !stored {
			if logger != nil {
				logger.Warn("mysql root login over socket failed", "socket", socket, "err", err)
			}
			return nil, notConnectedError{err}
		}
		return nil, &LoginError{Err: err}
	}
	return db, nil
}

// LoginError is Open's error when the stored account is refused or cannot
// connect. Err is the driver's error (a *mysql.MySQLError when the server
// answered).
type LoginError struct{ Err error }

func (e *LoginError) Error() string { return "login MySQL gagal: " + e.Err.Error() }
func (e *LoginError) Unwrap() error { return e.Err }

// notConnectedError is ErrNotConnected (its text, errors.Is) that keeps the
// root login's own error for errors.As.
type notConnectedError struct{ err error }

func (e notConnectedError) Error() string        { return ErrNotConnected.Error() }
func (e notConnectedError) Is(target error) bool { return target == ErrNotConnected }
func (e notConnectedError) Unwrap() error        { return e.err }

// Server errors that refuse a login: ER_ACCESS_DENIED_ERROR and
// ER_ACCESS_DENIED_NO_PASSWORD_ERROR (auth_socket for another OS user).
const (
	errAccessDenied           = 1045
	errAccessDeniedNoPassword = 1698
)

// LoginRefused reports whether err is the server refusing a login (1045,
// 1698): the server answered, so it is up, but the account does not work.
func LoginRefused(err error) bool {
	var me *drv.MySQLError
	return errors.As(err, &me) && (me.Number == errAccessDenied || me.Number == errAccessDeniedNoPassword)
}

// openWith connects with p (over socket for a local account) and pings. The
// error is the driver's own, so callers can read its *drv.MySQLError.
func openWith(ctx context.Context, socket string, p proto.ConnectParams) (*sql.DB, error) {
	db, err := sql.Open("mysql", DSN(p, socket))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxOpenConns)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
