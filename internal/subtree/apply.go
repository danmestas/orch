package subtree

import (
	"context"
	"fmt"
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

// Phase names the apply pipeline step. The five phases are part of
// the public interface (Proposal 0006, "apply semantics — time-ordered,
// by design"); operators predict where in the sequence apply will
// fail. Renaming or reordering is a breaking change.
type Phase string

const (
	PhaseParse        Phase = "parse"
	PhaseResolveSesh  Phase = "resolve-sesh"
	PhaseSpawnWorkers Phase = "spawn-workers"
	PhaseSeedState    Phase = "seed-state"
	PhasePersist      Phase = "persist"
)

// AllPhases is the strict apply order. Iterating this slice is the
// canonical way to run apply (and the canonical way to enumerate the
// phases in diagnostics / progress UI).
var AllPhases = []Phase{
	PhaseParse,
	PhaseResolveSesh,
	PhaseSpawnWorkers,
	PhaseSeedState,
	PhasePersist,
}

// PhaseError wraps a per-phase failure so callers can dispatch on the
// phase that failed without parsing error strings. Per-phase failure
// modes are part of Proposal 0006's public interface:
//
//   - PhaseSpawnWorkers partial → re-apply seeds state (idempotent)
//   - PhaseSeedState partial → re-apply completes state (idempotent)
//   - PhasePersist failed → subtree is up; `adopt` reconstructs cache
type PhaseError struct {
	Phase Phase
	Err   error
}

func (e *PhaseError) Error() string {
	return fmt.Sprintf("subtree apply: phase %q: %s", e.Phase, e.Err.Error())
}

func (e *PhaseError) Unwrap() error { return e.Err }

func phaseErr(p Phase, err error) error {
	if err == nil {
		return nil
	}
	return &PhaseError{Phase: p, Err: err}
}

// ResolvedSesh is what phase 2 produces and phases 3-5 consume: the
// NATS URL the subtree's workers should attach to, plus a flag
// indicating whether this subtree spawned its own hub (so destroy
// knows whether to tear the hub down too).
type ResolvedSesh struct {
	URL         string
	WeSpawnedIt bool
}

// SeshResolver is phase 2's interface. Implementations:
//
//   - the production NATS implementation (resolves existing URL via
//     env-var expansion done in ResolveEnv, or shells out to
//     `sesh up --session=<name>`)
//   - the test stub (returns a canned URL)
//
// Hiding the implementation behind an interface keeps Apply pure of
// process-spawning, which is what makes contract tests viable.
type SeshResolver interface {
	Resolve(ctx context.Context, s SeshSection, subtreeName string) (ResolvedSesh, error)
}

// LiveRegistry is the read-only side of the registry (Proposal 0005)
// the apply pipeline consults to identify already-running workers.
// Phase 3 compares desired (Topology.Workers) vs actual (live names);
// only the missing ones get spawned.
type LiveRegistry interface {
	// AliveByName returns the names currently registered on the bus
	// (filtered by owner / cwd / labels — left to the impl).
	AliveByName(ctx context.Context) (map[string]struct{}, error)
}

// WorkerSpawner is phase 3's interface. The dispatcher is opaque to
// the apply pipeline — concrete impls invoke orch-spawn with stdin
// SpawnSpec, mock impls return canned WorkerHandles. The handle map
// returned from Apply ends up in AppliedSubtree.Workers so destroy
// has the abort verbs.
type WorkerSpawner interface {
	Spawn(ctx context.Context, spec spawnspec.SpawnSpec, sesh ResolvedSesh) (*spawnspec.WorkerHandle, error)
}

// StateSeeder is phase 4's interface — pass-through to sesh-ops.
// Idempotency is delegated to sesh-ops (which is CAS-on-id for tasks
// and upsert-on-scope-id for goals).
type StateSeeder interface {
	SeedTask(ctx context.Context, t TaskSeed, sesh ResolvedSesh) error
	SeedGoal(ctx context.Context, g GoalSeed, sesh ResolvedSesh) error
}

// CacheStore is phase 5's persistence interface. Concrete impl writes
// to ~/.cache/orch-subtrees/<name>.applied.yaml; tests use an
// in-memory map.
type CacheStore interface {
	Read(name string) (*AppliedSubtree, error)
	Write(a *AppliedSubtree) error
	Delete(name string) error
	List() ([]string, error)
}

// Engine wires the interfaces together. Construct one per CLI
// invocation (or one per test). Callers compose the prod engine in
// cmd/orch-subtree; tests compose a mock engine.
//
// All fields are required for Apply; Status / Destroy / Diff / List
// each declare which fields they consult so partial engines (e.g.,
// "just diff, no live bus") are easy to construct.
type Engine struct {
	Sesh     SeshResolver
	Registry LiveRegistry
	Spawner  WorkerSpawner
	Seeder   StateSeeder
	Cache    CacheStore

	// Now is the clock used to stamp AppliedSubtree.AppliedAt. Default
	// time.Now; injected so tests get deterministic timestamps.
	Now func() time.Time
}

// ApplyResult records what changed during apply. Each slice contains
// the names of workers/seeds that were processed in this run, so the
// CLI can print "spawned: X, Y; already running: Z" output.
type ApplyResult struct {
	Spawned         []string
	AlreadyRunning  []string
	TasksSeeded     int
	GoalsSeeded     int
	WorkerHandles   map[string]*spawnspec.WorkerHandle
	ResolvedSesh    ResolvedSesh
}

// Apply runs the five-phase pipeline against t. Phase ordering is
// fixed (see AllPhases); each phase wraps its error in PhaseError so
// the caller can dispatch on which phase failed.
//
// Re-runs are idempotent at every phase boundary:
//
//   - PhaseSpawnWorkers compares Topology.Workers to Registry; only
//     missing names get Spawn() invoked.
//   - PhaseSeedState delegates idempotency to sesh-ops (CAS on ids).
//   - PhasePersist overwrites the cache atomically.
func (e *Engine) Apply(ctx context.Context, t *Topology) (*ApplyResult, error) {
	if e == nil {
		return nil, fmt.Errorf("subtree: nil Engine")
	}
	if t == nil {
		return nil, fmt.Errorf("subtree: nil Topology")
	}
	now := e.Now
	if now == nil {
		now = time.Now
	}

	// Phase 1: Parse (validate). The caller has already parsed the
	// YAML, but we re-run Validate here so the engine remains usable
	// from contexts that construct Topology in code (tests, future
	// programmatic callers) without bypassing the checks.
	if err := Validate(t).Err(); err != nil {
		return nil, phaseErr(PhaseParse, err)
	}

	// Phase 2: Resolve sesh.
	sesh, err := e.Sesh.Resolve(ctx, t.Sesh, t.Name)
	if err != nil {
		return nil, phaseErr(PhaseResolveSesh, err)
	}

	// Phase 3: Spawn missing workers.
	res := &ApplyResult{
		WorkerHandles: make(map[string]*spawnspec.WorkerHandle, len(t.Workers)),
		ResolvedSesh:  sesh,
	}
	alive, err := e.Registry.AliveByName(ctx)
	if err != nil {
		return nil, phaseErr(PhaseSpawnWorkers, fmt.Errorf("registry snapshot: %w", err))
	}
	for i := range t.Workers {
		w := &t.Workers[i]
		if _, exists := alive[w.Name]; exists {
			res.AlreadyRunning = append(res.AlreadyRunning, w.Name)
			continue
		}
		handle, err := e.Spawner.Spawn(ctx, w.SpawnSpec, sesh)
		if err != nil {
			return nil, phaseErr(PhaseSpawnWorkers,
				fmt.Errorf("spawn %q: %w", w.Name, err))
		}
		if handle != nil {
			res.WorkerHandles[w.Name] = handle
		}
		res.Spawned = append(res.Spawned, w.Name)
	}

	// Phase 4: Seed state. Errors here do NOT roll back phase 3 —
	// per Proposal 0006, the operator re-applies to fill in partial
	// state.
	for i, ts := range t.State.Tasks {
		if err := e.Seeder.SeedTask(ctx, ts, sesh); err != nil {
			return nil, phaseErr(PhaseSeedState,
				fmt.Errorf("task[%d] %q: %w", i, ts.Title, err))
		}
		res.TasksSeeded++
	}
	for i, gs := range t.State.Goals {
		if err := e.Seeder.SeedGoal(ctx, gs, sesh); err != nil {
			return nil, phaseErr(PhaseSeedState,
				fmt.Errorf("goal[%d] %q: %w", i, gs.Objective, err))
		}
		res.GoalsSeeded++
	}

	// Phase 5: Persist. Failure here leaves the subtree up but the
	// cache empty — `orch subtree adopt` (future) reconstructs from
	// live state.
	applied := &AppliedSubtree{
		SpecVersion:  SpecVersion,
		Name:         t.Name,
		AppliedAt:    now().UTC(),
		Topology:     *t,
		ResolvedNATS: sesh.URL,
		Workers:      res.WorkerHandles,
	}
	if err := e.Cache.Write(applied); err != nil {
		return nil, phaseErr(PhasePersist, err)
	}

	return res, nil
}
