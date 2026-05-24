package tmux

import (
	"strings"
	"time"
)

// splitLines is a small helper that splits on \n and drops empty
// trailing lines. Keeps Handle.Wait's pane-presence check terse.
func splitLines(s string) []string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

// pollTick returns a channel that fires after the standard pane-wait
// poll interval. Factored so tests could override it (none today).
func pollTick() <-chan time.Time {
	return time.After(500 * time.Millisecond)
}
