package subtree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// TmuxWorkerKiller implements WorkerKiller for executor=tmux workers.
// The kill path mirrors the operator pattern: send SIGINT-equivalent
// (C-c) so the agent gets a graceful interrupt + flush, wait a
// moment, then kill-pane to reclaim the tmux real estate.
//
// Cached handles drive the kill (PaneID + Abort{Kind,Target,Keys}).
// Workers spawned through legacy paths (no cached handle) cannot be
// killed by this killer — callers either resolve them via the live
// registry first or accept that legacy spawns must be torn down
// manually.
//
// CF Worker / CF Durable Object kill paths are not yet implemented;
// when invoked for those AbortKinds the killer returns a clear "not
// implemented for kind=X" error so the operator gets a signal
// instead of a silent skip.
type TmuxWorkerKiller struct {
	// BinPath is the absolute path to tmux. Empty falls back to
	// $PATH lookup.
	BinPath string

	// GracePeriod is the wait between C-c and kill-pane. Default
	// 500ms — long enough for the agent to flush a trailing line,
	// short enough that destroy doesn't drag.
	GracePeriod time.Duration

	// Stderr receives tmux's stderr. Nil discards.
	Stderr *os.File
}

// NewTmuxWorkerKiller constructs the production killer.
func NewTmuxWorkerKiller() *TmuxWorkerKiller {
	return &TmuxWorkerKiller{Stderr: os.Stderr, GracePeriod: 500 * time.Millisecond}
}

// Kill implements WorkerKiller. See type doc for behaviour.
func (k *TmuxWorkerKiller) Kill(ctx context.Context, name string, handle *WorkerHandleRef) error {
	if handle == nil {
		return fmt.Errorf("subtree kill %q: no cached handle — cannot determine pane id (legacy spawn?)", name)
	}
	// Only executor=tmux is implemented today. CF backends will
	// follow the same construction pattern (HTTP POST for cf-worker,
	// DO RPC for cf-durable-object).
	if handle.Executor != "" && handle.Executor != "tmux" {
		return fmt.Errorf("subtree kill %q: executor=%s not yet supported (cf-worker / cf-durable-object are Phase B+)", name, handle.Executor)
	}
	if handle.PaneID == "" {
		return fmt.Errorf("subtree kill %q: handle has no pane id (executor mismatch?)", name)
	}

	bin := k.BinPath
	if bin == "" {
		p, err := exec.LookPath("tmux")
		if err != nil {
			return fmt.Errorf("subtree kill %q: 'tmux' not on PATH: %w", name, err)
		}
		bin = p
	}

	// Step 1: graceful interrupt (C-c equivalent). Skip when the
	// handle declares an abort verb we don't recognise — kill-pane
	// alone is still a correct teardown.
	if handle.AbortKind == "tmux-send-keys" {
		keys := handle.AbortKeys
		if keys == "" {
			keys = "C-c"
		}
		target := handle.AbortVerb
		if target == "" {
			target = handle.PaneID
		}
		if err := k.runTmux(ctx, bin, "send-keys", "-t", target, keys); err != nil {
			// A failed send-keys (pane already gone, e.g.) is non-fatal
			// — we fall through to kill-pane which will either succeed
			// or report the pane is already missing.
			fmt.Fprintf(k.devnull(), "subtree kill %q: send-keys failed (continuing to kill-pane): %v\n", name, err)
		}
		grace := k.GracePeriod
		if grace <= 0 {
			grace = 500 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(grace):
		}
	}

	// Step 2: reclaim the pane. tmux exits non-zero if the pane is
	// already gone; we treat that as success (destroy is idempotent
	// — already-dead is the desired post-state).
	if err := k.runTmux(ctx, bin, "kill-pane", "-t", handle.PaneID); err != nil {
		// Detect "can't find pane" → treat as already-dead.
		if isPaneMissing(err) {
			return nil
		}
		return fmt.Errorf("subtree kill %q: kill-pane: %w", name, err)
	}
	return nil
}

func (k *TmuxWorkerKiller) runTmux(ctx context.Context, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := stderr.String(); msg != "" {
			return fmt.Errorf("%s %v: %w: %s", bin, args, err, msg)
		}
		return fmt.Errorf("%s %v: %w", bin, args, err)
	}
	return nil
}

func (k *TmuxWorkerKiller) devnull() *os.File {
	if k.Stderr != nil {
		return k.Stderr
	}
	// Discard; using stderr at least surfaces failures.
	return os.Stderr
}

// isPaneMissing recognises tmux's "can't find pane" exit. The exit
// status alone is ambiguous; we sniff the stderr substring. Wrapping
// in a helper keeps the check in one place if tmux's message ever
// changes.
func isPaneMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"can't find pane", "no such pane", "pane not found"} {
		if containsLower(msg, marker) {
			return true
		}
	}
	return false
}

func containsLower(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	// Manual lowercase compare — keep dependency-free.
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			a := haystack[i+j]
			b := needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 'a' - 'A'
			}
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
