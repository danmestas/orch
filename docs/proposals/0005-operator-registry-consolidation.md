# Proposal 0005 — Operator registry consolidation

**Status:** draft (follow-on; spec only)
**Depends on:** none (in-repo deepening)
**Blocks:** UI improvements that need a unified worker view

## Why

Today, operator-side knowledge of "who's on the bus and what they're called" is scattered across at least five sources:

| Source | What it has | When updated |
|---|---|---|
| `~/.cache/orch-operator.json` | operator's pane id, claim timestamp, transcript path | once per session at `orch-claim-operator` |
| `~/.config/orch-aliases` | hand-maintained pane → role alias map | manually by operator on spawn |
| `~/.cache/orch-shim/<pct-pane>.log` | per-shim startup + ad-hoc logs | by each shim instance |
| `$SRV.INFO.agents` (NATS) | live agent metadata (agent, owner, session, role, outfit, pane_id, protocol_version) | by each shim continuously |
| `nats sub 'agents.hb.>'` | per-instance heartbeats with timing | every 30s per shim |

To answer "what workers are live, what are their roles, when did they last heartbeat" — an operator (or UI, or orch-peek, or a future goal-tracking tool) bounces between all five. Each consumer reinvents the join.

Symptoms hit in recent topology work:

- `orch-tell` failed with "not registered on the bus" because it consults a registry separate from the live bus state
- The UI displays `pct<N>` for primary identifier despite the shim having a `metadata.session` field (UI doesn't join the bus state to display-friendly names)
- After-the-fact debugging (the missing-`Tails` docstring in PR #136, the npm/orch-spawn shadow, etc.) all required reading multiple state sources

The architecture skill calls this a **shallow seam**. The deletion test: imagine removing any single source — complexity reappears across N callers each re-implementing the join. They're earning their keep individually but the cohesion is missing at the join layer. A deepened **registry** module would consolidate behind a single read interface.

## Goals

1. One canonical interface for "what's on the bus": a `Registry` module with `Snapshot() []Worker` and `Watch() <-chan Event` semantics
2. Sources of truth stay where they are (NATS for bus state, file for alias overrides, ad-hoc fields for per-instance config) — the registry is a JOIN, not a new source
3. Consumers (UI, `orch-peek`, `orch-tell`, `orch-ask`, `orch-spy`) talk to the registry, not to the underlying sources
4. The registry is in-process for Go consumers, optional sidecar for CLI / UI / external consumers

## Non-goals

- Replacing the bus as source of truth (the bus is canonical for live state)
- Eliminating `~/.config/orch-aliases` (operator's preferences stay where they are; registry joins them in)
- Centralised state store (no Redis, no etcd; registry composes existing sources)

## Public interface

### Worker shape (Go)

```go
package registry

type Worker struct {
    // Identity (immutable per worker instance)
    PaneID      string  // raw tmux pane id, e.g. "%64"
    InstanceID  string  // micro service instance id from $SRV.INFO
    Subject     string  // bus subject prefix: "agents.{verb}.cc.dmestas.lead-engineer"

    // Display
    Name        string  // operator-friendly name; from alias file or metadata.session; fallback to pct-form
    Role        string  // metadata.role
    Outfit      string  // metadata.outfit

    // Lifecycle
    Agent       string  // metadata.agent ("claude-code" / "codex" / "pi" / "gemini")
    CWD         string  // metadata.cwd
    Owner       string  // metadata.owner
    Session     string  // metadata.session (may be empty)
    LastHB      time.Time
    Alive       bool   // last heartbeat within 2× interval

    // Inputs preserved verbatim for debugging
    Metadata    map[string]string  // raw $SRV.INFO metadata
}

type Event struct {
    Type      EventType  // Joined | Updated | Departed
    Worker    Worker
    Timestamp time.Time
}

type EventType int
const (
    Joined EventType = iota
    Updated
    Departed
)

type Registry interface {
    Snapshot() []Worker
    Watch(ctx context.Context) <-chan Event
    Lookup(nameOrPane string) (Worker, bool)  // by name (alias), by pane id, by subject — caller's choice
}
```

### CLI (orch-peek refactored)

```
orch-peek                          # all workers, table format
orch-peek --json                   # JSON dump for piping
orch-peek --watch                  # follow mode (event stream)
orch-peek <name|pane>              # specific worker
orch-peek --since 5m               # workers last seen within 5m
```

Same as today's `orch-peek` but driven by the new registry under the hood.

### Sidecar (optional)

```
orch-registry serve                # publishes orch.registry.snapshot subject on NATS
                                   # consumers (UI, dashboards) subscribe instead of polling
```

The sidecar is OPTIONAL — Go consumers use the in-process registry; non-Go / cross-process consumers can either:
- shell out to `orch-peek --json` (poll)
- subscribe to `orch.registry.snapshot` if the sidecar is running (push)

## Module layout

```
orch/
├── internal/
│   └── registry/
│       ├── registry.go       # types + interface
│       ├── join.go            # the actual join logic (sources → workers)
│       ├── sources/
│       │   ├── nats.go        # subscribes $SRV.INFO + agents.hb.>
│       │   ├── aliases.go     # reads ~/.config/orch-aliases
│       │   ├── operator.go    # reads ~/.cache/orch-operator.json
│       │   └── shim_logs.go   # optional, ad-hoc diagnostic fields
│       └── registry_test.go
├── cmd/
│   └── orch-registry/         # optional sidecar binary
│       └── main.go
└── bin/
    └── orch-peek              # rewritten thin wrapper over internal/registry
```

## Migration plan

### Step 1: Define registry package + types

1. Create `internal/registry/` with the public Go types
2. Implement source readers (NATS, alias file, operator marker)
3. Implement the join logic
4. Unit tests: feed mock source data, assert correct join output

### Step 2: Migrate `orch-peek` to use registry

1. `bin/orch-peek` (currently bash) ports to Go OR keeps bash but shells out to `orch-registry --json`
2. Smoke test: outputs same shape as before; consumers unaffected

### Step 3: Migrate other consumers

1. `orch-tell` looks up target worker via registry instead of its separate file-based lookup
2. `orch-ask` same
3. `orch-spy` same (for finding observer targets)
4. UI updates to consume `orch-peek --json --watch` OR `nats sub orch.registry.snapshot` if sidecar is running

### Step 4: Sidecar (optional)

1. New `cmd/orch-registry/` binary that runs the registry continuously, publishes snapshots to `orch.registry.snapshot.*`
2. Useful for the UI to skip the orch-peek subprocess + JSON parse per refresh
3. install.sh adds an optional launchd / systemd entry

## What changes for operators

- `orch-peek` output unchanged (same table format)
- New: `orch-peek --json --watch` for live tail
- New: aliases.json reads are auto-picked-up by orch-tell / orch-ask (no need to re-source)
- UI starts showing `lead-engineer` (from metadata.session) instead of `pct64`

## What changes for the codebase

- Five source files become one read interface
- Tests for "what does the operator see" become tests against the registry — no more mocking NATS + aliases + operator marker per test
- New consumers (Go or CLI) get the join for free
- The bug surfaced during the UI demo (orch-tell saying "not registered on the bus" when bus shows the worker IS there) becomes impossible — there's one truth

## Backwards compatibility

- All existing sources preserved (registry consumes them, doesn't replace)
- `orch-peek`'s default output shape unchanged
- `~/.config/orch-aliases` continues to work as today

## Acceptance criteria

- [ ] `internal/registry/` package with full Go type + source readers + join + tests
- [ ] `orch-peek` rewritten to use registry; output unchanged
- [ ] `orch-tell` and `orch-ask` use registry for target resolution; "not registered" errors disappear
- [ ] `orch-spy` uses registry
- [ ] Optional: `orch-registry` sidecar binary
- [ ] UI consumer documentation: how to subscribe (poll `orch-peek --json` OR sub `orch.registry.snapshot`)
- [ ] All existing bench assertions still pass
- [ ] New tests: registry join correctness (mock sources → expected Worker list)

## Decisions deferred to design phase

1. **Where does the registry live after Proposal 0001?** If the shim moves to `synadia-agent-shim`, the registry stays in orch (it composes orch-specific sources like the alias file and operator marker). Confirm.
2. **Sidecar default-on or default-off?** Lean: optional, enabled by `--with-sidecar` in install.sh.
3. **Watch semantics**: bounded buffer + drop oldest, or unbounded + risk OOM? Lean: bounded (256) + drop oldest with stderr warning.
4. **Stale worker definition**: how long after last heartbeat is a worker "departed"? Lean: 3× interval (default 90s).
5. **Cross-process synchronization**: if two orch CLIs run concurrently, do they each maintain their own registry instance, or share one? Lean: independent instances for v1 (NATS subscriptions are cheap); reassess if heartbeat fan-out becomes a load issue.

## Risks

- **Source drift**: if any source schema changes (e.g., aliases file gets a new column), registry needs updating. Mitigation: version the alias file; registry reads version-tagged sources gracefully.
- **Latency on first read**: registry's first snapshot requires sub'ing to NATS + waiting for $SRV.INFO replies. Mitigation: warm cache by sub'ing on construction; lazy first read blocks briefly.
- **Bug: orch-peek showing 5 workers while bus has 3**: registry must correctly remove departed workers. Tests must cover heartbeat-stops-firing → worker eventually marked departed.

## Effort estimate

~1 week:
- Days 1-2: types + source readers + join + tests
- Day 3: orch-peek migration
- Day 4: orch-tell / orch-ask / orch-spy migration
- Day 5: optional sidecar + UI docs
