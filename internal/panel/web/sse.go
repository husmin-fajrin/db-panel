package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	ssePingInterval = 20 * time.Second
	sseBuffer       = 16 // events queued per client before new ones are dropped
)

// Broker fans out server-sent events to every connected browser.
type Broker struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewBroker returns a broker with no subscribers.
func NewBroker() *Broker {
	return &Broker{subs: make(map[chan []byte]struct{})}
}

// Publish sends event with data (marshalled as JSON) to all subscribers.
// It never blocks: a subscriber whose buffer is full misses the event.
func (b *Broker) Publish(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("sse: marshal event", "event", event, "err", err)
		return
	}
	msg := fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, payload)
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *Broker) subscribe() chan []byte {
	ch := make(chan []byte, sseBuffer)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// ServeHTTP streams events until the client goes away. The subscription is
// in place before the response headers are sent, so once a client sees the
// headers it receives every later Publish.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ch := b.subscribe()
	defer b.unsubscribe(ch)

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // stop nginx from buffering the stream
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}

	ping := time.NewTicker(ssePingInterval)
	defer ping.Stop()
	for {
		var msg []byte
		select {
		case <-r.Context().Done():
			return
		case msg = <-ch:
		case <-ping.C:
			msg = []byte(": ping\n\n")
		}
		if _, err := w.Write(msg); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}
