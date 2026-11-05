package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestServerTLSRoundTripAndExpiring(t *testing.T) {
	s := openTemp(t)
	a, b := mustServer(t, s, "a"), mustServer(t, s, "b")
	if _, err := s.GetServerTLS(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("before set: %v, want ErrNotFound", err)
	}
	inst := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ta := ServerTLS{ServerID: a.ID, CertSerial: "01", NotAfter: inst.Add(24 * time.Hour), InstalledAt: inst,
		Hosts: []string{"10.0.0.1", "db.example.test"}}
	tb := ServerTLS{ServerID: b.ID, CertSerial: "02", NotAfter: inst.Add(90 * 24 * time.Hour), InstalledAt: inst}
	for _, x := range []ServerTLS{ta, tb} {
		if err := s.SetServerTLS(x); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetServerTLS(a.ID)
	if err != nil || !reflect.DeepEqual(got, ta) {
		t.Fatalf("get = %+v, %v; want %+v", got, err, ta)
	}
	ta.CertSerial = "03"
	if err := s.SetServerTLS(ta); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetServerTLS(b.ID); got.Hosts != nil {
		t.Fatalf("hosts of a record without them = %q, want nil", got.Hosts)
	}
	if got, _ := s.GetServerTLS(a.ID); got.CertSerial != "03" {
		t.Fatalf("upsert serial = %q, want 03", got.CertSerial)
	}
	exp, err := s.ListServerTLSExpiring(inst.Add(48 * time.Hour))
	if err != nil || len(exp) != 1 || exp[0].ServerID != a.ID {
		t.Fatalf("expiring = %+v, %v; want only server a", exp, err)
	}
	if err := s.DeleteServer(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetServerTLS(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after server delete: %v, want ErrNotFound", err)
	}
}

func TestReplicaTLSVerified(t *testing.T) {
	s := openTemp(t)
	m, r := mustServer(t, s, "m"), mustServer(t, s, "r")
	topo, err := s.CreateTopology(m.ID, "repl", nil, "10.0.0.1", 3306)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.AddReplica(topo.ID, r.ID, nil)
	if err != nil || rep.TLSVerified {
		t.Fatalf("new replica TLSVerified = %v, %v; want false", rep.TLSVerified, err)
	}
	if err := s.SetReplicaTLSVerified(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetReplicaByServer(r.ID); !got.TLSVerified {
		t.Fatal("TLSVerified not set")
	}
	if err := s.SetReplicaTLSVerified(r.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetReplicaByServer(r.ID); got.TLSVerified {
		t.Fatal("TLSVerified not cleared")
	}
	if err := s.SetReplicaTLSVerified(999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown server: %v, want ErrNotFound", err)
	}
}
