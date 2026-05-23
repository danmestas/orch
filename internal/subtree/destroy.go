package subtree

import (
	"context"
	"fmt"
	"sort"

	"github.com/danmestas/orch/internal/spawnspec"
)

// WorkerKiller is the destroy-time counterpart of WorkerSpawner. Each
// kill verb is executor-specific (tmux kill-pane, CF Worker delete,
// DO drop) — the impl resolves it from the cached WorkerHandle's
// abort block.
type WorkerKiller interface {
	Kill(ctx context.Context, name string, handle *WorkerHandleRef) error
}

// WorkerHandleRef is a minimal projection of spawnspec.WorkerHandle
// carrying just the fields destroy needs (abort verb + target). Keeps
// the Killer interface tight and lets tests substitute fakes without
// constructing full handles.
type WorkerHandleRef struct {
	Executor   string
	PaneID     string
	ID         string
	AbortKind  string
	AbortVerb  string
	AbortKeys  string
}

// SeshTeardown is the optional phase that brings down a sesh hub the
// subtree spawned (sesh.spawn). When the subtree joined an existing
// hub, this is a no-op.
type SeshTeardown interface {
	Down(ctx context.Context, sessionLabel string) error
}

// DestroyOptions toggles teardown behaviour at the operator's
// discretion.
type DestroyOptions struct {
	// PurgeState wipes the seeded KV state buckets. Default is
	// preserve — operator's tasks/goals survive subtree teardown
	// (Proposal 0006, §"Destroy semantics", item 3).
	PurgeState bool
}

// DestroyResult records what destroy actually killed. Used by the
// CLI to print a one-line-per-resource summary (per the
// "post-action re-snapshot is noise" feedback memory: the trailing
// table is dead weight, but per-resource confirmation is fine).
type DestroyResult struct {
	WorkersKilled []string
	SeshTornDown  bool
	CacheRemoved  bool
}

// Destroy tears down the subtree named `name`. Phases in order:
//
//  1. Kill workers per the cached handles (one Kill call per worker)
//  2. If sesh.spawn was used: invoke SeshTeardown
//  3. KV state preserved by default (PurgeState opt-in)
//  4. Remove the applied.yaml cache entry
//
// Idempotent: re-destroying a no-longer-cached subtree returns nil
// (no-op). Partial failures stop the pipeline so the operator can
// investigate without state mutation continuing.
//
// --purge-state is a Phase-B flag whose state-purge backend
// (sesh-ops scope-purge) is not yet implemented. Destroy refuses
// up front when opts.PurgeState is set so the operator does NOT
// end up with workers killed + cache claiming they're alive
// (issue #159). When Phase B lands, the early-return is replaced
// with the real PurgeState → Kill → SeshTeardown → CacheDelete
// ordering.
func (e *Engine) Destroy(ctx context.Context, name string, killer WorkerKiller, sesh SeshTeardown, opts DestroyOptions) (*DestroyResult, error) {
	if e == nil {
		return nil, fmt.Errorf("subtree: nil Engine")
	}
	if killer == nil {
		return nil, fmt.Errorf("subtree destroy: nil WorkerKiller")
	}

	// Refuse --purge-state up front, BEFORE any kill or cache mutation.
	// State purge is wired in when sesh-ops gains a scope-purge verb.
	// Phase A leaves the hook in place so the CLI flag is already
	// valid; the operator gets a clear "not yet wired" surface rather
	// than silent skip — and, critically, no workers are killed
	// (issue #159: ordering used to kill first then refuse, leaving
	// the cache claiming workers were alive after they were dead).
	if opts.PurgeState {
		return &DestroyResult{}, fmt.Errorf("subtree destroy: --purge-state not yet implemented (sesh-ops scope-purge verb missing); workers not killed")
	}

	applied, err := e.Cache.Read(name)
	if err != nil {
		// Idempotent: if the cache entry is gone, there's nothing to
		// destroy and that's not an error. Distinguishing "never
		// applied" from "already destroyed" is the operator's job.
		return &DestroyResult{}, nil
	}

	res := &DestroyResult{}

	// Sort worker kill order for determinism — useful in tests and
	// gives the operator a predictable trace when destroy fails
	// midway.
	names := make([]string, 0, len(applied.Topology.Workers))
	for _, w := range applied.Topology.Workers {
		names = append(names, w.Name)
	}
	sort.Strings(names)

	for _, n := range names {
		ref := buildHandleRef(applied.Workers[n])
		if err := killer.Kill(ctx, n, ref); err != nil {
			return res, fmt.Errorf("subtree destroy: kill %q: %w", n, err)
		}
		res.WorkersKilled = append(res.WorkersKilled, n)
	}

	if applied.Topology.Sesh.Spawn != nil && sesh != nil {
		session := applied.Topology.Sesh.Spawn.Session
		if session == "" {
			session = applied.Name
		}
		if err := sesh.Down(ctx, session); err != nil {
			return res, fmt.Errorf("subtree destroy: sesh down %q: %w", session, err)
		}
		res.SeshTornDown = true
	}

	if err := e.Cache.Delete(name); err != nil {
		return res, fmt.Errorf("subtree destroy: cache delete: %w", err)
	}
	res.CacheRemoved = true
	return res, nil
}

// buildHandleRef projects a cached *spawnspec.WorkerHandle into the
// minimal shape Killer impls actually need. A nil handle yields nil;
// concrete killers fall back to live bus discovery in that case.
func buildHandleRef(h *spawnspec.WorkerHandle) *WorkerHandleRef {
	if h == nil {
		return nil
	}
	ref := &WorkerHandleRef{
		Executor: h.Executor,
		PaneID:   h.PaneID,
		ID:       h.ID,
	}
	if h.Abort != nil {
		ref.AbortKind = h.Abort.Kind
		ref.AbortVerb = h.Abort.Target
		ref.AbortKeys = h.Abort.Keys
	}
	return ref
}

// List returns every subtree name that has a cached applied.yaml.
// Returns names in lexical order so list output is stable across
// runs (Proposal 0006 §"Risks": "subtree sprawl" — predictable list
// helps operators notice forgotten subtrees).
func (e *Engine) List() ([]string, error) {
	if e == nil {
		return nil, fmt.Errorf("subtree: nil Engine")
	}
	names, err := e.Cache.List()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}
