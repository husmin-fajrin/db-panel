package web

import (
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
)

// TestNewKindsHaveLabels: every alert kind has a label for people, never
// the raw kind name (the notify package checks its own kind names).
func TestNewKindsHaveLabels(t *testing.T) {
	for _, k := range monitor.AllKinds() {
		if l, ok := alertKindLabels[k]; !ok || l == "" || l == k {
			t.Errorf("kind %s has no label in alertKindLabels", k)
		}
	}
}
