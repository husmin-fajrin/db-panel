package testmysql

import (
	"testing"
	"time"
)

// NewStopped is an instance in dir that was never started, with the
// files, port and server id given; its supervisor is the real one.
func NewStopped(dir string, port int, serverID uint32) *Instance {
	i := &Instance{Dir: dir, Port: port, ServerID: serverID, uid: -1, gid: -1}
	i.ConfigFile = dir + "/my.cnf"
	i.Socket = dir + "/mysql.sock"
	i.PIDFile = dir + "/mysqld.pid"
	i.ErrorLog = dir + "/error.log"
	i.svc = &supervisor{inst: i}
	return i
}

// Teardown is what the end of the test does to i, with the stop bounded
// by timeout instead of stopTimeout.
func (i *Instance) Teardown(t testing.TB, timeout time.Duration) { i.teardown(t, timeout) }
