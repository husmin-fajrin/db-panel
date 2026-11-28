package mysql

import (
	"context"
	"database/sql"
	"fmt"
)

// The one pair of statements every path that opens or locks a master
// sends (spec 5.4 item 2): mysql.unlockPromote, the guard at the deadline,
// mysql.setReadOnly{on:false} and "dbpanel agent unlock-master" open with
// OpenWrites; mysql.lockForPromote, the guard's ensureLocked and "dbpanel
// agent lock-master" lock with LockWrites. Both flags are always stored
// explicitly (SET PERSIST) and never taken out of the persisted set: a
// flag left out lets the option file or an older persisted value decide
// at the next MySQL start, which is how a master once opened came back
// read-only, or writable while super_read_only read ON, after a restart
// (R-188).
//
// The quarantine (persistReadOnly), setReadOnly(true), the start lock and
// a promotion's own write-opening (promote_open.go) keep their own
// statements.

// Statements of OpenWrites and LockWrites, in the order they are sent.
const (
	openSuperStmt = "SET PERSIST super_read_only = OFF"
	openROStmt    = "SET PERSIST read_only = OFF"
	lockSuperStmt = "SET PERSIST super_read_only = ON"
	lockROStmt    = "SET PERSIST read_only = ON"
)

// OpenWrites: SET PERSIST super_read_only = OFF, then SET PERSIST
// read_only = OFF. super_read_only goes first: read_only = OFF alone
// turns super_read_only off at runtime but leaves a persisted
// super_read_only=ON for the next start. Any error means the server may
// still be read-only, now or after a restart, or may be writable already
// (the connection was lost after the statement took effect): the caller
// reports the error and keeps its promote lock file, so the guard locks
// the server again on its next round.
func OpenWrites(ctx context.Context, db execer) error {
	for _, q := range []string{openSuperStmt, openROStmt} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("buka kunci master (%s): %w", q, err)
		}
	}
	return nil
}

// LockWrites: SET PERSIST super_read_only = ON, then SET PERSIST
// read_only = ON. locked is true once the first statement succeeded:
// MySQL then runs with both flags on (super_read_only turns read_only on)
// and loads super_read_only=ON, which turns read_only on again, at its
// next start whatever the option file says. An error with locked true is
// the second statement's: the server is locked now and after a restart,
// only read_only is not stored; the caller logs it and reports locked.
func LockWrites(ctx context.Context, db execer) (locked bool, err error) {
	if _, err := db.ExecContext(ctx, lockSuperStmt); err != nil {
		return false, fmt.Errorf("kunci master (%s): %w", lockSuperStmt, err)
	}
	if _, err := db.ExecContext(ctx, lockROStmt); err != nil {
		return true, fmt.Errorf("master sudah dikunci (super_read_only ON, juga sesudah restart), tetapi %s gagal: %w", lockROStmt, err)
	}
	return true, nil
}

// flagExec is the execer the driver's lock and unlock paths send
// OpenWrites and LockWrites on; tests replace it to fail one statement.
var flagExec = func(db *sql.DB) execer { return db }
