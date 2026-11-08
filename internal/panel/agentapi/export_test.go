package agentapi

import (
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// ShellQuote exposes shellQuote to the external tests.
var ShellQuote = shellQuote

// SetReadTimeout changes the agent listener's ReadTimeout for listeners
// started afterwards and returns a func restoring it. Not for parallel tests.
func SetReadTimeout(d time.Duration) (restore func()) {
	old := readTimeout
	readTimeout = d
	return func() { readTimeout = old }
}

// SetPeekPairTokenErr makes the pairing handler's token peek fail with
// err (nil restores it) and returns a func restoring it.
func SetPeekPairTokenErr(err error) (restore func()) {
	old := peekPairToken
	peekPairToken = func(*store.Store, string, time.Time) (store.PairToken, error) { return store.PairToken{}, err }
	return func() { peekPairToken = old }
}
