package mysql

import (
	"reflect"
	"testing"
)

func TestMissingGrants(t *testing.T) {
	const acct = "adm@10.0.0.%"
	to := " ON *.* TO 'adm'@'10.0.0.%'"
	allStatic := "SELECT, INSERT, UPDATE, DELETE, CREATE USER, RELOAD, PROCESS, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE"
	cases := []struct {
		name   string
		grants []string
		want   []string
	}{
		{"all privileges with grant option", []string{"GRANT ALL PRIVILEGES ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION"}, nil},
		{"all privileges without grant option", []string{"GRANT ALL PRIVILEGES ON *.* TO `adm`@`10.0.0.%`"},
			[]string{"GRANT BACKUP_ADMIN" + to + " WITH GRANT OPTION", "GRANT USAGE" + to + " WITH GRANT OPTION"}},
		{"explicit lists as MySQL 8.0 shows root", []string{
			"GRANT " + allStatic + " ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
			"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
		}, nil},
		{"super stands in only for two dynamic privileges", []string{
			"GRANT " + allStatic + ", SUPER ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION"},
			[]string{"GRANT BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN" + to + " WITH GRANT OPTION"}},
		{"dynamic privilege missing; BACKUP_ADMIN held but not grantable", []string{
			"GRANT " + allStatic + " ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
			"GRANT SYSTEM_VARIABLES_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO `adm`@`10.0.0.%`",
		}, []string{"GRANT REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN" + to + " WITH GRANT OPTION"}},
		{"only CLONE_ADMIN missing, BACKUP_ADMIN grantable", []string{
			"GRANT " + allStatic + " ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
			"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
		}, []string{"GRANT CLONE_ADMIN" + to}},
		{"static WITH GRANT OPTION, dynamic without", []string{
			"GRANT " + allStatic + " ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
			"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO `adm`@`10.0.0.%`",
		}, []string{"GRANT BACKUP_ADMIN" + to + " WITH GRANT OPTION"}},
		{"dynamic WITH GRANT OPTION, static without", []string{
			"GRANT " + allStatic + " ON *.* TO `adm`@`10.0.0.%`",
			"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
		}, []string{"GRANT USAGE" + to + " WITH GRANT OPTION"}},
		{"database-level grants do not count", []string{
			"GRANT USAGE ON *.* TO `adm`@`10.0.0.%`",
			"GRANT ALL PRIVILEGES ON `app`.* TO `adm`@`10.0.0.%` WITH GRANT OPTION",
			"GRANT `reader`@`%` TO `adm`@`10.0.0.%`",
		}, []string{
			"GRANT CREATE USER, RELOAD, PROCESS, SELECT, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE" + to,
			"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN" + to + " WITH GRANT OPTION",
			"GRANT USAGE" + to + " WITH GRANT OPTION",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := missingGrants(c.grants, acct, masterGrants); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

// TestMissingGrantsReplicaSet: a replica loads dumps, so it needs the data
// privileges on top of everything a master needs.
func TestMissingGrantsReplicaSet(t *testing.T) {
	const acct = "adm@%"
	masterOK := []string{
		"GRANT CREATE USER, RELOAD, PROCESS, SELECT, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE ON *.* TO `adm`@`%` WITH GRANT OPTION",
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN ON *.* TO `adm`@`%` WITH GRANT OPTION",
	}
	if got := missingGrants(masterOK, acct, masterGrants); got != nil {
		t.Fatalf("master set: %q", got)
	}
	want := []string{"GRANT DROP, CREATE, INSERT, UPDATE, DELETE, ALTER, INDEX, CREATE VIEW, CREATE ROUTINE, ALTER ROUTINE, REFERENCES, CREATE TEMPORARY TABLES ON *.* TO 'adm'@'%'"}
	if got := missingGrants(masterOK, acct, replicaGrants); !reflect.DeepEqual(got, want) {
		t.Errorf("replica set:\n%q\nwant\n%q", got, want)
	}
	all := []string{"GRANT ALL PRIVILEGES ON *.* TO `adm`@`%` WITH GRANT OPTION"}
	if got := missingGrants(all, acct, replicaGrants); got != nil {
		t.Errorf("ALL PRIVILEGES: %q", got)
	}

	req := RequiredReplicaGrants()
	seen := map[string]int{}
	for _, g := range req {
		seen[g]++
	}
	for _, g := range append(RequiredGrants(), "DROP", "CREATE TEMPORARY TABLES", "TRIGGER", "EVENT") {
		if seen[g] != 1 {
			t.Errorf("RequiredReplicaGrants lists %q %d times: %q", g, seen[g], req)
		}
	}
}

func TestQuoteAccount(t *testing.T) {
	for in, want := range map[string]string{
		"root@localhost": "'root'@'localhost'",
		"o'neil@%":       "'o''neil'@'%'",
		`a\b@h`:          `'a\\b'@'h'`,
		"we@ird@host":    "'we@ird'@'host'",
	} {
		if got := quoteAccount(in); got != want {
			t.Errorf("quoteAccount(%q) = %s, want %s", in, got, want)
		}
	}
}
