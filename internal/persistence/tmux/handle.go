package tmux

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// tmuxHandle is the tmux-specific instance.Handle. Locator() returns
// the pane id ("%64"); Wait() polls tmux for pane death; Kill() sends
// C-c then closes the pane.
type tmuxHandle struct {
	slug    string
	paneID  string
	tmuxBin string

	// waitOnce / waitErr cache the Wait result so multiple callers
	// (shim watchdog, layout teardown) all see the same answer.
	waitOnce sync.Once
	waitErr  error

	// killOnce gates concurrent Kill calls; subsequent calls no-op
	// (idempotent contract).
	killOnce sync.Once
	killErr  error
}

// ID implements instance.Handle.
func (h *tmuxHandle) ID() string { return h.slug }

// Locator implements instance.Handle. Returns the tmux pane id.
func (h *tmuxHandle) Locator() string { return h.paneID }

// Wait implements instance.Handle. Polls tmux for pane existence at
// a low cadence (the pane-death case is operator-driven, not a hot
// loop). Returns nil when the pane has been closed cleanly.
//
// Phase A uses a simple poll; Phase C will switch to a tmux
// `wait-for` channel or NATS pane-died event when those land.
func (h *tmuxHandle) Wait() error {
	h.waitOnce.Do(func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			if !h.paneExists() {
				h.waitErr = nil
				return
			}
		}
	})
	return h.waitErr
}

// Kill implements instance.Handle. Sends C-c to the pane (graceful),
// then closes it. Idempotent — calling on an already-dead pane returns
// nil.
func (h *tmuxHandle) Kill() error {
	h.killOnce.Do(func() {
		if !h.paneExists() {
			h.killErr = nil
			return
		}
		// Graceful first: C-c to the foreground process.
		_ = exec.Command(h.tmuxBin, "send-keys", "-t", h.paneID, "C-c").Run()
		// Brief settle so a well-behaved REPL can flush.
		time.Sleep(200 * time.Millisecond)
		// Hard close.
		if err := exec.Command(h.tmuxBin, "kill-pane", "-t", h.paneID).Run(); err != nil {
			// Re-check existence — if the pane is already gone,
			// kill-pane errors are spurious.
			if !h.paneExists() {
				h.killErr = nil
				return
			}
			h.killErr = fmt.Errorf("tmux engine: kill-pane %s failed: %w", h.paneID, err)
		}
	})
	return h.killErr
}

// paneExists is the engine-internal predicate for "is this pane still
// alive". Implemented via `tmux list-panes -aF '#{pane_id}'` so a
// single tmux invocation answers regardless of the operator's current
// session.
func (h *tmuxHandle) paneExists() bool {
	out, err := exec.Command(h.tmuxBin, "list-panes", "-aF", "#{pane_id}").Output()
	if err != nil {
		// Treat exec failure as "tmux unreachable, can't tell" —
		// we conservatively return true so Wait keeps polling
		// rather than declaring victory.
		return true
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == h.paneID {
			return true
		}
	}
	return false
}
