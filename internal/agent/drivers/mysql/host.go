package mysql

import (
	"context"
	"database/sql"
	"io"
	"os"
	"syscall"
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
	// ExecEnv is Exec with env (KEY=VALUE) replacing those variables of the agent's environment.
	// A host that wraps another and overrides Exec must route ExecEnv too, or --print-defaults bypasses it.
	ExecEnv(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error
	// ExecEnvAs is ExecEnv run with the user id, group id and supplementary
	// groups in cred instead of the agent's, so the command never opens a
	// path with root's rights. A host that wraps another and overrides
	// ExecEnv must route it too.
	ExecEnvAs(ctx context.Context, cred *syscall.Credential, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error
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
