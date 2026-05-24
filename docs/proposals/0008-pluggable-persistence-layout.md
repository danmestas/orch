# Proposal 0008 — Decouple persistence engine + layout engine from comms (tmux → pluggable)

**Status:** Phase A landed. Phase B (cmux backend) pending. See [§ Status](#status).
**Depends on:** Proposal 0009 / issue #181 (stable slug as worker identity — landed as `aa68ba3`).
**Blocks:** cmux integration; future libghostty / zmx / headless-noop layout engines.

**Ousterhout-review adjustments (2026-05-19, issue [#180 comment](https://github.com/danmestas/orch/issues/180#issuecomment-4489183265)):**

- **Slug-as-identity work split out** into Proposal 0009 (issue #181) — shippable independently with its own bug ledger.
- **Initial backend targets narrowed to tmux + cmux.** zmx / libghostty / systemd / none-at-all are future possibilities, not proposal targets. Don't shape an interface for unproven consumers.
- **InstanceHandle is the cross-engine contract** — without it, both LayoutEngine and the shim would leak persistence-engine internals (TTY shape, pid, watchdog mechanism). The handle owns ID, Locator, Wait, Kill.
- **Composition table is a closed registry**, validated at flag-parse — invalid combos are unrepresentable, not runtime-errored.

## Status

- **2026-05-23 — Phase A landed.** Go interfaces in `internal/instance`, `internal/persistence`, `internal/layout` define the seam. Tmux reference impl in `internal/persistence/tmux/` (wraps `executors/tmux/spawn.sh` via `os/exec` — gradual cutover per Decision 5 default in #142 brief) and `internal/layout/tmux/` (post-spawn pane title + alias-file write, mirroring the inline bash logic that previously lived in `bin/orch-spawn` lines 748-805). New `cmd/orch-engines` binary exposes `validate <persistence> <layout>` for bash callers; `bin/orch-spawn` now grows `--persistence` / `--layout` flags (defaults: `tmux` / `tmux`) and shells out to `orch-engines validate` at flag-parse time. Closed composition registry: `{tmux, tmux}` only. Phase A does NOT change observable behavior for the default invocation — the engine layer is the seam, not yet the hot path.
- **Pending — Phase B (cmux backend).** `internal/persistence/cmux/` + `internal/layout/cmux/` register `{cmux, cmux}` in the composition table. End-to-end test: `orch-spawn --persistence cmux --layout cmux` spawns a worker, NATS round-trip works. Cross-engine pairs stay rejected.
- **Pending — Phase C (cutover).** `bin/orch-spawn` invokes `cmd/orch-engines spawn` (a new subcommand) that drives `persistence.Start → layout.Spawn` end-to-end in Go; the inline bash slug-labeling block deletes. Until then, the labels are written twice (once by bash inline, once available via the Go layout engine for downstream consumers) — harmless idempotent overlap.

## Two orthogonal axes (which tmux/cmux currently bundle)

**Persistence engine** — keeps an agent's process alive across operator disconnects; owns the PTY.

- Today: tmux sessions
- Next: cmux sessions
- Future (out of scope here): zmx, screen, systemd-run, custom NATS-aware supervisor

**Layout engine** — arranges agent UI surfaces in the operator's view (split panes, windows, tabs).

- Today: tmux windows+panes
- Next: cmux's layout CLI
- Future (out of scope here): libghostty, Zellij, headless-no-op

## Why split persistence from layout

Today's coupling is the bug: tmux conflates them so you can't use cmux persistence with tmux layout, or layout-only without persistence. Splitting them is what earns the abstraction depth — depth comes from real composition needs, not hypothetical engines.

## The abstraction surface

```go
// internal/instance/handle.go
type Handle interface {
    ID() string         // stable slug (Proposal 0009)
    Locator() string    // engine-native locator (pane id for tmux, session id for cmux)
    Wait() error        // blocks until worker exits
    Kill() error        // graceful then forceful; idempotent
}

// internal/persistence/engine.go
type Engine interface {
    Name() string
    Start(spec StartSpec) (instance.Handle, error)
    Attach(slug string) (instance.Handle, error)
    List() ([]instance.Handle, error)
}

// internal/layout/engine.go
type Engine interface {
    Name() string
    Spawn(spec SpawnSpec, h instance.Handle) error
    Arrange(preset string) error
    Close(slug string) error
}

// orch-spawn composes them via the handle:
//   instance := persistence.Start(spec)         // owns the PTY
//   surface  := layout.Spawn(spec, instance)    // attaches UI to instance
//   shim     := startShim({instance: instance.ID(), watch: instance.Wait})
//
// Shim's identity = instance.ID() (per #181); shim's lifecycle = instance.Wait().
// Comms = NATS only. Layout has zero comms responsibility.
```

CLI shape (only the two real targets):

```sh
orch-spawn claude --persistence cmux --layout cmux --slug lead-engineer
orch-spawn codex  --persistence tmux --layout tmux --slug api-worker
```

Default composition: `persistence=tmux, layout=tmux` (today's behavior).

## Closed-registry composition

Per the Ousterhout review: a free Cartesian product over (persistence × layout) is invitation-to-ambiguity. Instead, the composition table is a **closed registry** validated at flag-parse time, owned by `internal/persistence/registry.go`:

| Persistence | Layout | Supported | Notes |
|---|---|---|---|
| tmux | tmux | yes | today's default; landed Phase A |
| cmux | cmux | future | unified cmux deployment (Phase B target) |
| tmux | cmux | no | rejected — cross-engine attach requires a forwarder we're not building |
| cmux | tmux | no | same |
| any | none | future | true-headless mode; deferred until a real CI consumer pulls it in |
| none | any | no | nonsense — layout needs a PTY source |

Cross-engine combinations require explicit support code in each pair; rejected by default until that work is done. The `bin/orch-spawn` flag-parse layer calls `orch-engines validate <p> <l>`; non-zero exit terminates the spawn before any tmux/spawn work runs.

## Coupling points that moved (Phase A) and remain (Phase C)

| Today | After Phase A | After Phase C |
|---|---|---|
| `orch-spawn` shells `tmux split-window` directly | engines defined; `executors/tmux/spawn.sh` still on hot path (gradual cutover) | `persistence.Start` invokes spawn.sh via os/exec; `layout.Spawn` is called by orch-engines |
| Inline pane title / alias write in bash | mirrored in `internal/layout/tmux` (Go) — both layers coexist | bash inline labels deleted; Go layout engine is the only writer |
| `orch-agent-shim --pane %N` ties shim lifecycle to tmux pane | unchanged (Decision 6 — shim wire format does NOT change this PR) | Phase D: `--watch-handle <type>:<id>` (resolved by the persistence engine) |
| Hardcoded "tmux" assumption in `~/.config/orch-aliases` resolver | layout engine writes the file; resolver still inline bash | aliases resolve via active layout engine's `Lookup(slug)` |

## Compatibility plan

Backwards-compat for the default (tmux-tmux) composition: behavior identical to today. Only operators opting into `--persistence cmux` or `--layout cmux` exercise new code paths.

Internal refactor of `orch-spawn` to call the interfaces (instead of shelling tmux directly) is invisible to operators — Phase C completes this and removes the duplicated inline bash labeling.

## Implementation slicing

**Phase A — interfaces + tmux reference impl** *(landed 2026-05-23)*

Defined Go interfaces in `internal/instance`, `internal/persistence`, `internal/layout`. Tmux reference impl wraps `executors/tmux/spawn.sh` (Decision 5 gradual cutover). New `cmd/orch-engines validate` subcommand owns the closed composition table; `bin/orch-spawn` shells out at flag-parse. Zero externally-visible behavior change for the default invocation. Contract tests cover happy path + composition rejection + mock-handle Wait/Kill semantics.

**Phase B — add cmux backend** *(next)*

Implement `internal/persistence/cmux/` and `internal/layout/cmux/` against the same interfaces. Wire into the closed composition registry as `cmux+cmux`. End-to-end test: spawn a worker with `--persistence cmux --layout cmux`, drive via NATS, observe responses.

**Phase C — cutover** *(after Phase B)*

`bin/orch-spawn` invokes `orch-engines spawn <agent>` instead of running its own dispatch+label block. The bash side becomes the flag parser + suit-prepare driver + shim launcher; everything between the spawn-script call and the shim launch routes through Go.

**Phase D — shim wire change** *(future)*

`orch-agent-shim --watch-handle <type>:<id>` replaces `--pane %N`, so the shim is engine-agnostic. Out of scope for Proposal 0008 — slated for a follow-up.

(Future phases — zmx, libghostty, headless-none — earn entry to the registry when they have a real consumer.)

## Acceptance (umbrella)

Closeable when:

- [x] Phase A merged: tmux moved behind the two interfaces; tests prove identical behavior; closed composition registry rejects invalid combos at flag-parse.
- [ ] Phase B merged: cmux backend lands; cross-engine smoke test in bench (`--persistence cmux --layout cmux`).
- [ ] InstanceHandle is the only cross-engine contract (no TTY/PID leaks).
- [ ] Migration guide for operators (when to use which engine; defaults).

## Open questions

1. ~~Does the LayoutEngine need to know about persistence to attach a Surface to an existing Instance, or is the Surface just a TTY consumer that doesn't care?~~ **Resolved:** `InstanceHandle.Locator()` (and a future `TTY()` accessor) is what LayoutEngine binds to. Layout doesn't see persistence internals.
2. `LayoutEngine` in true-headless mode: deferred. Per Ousterhout review's open question — punted until a real headless consumer (CI fleet, server-side runner) forces the design. Until then `layout=none` is rejected as ambiguous.
3. Does the shim still attach to a process, or does the PersistenceEngine become the shim's "what to watch"? **Resolved:** shim's lifecycle = `go handle.Wait()`. The tmux-pane-watchdog (orch#167 fix) becomes the tmux-specific implementation of `Wait()`. Wire change deferred to Phase D so this PR doesn't touch the shim repo.
4. ~~Where do hybrid compositions land (e.g., persistence=tmux, layout=libghostty)?~~ **Resolved:** closed registry rejects them until explicit cross-engine support is built. No accidental ambiguity.

## Related

- #181 / Proposal 0009 — stable slug as worker identity. Landed `aa68ba3`. Prerequisite for clean ID() semantics.
- #142 / Proposal 0003 — executor backends. Similar pattern (heavyweight engines as sister repos). cmux could land that way too. The hybrid `resolve_executor` discovery (#200) already covers the executor seam; this proposal covers the orthogonal persistence/layout split.
- #167 — orphaned shims (closed). The pane-watchdog fix generalizes to the `handle.Wait()` interface.
- assume-orch skill — recognition that "Pane ids change every recycle" is the fact-of-life this proposal challenges. Slug-as-identity (Proposal 0009) supersedes that fact for operator-facing flows.
