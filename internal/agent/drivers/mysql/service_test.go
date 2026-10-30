package mysql

import (
	"context"
	"testing"
)

func TestDiscoverLayoutUnknownService(t *testing.T) {
	sys := &fakeSystem{
		exists: map[string]bool{"/usr/local/mysql/bin/mysqld": true},
		outputs: map[string]string{
			"/usr/local/mysql/bin/mysqld --verbose --help": mysqldHelp("/etc/my.cnf /etc/mysql/my.cnf /usr/local/mysql/etc/my.cnf ~/.my.cnf",
				"3307", "/tmp/mysql.sock", "/usr/local/mysql/data/", "/usr/local/mysql/data/db1.pid"),
			// no systemctl, no init script, and pgrep finds no mysqld_safe
		},
	}
	l, err := DiscoverLayout(context.Background(), sys)
	if err != nil {
		t.Fatal(err)
	}
	if l.ServiceKind != "unknown" || l.ServiceName != "" {
		t.Errorf("service = %q %q, want unknown", l.ServiceKind, l.ServiceName)
	}
	// No config file exists: the first system-wide file is where one would be created.
	if l.ConfigFile != "/etc/my.cnf" || l.Port != 3307 {
		t.Errorf("layout = %+v", l)
	}

	svc := NewServiceController(l, sys, func(context.Context) error { return nil })
	ctx := context.Background()
	for name, op := range map[string]func(context.Context) error{
		"Start": svc.Start, "Stop": svc.Stop, "Restart": svc.Restart,
	} {
		err := op(ctx)
		if err == nil || err.Error() != "cara menjalankan service MySQL tidak dikenali" {
			t.Errorf("%s error = %v", name, err)
		}
	}
	for _, c := range sys.ran {
		if c != "/usr/local/mysql/bin/mysqld --verbose --help" &&
			c != "systemctl list-unit-files --type=service --no-legend" &&
			c != "pgrep -x mysqld_safe" {
			t.Errorf("unexpected command %q", c)
		}
	}
}
