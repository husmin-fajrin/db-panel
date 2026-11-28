package orchestrator

import "github.com/husmin-fajrin/db-panel/internal/panel/monitor"

// The panel's one meaning of MySQL's two read-only flags (measured on
// mysqld 8.0: read_only ON refuses writes of accounts without SUPER or
// CONNECTION_ADMIN, both ON refuses every account, and read_only OFF with
// super_read_only ON, which only a restart produces, takes writes from
// every account). A read_only the answer does not carry is false: it may
// take writes.

// noWrites reports whether MySQL refuses writes from every account:
// both flags ON. Anything else may take writes from someone.
func noWrites(readOnly, superReadOnly bool) bool { return monitor.NoWrites(readOnly, superReadOnly) }

// appsWrite reports whether ordinary accounts can write: read_only OFF,
// whatever super_read_only says.
func appsWrite(readOnly bool) bool { return !readOnly }
