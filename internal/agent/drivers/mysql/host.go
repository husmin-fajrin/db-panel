package mysql

import (
	"context"
	"database/sql"
	"io"
	"os"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Host is everything the driver needs from the machine MySQL runs on. The
// agent uses the real host (NewRealHost); tests use testmysql's instances.
type Host interface {
	Layout() Layout
	// Open connects with the stored admin account, or as root over the
	// socket when none is stored.
	Open(ctx context.Context) (*sql.DB, error)
	Service() ServiceController
	// WriteConfig writes content (a [mysqld] section) to IncludeDir/zz-dbpanel.cnf (IncludeFile),
	// or replaces the managed block in ConfigFile when there is no IncludeDir.
	// It returns the file written.
	WriteConfig(content string) (path string, err error)
	// ValidateConfig runs mysqld --validate-config on ConfigFile.
	ValidateConfig(ctx context.Context) error
	DiskFree(path string) (free, total uint64, err error)
	Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error
	TCPReachable(ctx context.Context, host string, port int, timeout time.Duration) error
	OSFamily() string // "debian" | "rhel" | "unknown"
	TempFile(pattern string, mode os.FileMode) (*os.File, error)
}

// CredentialStore keeps the MySQL admin account the user connected with. A
// nil store means no account is stored.
type CredentialStore interface {
	Load() (params proto.ConnectParams, ok bool, err error)
	Save(params proto.ConnectParams) error
	Clear() error
}
