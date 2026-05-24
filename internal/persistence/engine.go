// Package persistence defines the engine that keeps a worker process
// alive across operator disconnects, owns the PTY, and produces an
// instance.Handle others can attach to.
//
// Per Proposal 0008 / issue #180:
//   - Phase A (this PR): interface + tmux ref impl. tmux is the only
//     registered backend.
//   - Phase B (next): cmux backend behind the same interface.
//
// The interface is intentionally narrow (Ousterhout deep modules): four
// verbs, no engine-specific bleed into the signature. Engine-specific
// nouns live in the spawnspec.SpawnSpec discriminator blocks.
package persistence

import "github.com/danmestas/orch/internal/instance"

// StartSpec is the subset of spawnspec.SpawnSpec the persistence engine
// consumes. Kept as a separate struct (vs. passing the full SpawnSpec)
// so engines don't take a dependency on layout-only fields.
//
// In Phase A this is a thin transcription — every field exists in
// spawnspec already. The split earns its keep when cmux lands and we
// can hand it a StartSpec without forcing it to ignore layout fields it
// doesn't care about.
type StartSpec struct {
	// Slug is the stable identity (Proposal 0009). Maps to ORCH_INSTANCE_ID.
	Slug string

	// Agent is the harness name (claude|pi|codex|gemini|echo).
	Agent string

	// Cwd is the worker's working directory.
	Cwd string

	// Env is the additional environment passed to the worker process.
	// Engine-defined required vars (TMUX, NATS_URL, etc.) are added by
	// the engine itself.
	Env map[string]string

	// Outfit / Bundle: the suit-prepare bundle directory (may be empty).
	Outfit string
	Bundle string

	// Bridge is the bus-bridge mode (synadia-plugin | shim-adapter).
	// Persistence engines honor it by setting up the right env for the
	// worker process; the shim sidecar is the dispatcher's concern.
	Bridge string

	// Role is worker | observer. Tagged into the bus metadata.
	Role string

	// Headless, when true, asks the engine to detach the worker into
	// its persistence-engine-native equivalent of a headless container
	// (orch-headless tmux session for tmux).
	Headless bool

	// NoFleet, NoShim, Verify mirror the orch-spawn flags. The
	// persistence engine consumes them to decide which wrapper
	// command to build.
	NoFleet bool
	NoShim  bool
	Verify  bool
}

// Engine is the cross-engine contract for persistence backends.
//
// Implementations MUST be safe for concurrent calls — multiple
// orch-spawn invocations may race against the same engine instance.
type Engine interface {
	// Name returns the engine's canonical identifier (must match a
	// registered name in the composition table). "tmux" for the
	// reference impl.
	Name() string

	// Start spawns a new worker and returns its Handle. Errors with
	// context when the spawn fails before producing a handle (unknown
	// agent, exec failure, etc.). Returning a non-nil Handle with a
	// non-nil error is forbidden — callers may assume one or the
	// other.
	Start(spec StartSpec) (instance.Handle, error)

	// Attach reconnects to an existing worker by slug. Used for
	// recovery scenarios where the dispatcher process restarts but
	// workers survive (tmux's killer feature). Returns
	// ErrNotFound when no worker with that slug is registered.
	Attach(slug string) (instance.Handle, error)

	// List returns handles for all currently-tracked workers. Order
	// is unspecified.
	List() ([]instance.Handle, error)
}
