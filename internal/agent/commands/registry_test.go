package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func TestDispatchUnknownCommand(t *testing.T) {
	r := NewRegistry()
	resp := r.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "nope"})
	if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeUnknownCommand {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Error.Message != "perintah tidak dikenal: nope" {
		t.Errorf("message = %q", resp.Error.Message)
	}
	if resp.ID != "1" {
		t.Errorf("ID = %q", resp.ID)
	}
}

func TestDispatchMarshalsResult(t *testing.T) {
	r := NewRegistry()
	r.Register("echo", func(ctx context.Context, params json.RawMessage) (any, error) {
		return map[string]string{"got": string(params)}, nil
	})
	resp := r.Dispatch(context.Background(), proto.Request{ID: "2", Cmd: "echo", Params: json.RawMessage(`{"a":1}`)})
	if !resp.OK || resp.Error != nil {
		t.Fatalf("resp = %+v", resp)
	}
	var got map[string]string
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got["got"] != `{"a":1}` {
		t.Errorf("result = %v", got)
	}
}

func TestDispatchParamError(t *testing.T) {
	r := NewRegistry()
	r.Register("bad", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, &ParamError{Msg: "parameter salah"}
	})
	resp := r.Dispatch(context.Background(), proto.Request{ID: "3", Cmd: "bad"})
	if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeBadParams {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Error.Message != "parameter salah" {
		t.Errorf("message = %q", resp.Error.Message)
	}
}

func TestDispatchHandlerError(t *testing.T) {
	r := NewRegistry()
	r.Register("fail", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	})
	resp := r.Dispatch(context.Background(), proto.Request{ID: "4", Cmd: "fail"})
	if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeInternal || resp.Error.Message != "boom" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestDispatchRecoversPanic(t *testing.T) {
	r := NewRegistry()
	r.Register("panic", func(ctx context.Context, params json.RawMessage) (any, error) {
		panic("kaboom")
	})
	resp := r.Dispatch(context.Background(), proto.Request{ID: "5", Cmd: "panic"})
	if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeInternal {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.ID != "5" {
		t.Errorf("ID = %q", resp.ID)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	r := NewRegistry()
	h := func(ctx context.Context, params json.RawMessage) (any, error) { return nil, nil }
	r.Register("x", h)
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r.Register("x", h)
}

func TestNamesSorted(t *testing.T) {
	r := NewRegistry()
	h := func(ctx context.Context, params json.RawMessage) (any, error) { return nil, nil }
	r.Register("b", h)
	r.Register("a", h)
	r.Register("c", h)
	got := r.Names()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("Names = %v", got)
	}
}

func TestDispatchWrappedParamError(t *testing.T) {
	r := NewRegistry()
	r.Register("bad", func(ctx context.Context, params json.RawMessage) (any, error) {
		return nil, fmt.Errorf("validasi: %w", &ParamError{Msg: "parameter salah"})
	})
	resp := r.Dispatch(context.Background(), proto.Request{ID: "6", Cmd: "bad"})
	if resp.OK || resp.Error == nil || resp.Error.Code != proto.CodeBadParams {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Error.Message != "parameter salah" {
		t.Errorf("message = %q", resp.Error.Message)
	}
}

func TestDenyRefusesWithoutRunning(t *testing.T) {
	r := NewRegistry()
	ran := false
	r.Register("mysql.dropDatabases", func(ctx context.Context, _ json.RawMessage) (any, error) {
		ran = true
		return nil, nil
	})
	r.Register("probe", func(ctx context.Context, _ json.RawMessage) (any, error) { return "ok", nil })
	r.Deny("mysql.dropDatabases")
	resp := r.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.dropDatabases", Params: json.RawMessage(`{"databases":["simrs"]}`)})
	if resp.OK || resp.Error.Code != proto.CodeBadParams || resp.Error.Message != ErrDenied || ran {
		t.Fatalf("denied command: resp %+v / %+v, ran %v", resp, resp.Error, ran)
	}
	if resp := r.Dispatch(context.Background(), proto.Request{ID: "2", Cmd: "probe"}); !resp.OK {
		t.Fatalf("other command refused: %+v", resp.Error)
	}
}

func TestDispatchLogsEveryCommandRedacted(t *testing.T) {
	var buf bytes.Buffer
	r := NewRegistry()
	r.SetLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	r.Register("mysql.connect", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return nil, &ParamError{Msg: "login MySQL gagal: 1045"}
	})
	r.Register("mysql.status", func(ctx context.Context, _ json.RawMessage) (any, error) { return "ok", nil })
	r.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "mysql.connect",
		Params: json.RawMessage(`{"user":"root","password":"S3cret-pw","host":"db","nested":{"token":"T0ken-x"}}`)})
	r.Dispatch(context.Background(), proto.Request{ID: "2", Cmd: "mysql.status"})
	r.Dispatch(context.Background(), proto.Request{ID: "3", Cmd: "no.such"})
	out := buf.String()
	for _, want := range []string{"cmd=mysql.connect", `\"user\":\"root\"`, `\"password\":\"***\"`, `\"token\":\"***\"`,
		"outcome=\"error bad_params: login MySQL gagal: 1045\"", "cmd=mysql.status", "outcome=ok", "took_ms=",
		"cmd=no.such", "unknown_command"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Contains(out, "S3cret-pw") || strings.Contains(out, "T0ken-x") {
		t.Fatalf("log holds a secret:\n%s", out)
	}
	if n := strings.Count(out, "level=INFO msg=command"); n != 3 {
		t.Errorf("%d info lines for 3 commands:\n%s", n, out)
	}
}

func TestRedactParams(t *testing.T) {
	for in, want := range map[string]string{
		``:         ``,
		`not json`: `(tidak terbaca)`,
		`{"Password":"x","list":[{"secret_key":"y"}],"n":1}`: `{"Password":"***","list":[{"secret_key":"***"}],"n":1}`,
	} {
		if got := RedactParams(json.RawMessage(in)); got != want {
			t.Errorf("RedactParams(%s) = %s, want %s", in, got, want)
		}
	}
	if got := RedactParams(json.RawMessage(`{"gtid_set":"` + strings.Repeat("a", 5000) + `"}`)); len(got) > 1100 {
		t.Errorf("long params logged in full (%d bytes)", len(got))
	}
}
