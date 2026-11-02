// Package notify sends the panel's alerts to Telegram: the Bot API client,
// the message texts, and the notifier that delivers, repeats and recovers
// alerts.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	defaultAPIBase = "https://api.telegram.org"
	sendTimeout    = 10 * time.Second
	maxReplySize   = 64 << 10 // largest Bot API reply read
	maxDescription = 200      // of the API's reason, in an error text
)

// tokenRe is the form of a bot token ("123456:ABC-def_..."). Nothing else
// reaches a URL path, so no token can add a path or a query.
var tokenRe = regexp.MustCompile(`^[0-9]{1,20}:[A-Za-z0-9_-]{1,100}$`)

var errBadToken = errors.New("token bot tidak valid")

// Telegram is a client of the Telegram Bot API.
type Telegram struct {
	BaseURL string       // "" means https://api.telegram.org
	HTTP    *http.Client // nil means a 10 s client over http.DefaultTransport
}

// NewTelegram returns a client of api.telegram.org with a 10 s timeout,
// going through HTTPS_PROXY when it is set (http.DefaultTransport).
func NewTelegram() *Telegram {
	return &Telegram{BaseURL: defaultAPIBase, HTTP: defaultClient()}
}

func defaultClient() *http.Client {
	return &http.Client{Timeout: sendTimeout, Transport: http.DefaultTransport}
}

// Send posts text to chatID as plain text, without link previews. Its
// error never holds the token: a failed request names the URL with the
// token replaced by "***", and a refusal gives the HTTP status and the
// API's reason.
func (t *Telegram) Send(ctx context.Context, token, chatID, text string) error {
	if !tokenRe.MatchString(token) {
		return errBadToken
	}
	base := strings.TrimRight(t.BaseURL, "/")
	if base == "" {
		base = defaultAPIBase
	}
	client := t.HTTP
	if client == nil {
		client = defaultClient()
	}
	body, err := json.Marshal(map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true})
	if err != nil {
		return err
	}
	endpoint := base + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New(scrub(err.Error(), token))
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = fmt.Errorf("%s %q: %v", ue.Op, base+"/bot***/sendMessage", ue.Err)
		}
		return errors.New(scrub(err.Error(), token))
	}
	defer resp.Body.Close()
	var reply struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplySize))
	jsonErr := json.Unmarshal(raw, &reply)
	if resp.StatusCode/100 != 2 || jsonErr != nil || !reply.OK {
		msg := fmt.Sprintf("Telegram menjawab HTTP %d", resp.StatusCode)
		if d := strings.TrimSpace(reply.Description); d != "" {
			if len(d) > maxDescription {
				d = strings.ToValidUTF8(d[:maxDescription], "")
			}
			msg += ": " + d
		}
		return errors.New(scrub(msg, token))
	}
	return nil
}

// scrub replaces the token, and its secret part on its own, in s in case
// anything echoes them.
func scrub(s, token string) string {
	if token == "" {
		return s
	}
	s = strings.ReplaceAll(s, token, "***")
	if _, secret, ok := strings.Cut(token, ":"); ok && secret != "" {
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}
