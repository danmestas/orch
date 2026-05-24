package cmux

import (
	"context"
	"fmt"
	"os/exec"
)

// Handle implements instance.Handle for a cmux surface. Locator is the
// cmux surface ref ("surface:30"); ID is the worker slug supplied at
// spawn time.
type Handle struct {
	slug    string
	surface string
}

// NewHandle constructs a cmux Handle. Exported so tests can build one
// without going through Engine.Start.
func NewHandle(slug, surface string) *Handle {
	return &Handle{slug: slug, surface: surface}
}

// ID returns the worker slug.
func (h *Handle) ID() string { return h.slug }

// Locator returns the cmux surface ref.
func (h *Handle) Locator() string { return h.surface }

// Wait blocks until the surface closes. cmux has no native blocking
// wait subcommand in the public CLI (as of 0.x); Phase 1 returns
// ErrNotImplemented rather than fake-poll. Future work can add it
// alongside an `orch attach` driver.
func (h *Handle) Wait(ctx context.Context) error {
	return fmt.Errorf("cmux.Handle.Wait: not implemented")
}

// Kill closes the cmux surface. Best-effort: `cmux close-surface`
// returns non-zero on a missing surface, which we swallow.
func (h *Handle) Kill() error {
	if h.surface == "" {
		return fmt.Errorf("cmux.Handle.Kill: empty surface")
	}
	_ = exec.Command("cmux", "close-surface", "--surface", h.surface).Run()
	return nil
}

// GracefulShutdown sends Ctrl-C to the surface via `cmux send-key
// --surface <surface> ctrl+c`. cmux's send-key verb takes a symbolic
// key name (not a raw byte), so ctrl+c is the correct spelling.
// Best-effort: a closed surface yields a non-zero exit, which we
// swallow.
func (h *Handle) GracefulShutdown(ctx context.Context) error {
	if h.surface == "" {
		return fmt.Errorf("cmux.Handle.GracefulShutdown: empty surface")
	}
	_ = exec.CommandContext(ctx, "cmux", "send-key", "--surface", h.surface, "ctrl+c").Run()
	return nil
}
