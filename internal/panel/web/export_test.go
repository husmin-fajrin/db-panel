package web

// SubscriberCount exposes the broker's subscriber count to web_test.
func SubscriberCount(b *Broker) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// PairCommand exposes pairCommand to web_test.
var PairCommand = pairCommand
