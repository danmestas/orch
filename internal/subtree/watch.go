package subtree

import (
	"context"
	"fmt"
)

// WatchEvent is one notification on the subtree-event stream.
// Producer-side: a real bus subscriber listening to
// `agents.events.>` filtered to this subtree's workers; Phase A stubs
// this out so the CLI surface is in place.
type WatchEvent struct {
	// SubtreeName is the topology this event belongs to.
	SubtreeName string

	// Worker is the worker name the event concerns. Empty for
	// subtree-level events (e.g. sesh hub state changes).
	Worker string

	// Kind is the event type from the bus envelope (e.g. "status",
	// "hb", "prompt"). Opaque pass-through.
	Kind string

	// Payload is the raw event payload — JSON or text. The CLI prints
	// this verbatim; programmatic consumers parse it.
	Payload string
}

// EventStream is the watch interface. The Monitor-friendly pattern
// (one push per state change) is provided by concrete impls (NATS
// subscriber). Phase A ships the type so cmd/orch-subtree can compile
// against it; a real impl arrives with the bench wiring (Phase C).
type EventStream interface {
	Subscribe(ctx context.Context, subtreeName string) (<-chan WatchEvent, error)
}

// Watch streams events for the subtree named `name`. Returns the
// event channel and a func to call when the operator is done (cancels
// the underlying subscription).
//
// Phase A wires the channel through the EventStream interface; a
// nil stream produces a NotImplemented error so the CLI prints a
// clear message instead of looking like it hung.
func (e *Engine) Watch(ctx context.Context, name string, stream EventStream) (<-chan WatchEvent, error) {
	if e == nil {
		return nil, fmt.Errorf("subtree: nil Engine")
	}
	if stream == nil {
		return nil, fmt.Errorf("subtree watch: no EventStream wired (Phase B will plug in the NATS subscriber)")
	}
	if _, err := e.Cache.Read(name); err != nil {
		return nil, err
	}
	return stream.Subscribe(ctx, name)
}
