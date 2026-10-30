package commands

import (
	"context"
	"encoding/json"
	"errors"
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
