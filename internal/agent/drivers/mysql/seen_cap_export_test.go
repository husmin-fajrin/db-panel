package mysql

import "time"

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
