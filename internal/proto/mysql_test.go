package proto_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestMySQLStatusOmitsPromoteRequestWhenNil: the promote_request field is
// optional both ways. An agent before it sends no key, which reads as nil
// ("unknown"), and a nil field is not sent; a status without the field (an
// older panel's struct) reads the new JSON without an error.
func TestMySQLStatusOmitsPromoteRequestWhenNil(t *testing.T) {
	var old proto.MySQLStatus
	if err := json.Unmarshal([]byte(`{"version":"8.0.36","read_only":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.PromoteRequest != nil {
		t.Errorf("PromoteRequest from JSON without the key = %v, want nil", *old.PromoteRequest)
	}
	b, err := json.Marshal(proto.MySQLStatus{Version: "8.0.36"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "promote_request") {
		t.Errorf("nil PromoteRequest is sent: %s", b)
	}

	for _, v := range []bool{true, false} {
		b, err := json.Marshal(proto.MySQLStatus{Version: "8.0.36", PromoteRequest: &v})
		if err != nil {
			t.Fatal(err)
		}
		var got proto.MySQLStatus
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.PromoteRequest == nil || *got.PromoteRequest != v {
			t.Errorf("PromoteRequest %v through %s = %v", v, b, got.PromoteRequest)
		}
		// A panel before the field: its struct has no PromoteRequest.
		var before struct {
			Version  string `json:"version"`
			ReadOnly bool   `json:"read_only"`
		}
		if err := json.Unmarshal(b, &before); err != nil || before.Version != "8.0.36" {
			t.Errorf("struct without the field reading %s: %+v, %v", b, before, err)
		}
	}
}
