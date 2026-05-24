// Package layout defines the engine that arranges worker UI surfaces
// in the operator's view (split panes, windows, tabs).
//
// Per Proposal 0008 / issue #180: persistence and layout are
// orthogonal axes that tmux happens to bundle. Splitting them earns
// the abstraction when a non-tmux layout engine lands (cmux, libghostty,
// headless-noop).
//
// In Phase A there is exactly one Engine (tmux). The interface earns
// its depth in Phase B.
package layout

import "github.com/danmestas/orch/internal/instance"

// SpawnSpec is the layout-engine slice of a spawn. The Handle gives it
// access to the persistence-engine's worker (TTY, locator); the slug
// drives operator-facing labels (pane title, alias file).
//
// Kept separate from persistence.StartSpec by design: a layout engine
// cannot reach into persistence internals via this struct. The
// instance.Handle is the only seam, per the Ousterhout review.
type SpawnSpec struct {
	// Slug is the operator-facing identity (Proposal 0009).
	Slug string

	// Position is the layout-engine-native preference for where to
	// place the surface. For tmux: right|left|above|below.
	Position string

	// Headless, when true, asks the layout engine to spawn the
	// surface in its detached / off-screen variant. For tmux: in
	// the orch-headless session instead of the operator's window.
	Headless bool
}

// Engine is the cross-engine contract for layout backends.
//
// Implementations MUST be safe for concurrent calls — orch-spawn may
// be invoked many times in parallel.
type Engine interface {
	// Name returns the engine's canonical identifier (must match a
	// registered name in the persistence composition table). "tmux"
	// for the reference impl.
	Name() string

	// Spawn places the worker's UI surface in the operator's view
	// and applies any layout-specific decoration (pane title, status
	// line, slug labels). The Handle is the seam — the layout
	// engine MUST NOT reach into the persistence engine directly.
	//
	// Spawn is logically post-persistence: persistence.Start has
	// already returned a live Handle, and Spawn binds the operator
	// view onto it. For tmux today, persistence and layout actually
	// run inside the same tmux process; this is the orchestration
	// shape, not an engine guarantee.
	Spawn(spec SpawnSpec, h instance.Handle) error

	// Arrange applies a layout-engine-native preset (grid, vertical,
	// horizontal, etc.) to all currently-tracked surfaces. Engines
	// MAY no-op when preset is empty or unknown.
	Arrange(preset string) error

	// Close removes the surface for the given slug. Idempotent —
	// returns nil when no such surface exists.
	Close(slug string) error
}
