package mysql

import (
	"os"
	"time"
)

// The caps of the seen files and of the promote lock's fan-out (SA-4).
var (
	ErrSeenPortsFull  = errSeenPortsFull
	ErrSeenTrimmed    = errSeenTrimmed
	ReadSeenPortsFull = readSeenPortsFull
)

const (
	MaxSeenPorts  = maxSeenPorts
	MaxSeenSize   = maxSeenSize
	MaxRecordSize = maxRecordSize
	MaxLockReads  = maxLockReads
)

// SetLockReadBudget shortens lockForPromote's budget for its reads.
func SetLockReadBudget(d time.Duration) (restore func()) {
	old := lockReadBudget
	lockReadBudget = d
	return func() { lockReadBudget = old }
}

// ReadTail is how the guard reads the end of MySQL's error log (SA-6).
var ReadTail = readTail

// ManagedConfigPath is the file holding the agent's MySQL options.
func ManagedConfigPath(l Layout) (string, error) {
	p, _, err := managedConfigPath(l)
	return p, err
}

// WriteStartLockFile writes the start lock file at path (SA-7).
var WriteStartLockFile = writeStartLockFile

// SetAfterStartLockOpen runs f between the start lock file's check and its
// owner and mode fix; it returns the function that restores the hook.
func SetAfterStartLockOpen(f func(path string)) (restore func()) {
	old := afterStartLockOpen
	afterStartLockOpen = f
	return func() { afterStartLockOpen = old }
}

// SetOpenLogFile replaces how readTail opens the error log after its
// Lstat check; it returns the function that restores it.
func SetOpenLogFile(f func(path string) (*os.File, error)) (restore func()) {
	old := openLogFile
	openLogFile = f
	return func() { openLogFile = old }
}
