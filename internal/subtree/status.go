package subtree

import (
	"context"
	"fmt"
	"sort"
)

// WorkerStatus is the per-worker outcome of `subtree status`.
type WorkerStatus struct {
	Name    string
	Alive   bool
	Missing bool // declared in topology but absent from live registry
	Extra   bool // present on bus but not declared (drift indicator)
}

// StatusReport is what `orch subtree status <name>` returns. The CLI
// renders this; programmatic callers compare structurally.
type StatusReport struct {
	Name         string
	ResolvedNATS string
	Workers      []WorkerStatus
	// Tasks / Goals are populated when a sesh-ops query implementation
	// is wired in; in Phase A they remain zero so the CLI can fall
	// back to "n/a — sesh-ops query not yet wired".
	TasksPending    int
	TasksInProgress int
	TasksCompleted  int
}

// Status compares the cached applied.yaml for `name` against live
// registry state. Missing workers are surfaced (operator re-applies);
// extra workers are surfaced (drift — operator decides).
//
// Status is read-only — it never mutates the cache or the bus.
func (e *Engine) Status(ctx context.Context, name string) (*StatusReport, error) {
	if e == nil {
		return nil, fmt.Errorf("subtree: nil Engine")
	}
	applied, err := e.Cache.Read(name)
	if err != nil {
		return nil, fmt.Errorf("subtree status: read cache: %w", err)
	}
	alive, err := e.Registry.AliveByName(ctx)
	if err != nil {
		return nil, fmt.Errorf("subtree status: registry snapshot: %w", err)
	}

	declared := make(map[string]struct{}, len(applied.Topology.Workers))
	report := &StatusReport{
		Name:         applied.Name,
		ResolvedNATS: applied.ResolvedNATS,
	}
	for _, w := range applied.Topology.Workers {
		declared[w.Name] = struct{}{}
		_, here := alive[w.Name]
		report.Workers = append(report.Workers, WorkerStatus{
			Name:    w.Name,
			Alive:   here,
			Missing: !here,
		})
	}
	// Extra workers: alive but not declared in this subtree. Surfacing
	// these helps the operator notice cross-subtree pollution.
	var extras []string
	for n := range alive {
		if _, ok := declared[n]; !ok {
			extras = append(extras, n)
		}
	}
	sort.Strings(extras)
	for _, n := range extras {
		report.Workers = append(report.Workers, WorkerStatus{Name: n, Alive: true, Extra: true})
	}
	return report, nil
}

// DiffEntry is one line of `orch subtree diff` output: a worker, seed
// task, or seed goal that is in the proposed Topology but not in the
// cached one (or vice versa).
type DiffEntry struct {
	Kind   string // "worker" | "task" | "goal"
	Op     string // "add" | "remove" | "change"
	Name   string // worker name, task title, or goal objective
	Reason string // human-readable why; populated for "change"
}

// Diff compares the proposed topology to the cached applied.yaml of
// the same Name. Returns the entries that an apply would change. Pure
// read — no bus or process work.
//
// Phase A scope: name-set comparison (added/removed workers, added/
// removed seeds). Field-level diff (e.g. outfit changed) is left as
// "change" with a coarse reason; deepening to per-field diff is Phase
// B work.
func (e *Engine) Diff(proposed *Topology) ([]DiffEntry, error) {
	if e == nil {
		return nil, fmt.Errorf("subtree: nil Engine")
	}
	if proposed == nil {
		return nil, fmt.Errorf("subtree diff: nil Topology")
	}
	if err := Validate(proposed).Err(); err != nil {
		return nil, err
	}

	prev, err := e.Cache.Read(proposed.Name)
	if err != nil {
		// First-time apply: every entry is an "add". This is the
		// common case for new operators; surfacing the bulk as adds
		// is what they'd expect.
		return diffFromScratch(proposed), nil
	}
	return diffTopologies(&prev.Topology, proposed), nil
}

func diffFromScratch(t *Topology) []DiffEntry {
	out := make([]DiffEntry, 0, len(t.Workers)+len(t.State.Tasks)+len(t.State.Goals))
	for _, w := range t.Workers {
		out = append(out, DiffEntry{Kind: "worker", Op: "add", Name: w.Name})
	}
	for _, ts := range t.State.Tasks {
		out = append(out, DiffEntry{Kind: "task", Op: "add", Name: ts.Title})
	}
	for _, gs := range t.State.Goals {
		out = append(out, DiffEntry{Kind: "goal", Op: "add", Name: gs.Objective})
	}
	return out
}

func diffTopologies(prev, next *Topology) []DiffEntry {
	var out []DiffEntry

	prevWorkers := workerSet(prev)
	nextWorkers := workerSet(next)
	for name := range nextWorkers {
		if _, was := prevWorkers[name]; !was {
			out = append(out, DiffEntry{Kind: "worker", Op: "add", Name: name})
		}
	}
	for name := range prevWorkers {
		if _, still := nextWorkers[name]; !still {
			out = append(out, DiffEntry{Kind: "worker", Op: "remove", Name: name})
		}
	}

	prevTasks := taskSet(prev)
	nextTasks := taskSet(next)
	for k := range nextTasks {
		if _, was := prevTasks[k]; !was {
			out = append(out, DiffEntry{Kind: "task", Op: "add", Name: k})
		}
	}
	for k := range prevTasks {
		if _, still := nextTasks[k]; !still {
			out = append(out, DiffEntry{Kind: "task", Op: "remove", Name: k})
		}
	}

	prevGoals := goalSet(prev)
	nextGoals := goalSet(next)
	for k := range nextGoals {
		if _, was := prevGoals[k]; !was {
			out = append(out, DiffEntry{Kind: "goal", Op: "add", Name: k})
		}
	}
	for k := range prevGoals {
		if _, still := nextGoals[k]; !still {
			out = append(out, DiffEntry{Kind: "goal", Op: "remove", Name: k})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Op != out[j].Op {
			return out[i].Op < out[j].Op
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func workerSet(t *Topology) map[string]struct{} {
	m := make(map[string]struct{}, len(t.Workers))
	for _, w := range t.Workers {
		m[w.Name] = struct{}{}
	}
	return m
}

func taskSet(t *Topology) map[string]struct{} {
	m := make(map[string]struct{}, len(t.State.Tasks))
	for _, ts := range t.State.Tasks {
		m[ts.Title] = struct{}{}
	}
	return m
}

func goalSet(t *Topology) map[string]struct{} {
	m := make(map[string]struct{}, len(t.State.Goals))
	for _, gs := range t.State.Goals {
		m[gs.Objective] = struct{}{}
	}
	return m
}
