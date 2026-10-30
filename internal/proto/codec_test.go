package proto

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := map[string]Envelope{
		"request": {
			Kind:    KindRequest,
			Request: &Request{ID: "r1", Cmd: "probe", Params: json.RawMessage(`{"a":1}`)},
		},
		"response": {
			Kind:     KindResponse,
			Response: &Response{ID: "r1", OK: true, Result: json.RawMessage(`{"hostname":"db1"}`)},
		},
		"event": {
			Kind:  KindEvent,
			Event: &Event{Type: "job.progress", JobID: "j1", Payload: json.RawMessage(`{"pct":50}`)},
		},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			b, err := Encode(in)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			out, err := Decode(b)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !reflect.DeepEqual(in, out) {
				t.Fatalf("round trip mismatch:\n in: %+v\nout: %+v", in, out)
			}
		})
	}
}

func TestDecodeRejectsUnknownKind(t *testing.T) {
	_, err := Decode([]byte(`{"kind":"x"}`))
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if !strings.Contains(err.Error(), `"x"`) {
		t.Fatalf("error should name the kind, got: %v", err)
	}
}

func TestDecodeRejectsMissingBody(t *testing.T) {
	for _, kind := range []string{KindRequest, KindResponse, KindEvent} {
		if _, err := Decode([]byte(`{"kind":"` + kind + `"}`)); err == nil {
			t.Errorf("kind %q: expected error for missing body", kind)
		}
	}
}

func TestDecodeRejectsInvalidJSON(t *testing.T) {
	if _, err := Decode([]byte(`not json`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestErrorResponseHasOKFalse(t *testing.T) {
	r := ErrorResponse("r1", CodeBadParams, "bad")
	if r.ID != "r1" || r.OK {
		t.Fatalf("unexpected response: %+v", r)
	}
	if r.Error == nil || r.Error.Code != CodeBadParams || r.Error.Message != "bad" {
		t.Fatalf("unexpected error body: %+v", r.Error)
	}
}

func TestNewResponse(t *testing.T) {
	r, err := NewResponse("r1", map[string]int{"n": 3})
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || r.Error != nil || string(r.Result) != `{"n":3}` {
		t.Fatalf("unexpected response: %+v", r)
	}

	r, err = NewResponse("r2", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || len(r.Result) != 0 {
		t.Fatalf("nil result should yield empty Result, got %q", r.Result)
	}

	if _, err := NewResponse("r3", make(chan int)); err == nil {
		t.Fatal("expected marshal error for unsupported type")
	}
}

func TestProbeResultJSONTags(t *testing.T) {
	b, err := json.Marshal(ProbeResult{Disks: []DiskInfo{{Mount: "/"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"hostname"`, `"os"`, `"os_family"`, `"arch"`, `"kernel"`, `"cpu_cores"`,
		`"mem_total_mb"`, `"disks"`, `"mount"`, `"total_mb"`, `"used_mb"`, `"mysql"`,
		`"installed"`, `"version"`, `"service_name"`, `"service_active"`, `"port"`,
		`"data_dir"`, `"agent_version"`,
	} {
		if !strings.Contains(string(b), key) {
			t.Errorf("missing key %s in %s", key, b)
		}
	}
}
