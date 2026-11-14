package agent

// The tests never write to this machine's system log: a test that wants
// the lines replaces cliSyslog itself.
func init() {
	cliSyslog = func(string) error { return nil }
}
