// Package instance defines the engine-agnostic handle for a spawned
// worker pane. Persistence engines (tmux, cmux, future zmx) return
// implementations of Handle from their Start/Attach/List calls so
// callers can address a worker without knowing which engine produced
// it.
//
// Phase 1 of the zmx work (see persistence.Engine) introduces this
// interface as the narrow seam between cmd/orch/spawn.go and the
// per-engine packages. The interface is intentionally minimal — only
// the four operations every caller actually needs today.
package instance

import "context"

// Handle is the engine-agnostic view of a spawned worker pane. The
// underlying pane lives in tmux, cmux, or (Phase 2) zmx — the locator
// shape differs per engine, but every handle answers ID/Locator/Wait/
// Kill the same way.
//
// Lifetime: a Handle is valid until Kill returns. Wait may be called
// concurrently from multiple goroutines, but Kill should be called at
// most once. Implementations are best-effort on Wait/Kill — neither is
// on the hot path of orch spawn today; they're here so the abstraction
// is complete for future callers (orch attach, orch kill).
type Handle interface {
	// ID returns the worker slug supplied at spawn time. Empty when no
	// slug was set (legacy pre-#181 panes).
	ID() string

	// Locator returns the engine-native pane locator: tmux pane id
	// ("%37"), cmux surface ref ("surface:30"), or a future zmx session
	// name. Stable for the lifetime of the pane.
	Locator() string

	// Wait blocks until the worker process exits. Best-effort: an engine
	// without a native wait primitive may return an error rather than
	// poll. Honors ctx cancellation.
	Wait(ctx context.Context) error

	// Kill terminates the worker pane. Best-effort: tries graceful first,
	// then forceful. Idempotent on the engine's tolerance.
	Kill() error

	// GracefulShutdown sends an engine-native interrupt (Ctrl-C
	// equivalent) so the worker process can flush + exit cleanly before
	// the caller resorts to Kill. Best-effort: implementations swallow
	// "target already gone" errors so the caller can sequence
	// GracefulShutdown → wait → Kill without branching on partial
	// failure. Honors ctx cancellation for engines whose interrupt verb
	// blocks.
	//
	// Idempotent: safe to call multiple times, and safe to call on a
	// pane that's already exited.
	GracefulShutdown(ctx context.Context) error
}
