package tmux

import (
	"context"
	"fmt"
	"os/exec"
)

// Handle implements instance.Handle for a tmux pane. Locator is the
// tmux pane id (e.g. "%37"); ID is the worker slug supplied at spawn
// time (may be empty for legacy pre-#181 panes).
type Handle struct {
	slug   string
	paneID string
}

// NewHandle constructs a tmux Handle. Exported so tests can build one
// without going through Engine.Start.
func NewHandle(slug, paneID string) *Handle {
	return &Handle{slug: slug, paneID: paneID}
}

// ID returns the worker slug.
func (h *Handle) ID() string { return h.slug }

// Locator returns the tmux pane id.
func (h *Handle) Locator() string { return h.paneID }

// Wait blocks until the pane exits. tmux has no native blocking wait
// for a pane lifecycle; we poll list-panes -F #{pane_id} at modest
// frequency. Best-effort — ctx cancellation honored.
func (h *Handle) Wait(ctx context.Context) error {
	if h.paneID == "" {
		return fmt.Errorf("tmux.Handle.Wait: empty pane id")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out, err := exec.Command("tmux", "list-panes", "-aF", "#{pane_id}").Output()
		if err != nil {
			// tmux may have exited; treat as pane gone.
			return nil
		}
		// Walk output looking for our pane id; if absent, pane is gone.
		found := false
		for _, line := range splitLines(string(out)) {
			if line == h.paneID {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		// Poll cadence — tmux pane lifecycles are seconds-to-minutes;
		// 500ms is responsive without thrashing.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pollTick():
		}
	}
}

// Kill terminates the pane via tmux kill-pane. Idempotent: if the pane
// is already gone tmux returns non-zero, which we swallow.
func (h *Handle) Kill() error {
	if h.paneID == "" {
		return fmt.Errorf("tmux.Handle.Kill: empty pane id")
	}
	// Best-effort. tmux kill-pane on a missing target prints to stderr
	// and exits non-zero; the operator's intent (pane gone) is satisfied
	// either way.
	_ = exec.Command("tmux", "kill-pane", "-t", h.paneID).Run()
	return nil
}

// GracefulShutdown sends Ctrl-C to the pane via `tmux send-keys -t
// <pane> C-c` so the agent can flush + exit before kill-pane reclaims
// the slot. Best-effort: a missing pane (already exited) is not an
// error — the post-condition (worker received SIGINT or is already
// gone) is satisfied either way.
func (h *Handle) GracefulShutdown(ctx context.Context) error {
	if h.paneID == "" {
		return fmt.Errorf("tmux.Handle.GracefulShutdown: empty pane id")
	}
	// Best-effort; swallow exec error. send-keys on a missing pane
	// exits non-zero, and that's fine — Kill will follow.
	_ = exec.CommandContext(ctx, "tmux", "send-keys", "-t", h.paneID, "C-c").Run()
	return nil
}
