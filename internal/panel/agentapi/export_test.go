package agentapi

import "time"

// ShellQuote exposes shellQuote to the external tests.
var ShellQuote = shellQuote

// SetReadTimeout changes the agent listener's ReadTimeout for listeners
// started afterwards and returns a func restoring it. Not for parallel tests.
func SetReadTimeout(d time.Duration) (restore func()) {
	old := readTimeout
	readTimeout = d
	return func() { readTimeout = old }
}
