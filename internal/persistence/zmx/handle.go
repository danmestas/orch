package zmx

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Handle implements instance.Handle for a zmx session. Locator is the
// operator-supplied session name (which zmx uses as its primary key);
// ID is the worker slug — for zmx these are often the same string,
// but the layer keeps the abstraction parallel to tmux/cmux where
// they differ.
type Handle struct {
	slug        string
	sessionName string
	zmxBin      string
}

// NewHandle constructs a zmx Handle. Exported so tests can build one
// without going through Engine.Start. The zmxBin parameter lets tests
// point at a stub script; production callers should pass the path
// resolved from the engine's lookup.
func NewHandle(slug, sessionName, zmxBin string) *Handle {
	return &Handle{slug: slug, sessionName: sessionName, zmxBin: zmxBin}
}

// ID returns the worker slug.
func (h *Handle) ID() string { return h.slug }

// Locator returns the zmx session name.
func (h *Handle) Locator() string { return h.sessionName }

// Wait blocks until the zmx session ends. zmx has a `zmx wait <name>`
// verb that blocks until a `run`-launched task completes, but it
// doesn't block on session death — and orch cares about session death,
// not task death (the worker may launch sub-tasks). So we poll
// `zmx list --short` at ~1Hz looking for our session to disappear.
//
// Best-effort: ctx cancellation honored. Returns nil when the session
// is gone, ctx.Err() on cancellation.
func (h *Handle) Wait(ctx context.Context) error {
	if h.sessionName == "" {
		return fmt.Errorf("zmx.Handle.Wait: empty session name")
	}
	bin := h.zmxBin
	if bin == "" {
		bin = "zmx"
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		out, err := exec.Command(bin, "list", "--short").Output()
		if err != nil {
			// zmx may have exited / lost its socket. Treat as session gone.
			return nil
		}
		found := false
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if strings.TrimSpace(line) == h.sessionName {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Kill terminates the zmx session via `zmx kill <name> --force`.
// Idempotent: zmx returns 0 even when the named session doesn't exist
// (verified against the live binary at 2026-05-24).
func (h *Handle) Kill() error {
	if h.sessionName == "" {
		return fmt.Errorf("zmx.Handle.Kill: empty session name")
	}
	bin := h.zmxBin
	if bin == "" {
		bin = "zmx"
	}
	// Best-effort. zmx kill --force on a missing target exits 0 (no-op).
	_ = exec.Command(bin, "kill", h.sessionName, "--force").Run()
	return nil
}
