package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const testToken = "123456789:AAHsecretSECRETsecret_tokenVALUE-xyz"

// botAPI is a fake Telegram Bot API: it records sendMessage calls and
// answers with status (200 when 0) and description.
type botAPI struct {
	srv *httptest.Server

	mu          sync.Mutex
	paths       []string
	msgs        []sentMessage
	status      int
	description string
}

type sentMessage struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

func newBotAPI(t *testing.T) *botAPI {
	t.Helper()
	b := &botAPI{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m sentMessage
		_ = json.Unmarshal(body, &m)
		b.mu.Lock()
		status, desc := b.status, b.description
		b.paths = append(b.paths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type"))
		if status == 0 || status/100 == 2 {
			b.msgs = append(b.msgs, m)
		}
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if status == 0 {
			io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
			return
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": status, "description": desc})
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *botAPI) fail(status int, description string) {
	b.mu.Lock()
	b.status, b.description = status, description
	b.mu.Unlock()
}

func (b *botAPI) messages() []sentMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]sentMessage(nil), b.msgs...)
}

func TestSendPostsToBotAPI(t *testing.T) {
	api := newBotAPI(t)
	tg := &Telegram{BaseURL: api.srv.URL}
	if err := tg.Send(context.Background(), testToken, "-100123", "halo\nbaris dua"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	api.mu.Lock()
	paths := api.paths
	api.mu.Unlock()
	if len(paths) != 1 || paths[0] != "POST /bot"+testToken+"/sendMessage application/json" {
		t.Fatalf("requests = %q", paths)
	}
	msgs := api.messages()
	if len(msgs) != 1 || msgs[0] != (sentMessage{ChatID: "-100123", Text: "halo\nbaris dua", DisableWebPagePreview: true}) {
		t.Fatalf("messages = %+v", msgs)
	}
}

func TestNewTelegramDefaults(t *testing.T) {
	tg := NewTelegram()
	if tg.BaseURL != "https://api.telegram.org" || tg.HTTP == nil || tg.HTTP.Timeout.Seconds() != 10 ||
		tg.HTTP.Transport != http.DefaultTransport {
		t.Fatalf("NewTelegram = %+v", tg)
	}
}

func TestSendErrorHidesToken(t *testing.T) {
	api := newBotAPI(t)
	tg := &Telegram{BaseURL: api.srv.URL}

	// The API refuses: the error has the status and the API's reason.
	api.fail(http.StatusBadRequest, "Bad Request: chat not found")
	err := tg.Send(context.Background(), testToken, "-1", "x")
	if err == nil || !strings.HasPrefix(err.Error(), "Telegram menolak: ") || !strings.Contains(err.Error(), "400") ||
		!strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("API error = %v; want it in Indonesian with the status and the reason", err)
	}
	if strings.Contains(err.Error(), testToken) || strings.Contains(err.Error(), "AAHsecret") {
		t.Fatalf("API error leaks the token: %v", err)
	}

	// The API cannot be reached: Go's error names the URL, token included.
	api.srv.Close()
	err = tg.Send(context.Background(), testToken, "-1", "x")
	if err == nil {
		t.Fatal("Send to a closed server succeeded")
	}
	if strings.Contains(err.Error(), "AAHsecret") || !strings.Contains(err.Error(), "/bot***/sendMessage") ||
		!strings.HasPrefix(err.Error(), "Telegram tidak terjangkau: ") {
		t.Fatalf("transport error = %v; want it in Indonesian, with the URL and the token as ***", err)
	}

	// A malformed token never reaches a URL (it could carry a path or query).
	if err := tg.Send(context.Background(), "12/../x?y", "-1", "x"); err == nil || strings.Contains(err.Error(), "12/../x") {
		t.Fatalf("malformed token error = %v", err)
	}
}
