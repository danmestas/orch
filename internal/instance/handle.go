// Package instance defines the cross-engine contract that persistence
// engines produce and that layout engines (and the shim, eventually)
// consume.
//
// Per Proposal 0008 / issue #180 (Ousterhout review #4489183265): the
// Handle is the single seam across the persistence/layout boundary.
// Without it, both LayoutEngine and the shim leak persistence-engine
// internals (TTY shape, pane id, pid, watchdog mechanism).
//
// In Phase A there is exactly one Handle implementation (tmuxHandle, in
// internal/persistence/tmux). The interface earns its depth in Phase B,
// when cmux lands behind the same shape.
package instance

// Handle is the per-worker primitive both engines depend on.
//
// Lifecycle:
//   - Born from PersistenceEngine.Start (or Attach for reconnect).
//   - Consumed by LayoutEngine.Spawn to bind a UI surface onto the
//     worker's TTY.
//   - Wait blocks until the worker process exits — used by the shim's
//     watchdog (today the tmux pane-death listener; tomorrow a cmux
//     session-death event).
//   - Kill is the cross-engine cancel verb. Persistence engines
//     translate it to whatever they own (tmux kill-pane, cmux
//     session-end, etc.). Idempotent: calling Kill on an already-dead
//     worker MUST return nil, not an error.
//
// Implementations MUST be safe for concurrent reads of ID() / TTY() /
// Locator(). Wait() and Kill() are serialized externally.
type Handle interface {
	// ID is the operator-facing stable identity (the slug, per
	// Proposal 0009 / issue #181). Drives bus subject tokens, alias
	// files, and the pane title.
	ID() string

	// Locator is the engine-native worker locator. For tmux this is
	// the pane id ("%64"). For cmux this will be the cmux session id.
	// Layout engines use it to bind UI surfaces; the shim uses it as
	// the watchdog target. Stable for the life of the worker.
	Locator() string

	// Wait blocks until the worker process exits. Returns nil on
	// clean exit, an engine-specific error on abnormal exit (pane
	// died, session was killed externally, etc.). Safe to call
	// multiple times — subsequent calls return the cached exit
	// status.
	Wait() error

	// Kill terminates the worker. Implementations SHOULD attempt a
	// graceful signal (C-c via send-keys for tmux, SIGTERM for cmux)
	// before falling back to forceful termination. Idempotent:
	// returns nil if the worker is already dead.
	Kill() error
}
