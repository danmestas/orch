# Proposal 0008 — Decouple persistence engine + layout engine from comms (tmux → pluggable)

**Status:** Phase A + Phase B landed; interfaces deferred to Rule-of-Three. See [§ Status](#status).
**Depends on:** Proposal 0009 / issue #181 (stable slug as worker identity — landed as `aa68ba3`).
**Blocks:** future zmx / libghostty / headless-noop engines.

**Ousterhout-review adjustments (2026-05-19, issue [#180 comment](https://github.com/danmestas/orch/issues/180#issuecomment-4489183265)) — and the follow-on 2026-05-23 design call:**

- **Slug-as-identity work split out** into Proposal 0009 (issue #181) — shippable independently with its own bug ledger.
- **Initial backend targets narrowed to tmux + cmux.** zmx / libghostty / systemd / none-at-all are future possibilities, not proposal targets. Don't shape an interface for unproven consumers.
- **Composition table is a closed registry**, validated at flag-parse — invalid combos are unrepresentable, not runtime-errored.
- **Engine / Handle / LayoutEngine interfaces deferred** (2026-05-23 call). The initial Phase-A scaffolding introduced them speculatively against a single concrete engine; PR #189C / #206 dropped that scaffolding and chose inline-with-switch — closer to the Rule of Three. The composition table remains as the dispatch authority. When a third engine (zmx is the named future candidate) arrives, that's the natural moment to extract `Engine` + `Handle` + `LayoutEngine` from the three concrete impls.

## Status

- **2026-05-23 — Phase A landed, then re-shaped.** The original Phase A PR (#203 / commit 98ea932) introduced `internal/instance`, `internal/persistence`, `internal/layout` interface packages plus separate tmux impl packages — interfaces shaped against one concrete consumer. Per the 2026-05-23 Ousterhout-call follow-up, PR #189C / #206 (`feat(orch): inline orch-spawn + executors into Go`) dropped the interface packages and inlined the tmux spawn path directly into `cmd/orch/spawn_tmux.go`. The surviving Phase-A surface is `internal/persistence/registry.go` — the closed composition table — and `cmd/orch-engines` (the `validate` / `list` probe binary). One engine in the codebase → no abstraction earned yet (Ousterhout: deep modules from real composition, not hypotheticals).
- **2026-05-24 — Phase B landed (issue #207 / this proposal's cmux delivery).** `cmd/orch/spawn_cmux.go` mirrors `spawn_tmux.go`'s inline shape — `cmux new-pane` + `cmux send` instead of `tmux split-window`. Composition table grows `{cmux, cmux}`. `cmd/orch/spawn.go` switches on `opts.Persistence` (no `Engine` interface) to pick the concrete spawn function. `validateComposition` still gates the pair at flag-parse via the `internal/persistence` registry, so `--persistence=tmux --layout=cmux` (and the reverse) reject cleanly before any spawn work. Verify (`--verify`) is not yet implemented for cmux — operators get a clear error rather than a silent skip.
- **Deferred — interface extraction.** Earns its keep when a third concrete engine lands. zmx (a Zig session-persistence tool at `~/references/zmx`) is the named candidate. zmx is sessions-only, with no native pane-splits, so when it arrives the layout-engine concept must accommodate "single-pane / no layout" — that's the discovery moment to commit to the `Engine` / `Handle` / `LayoutEngine` shape across the three impls, instead of guessing at it now.
- **Deferred — Phase C (shim wire-format change).** Today's shim accepts both `%64`-style and `surface:30`-style locators because it passes the string through unchanged; a future `--watch-handle <type>:<id>` makes that explicit. Out of scope here.
- **Deferred — Phase D (interface extraction).** Re-evaluated when zmx or another third engine arrives. See Implementation slicing below.

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

## The abstraction surface (current shape — inline, no interfaces)

Per the 2026-05-23 Ousterhout call: with one concrete engine in tree, the `Engine` / `Handle` / `LayoutEngine` interfaces were shape-against-one-consumer — speculation, not earned depth. The current shape is inline-with-switch:

```go
// cmd/orch/spawn.go (dispatch — the actual seam)
switch opts.Persistence {
case "tmux":
    paneID, spawnRC, err = opts.spawnPane()       // -> cmd/orch/spawn_tmux.go
case "cmux":
    paneID, spawnRC, err = opts.spawnPaneCmux()   // -> cmd/orch/spawn_cmux.go
default:
    return fmt.Errorf("orch spawn: no spawn implementation for persistence=%q", opts.Persistence)
}

// internal/persistence/registry.go — the closed composition table
// is what guards the switch. validateComposition() rejects mixed pairs
// at flag-parse before the switch ever runs.
var supportedPairs = map[Pair]struct{}{
    {Persistence: "tmux", Layout: "tmux"}: {},
    {Persistence: "cmux", Layout: "cmux"}: {},
}
```

Each concrete spawn file owns its engine's vocabulary directly: `spawn_tmux.go` calls `tmux split-window` / `tmux select-pane`, `spawn_cmux.go` calls `cmux new-pane` / `cmux send`. The pane locator returned to the caller is engine-native (`%64` for tmux, `surface:30` for cmux); the slug→locator alias file (`~/.config/orch-aliases`) stays the same shape. The shim wire format (`--pane <locator>`) is unchanged.

### Future shape — when zmx lands

When the third engine (zmx — sessions-only, single-pane) arrives, the Rule of Three is satisfied and extracting interfaces becomes profitable:

```go
// internal/instance/handle.go (PROPOSED — after zmx lands)
type Handle interface {
    ID() string       // stable slug (Proposal 0009)
    Locator() string  // engine-native locator
    Wait() error      // blocks until worker exits
    Kill() error      // graceful then forceful; idempotent
}

// internal/persistence/engine.go (PROPOSED)
type Engine interface {
    Name() string
    Start(spec StartSpec) (Handle, error)
}

// LayoutEngine acquires a Layout() string method so zmx can answer
// "single-pane / no layout" without faking a split direction.
```

The composition table remains the dispatch authority either way — the interface lift is an internal refactor, not a registry change.

CLI shape:

```sh
orch spawn claude --persistence cmux --layout cmux --slug lead-engineer
orch spawn codex  --persistence tmux --layout tmux --slug api-worker
```

Default composition: `persistence=tmux, layout=tmux` (today's behavior).

## Closed-registry composition

Per the Ousterhout review: a free Cartesian product over (persistence × layout) is invitation-to-ambiguity. Instead, the composition table is a **closed registry** validated at flag-parse time, owned by `internal/persistence/registry.go`:

| Persistence | Layout | Supported | Notes |
|---|---|---|---|
| tmux | tmux | yes | today's default; landed Phase A |
| cmux | cmux | yes | unified cmux deployment; landed Phase B (issue #207) |
| tmux | cmux | no | rejected — cross-engine attach requires a forwarder we're not building |
| cmux | tmux | no | same |
| zmx  | zmx  | future | sessions-only — earns entry when a real consumer pulls it in |
| any  | none | future | true-headless mode; deferred until a real CI consumer pulls it in |
| none | any  | no | nonsense — layout needs a PTY source |

Cross-engine combinations require explicit support code in each pair; rejected by default until that work is done. `cmd/orch spawn`'s flag-parse layer calls `validateComposition`, which either invokes `orch-engines validate <p> <l>` (when the binary is on PATH or under `$ORCH_ENGINES_BIN`) or falls back to the in-tree `go run ./cmd/orch-engines`. Non-zero exit terminates the spawn before any pane / surface work runs.

## Coupling points that moved and remain

| Concern | Pre-Proposal | After PR #206 (inline) + Phase B (cmux) | After interface extraction (when zmx lands) |
|---|---|---|---|
| `orch-spawn` shells `tmux split-window` directly | bash invoking tmux | `cmd/orch/spawn_tmux.go` (Go inline) + `cmd/orch/spawn_cmux.go` (Go inline); `spawn.go` dispatches by `opts.Persistence` | concrete spawn files implement a shared `persistence.Engine` interface |
| Inline pane title / alias write in bash | bash | inlined into Go `labelSlug` (used by both tmux and cmux paths) | folded into a `LayoutEngine.Label(handle, slug)` method |
| `orch-agent-shim --pane <locator>` ties shim lifecycle to a tmux pane id | tmux-only | shim accepts both `%64`-style and `surface:30`-style locator strings (wire format unchanged; just passes the string through) | Phase D: `--watch-handle <type>:<id>` resolved by the persistence engine |
| Hardcoded "tmux" assumption in `~/.config/orch-aliases` resolver | bash | the alias file keys on locator strings regardless of engine; resolver works the same | aliases resolve via active engine's `Lookup(slug)` |

## Compatibility plan

Backwards-compat for the default (tmux-tmux) composition: behavior identical to today. Only operators opting into `--persistence cmux` or `--layout cmux` exercise new code paths.

The inline-with-switch dispatch in `cmd/orch/spawn.go` is invisible to operators: the public CLI is `orch spawn ... --persistence <name> --layout <name>` regardless of how the dispatch happens internally. When interfaces are extracted (Phase D), that's also an internal-only refactor.

## Implementation slicing

**Phase A — composition table + orch-engines probe** *(landed 2026-05-23, then re-shaped)*

The original PR (#203) shipped interface packages (`internal/instance`, `internal/persistence`, `internal/layout`) plus tmux impl packages. PR #189C / #206 dropped the interface scaffolding when collapsing `bin/orch-spawn` + `executors/tmux/spawn.sh` into Go — those packages had one consumer apiece and were speculative. What survived Phase A:

- `internal/persistence/registry.go` — the closed composition table + `RequirePair` + `SupportedPairs`.
- `cmd/orch-engines/` — `validate <p> <l>` and `list` probe subcommands. Stable surface for bash callers; calls into `internal/persistence`.
- `--persistence` / `--layout` flags on `orch spawn` (defaults: `tmux` / `tmux`).

Zero externally-visible behavior change for the default invocation.

**Phase B — cmux engine (inline, no interfaces)** *(landed 2026-05-24, issue #207)*

`cmd/orch/spawn_cmux.go` mirrors `spawn_tmux.go`'s shape: cmux's `new-pane` + `send` instead of tmux's `split-window`. Composition table registers `{cmux, cmux}` alongside `{tmux, tmux}`; mixed pairs (`{tmux, cmux}`, `{cmux, tmux}`) reject at flag-parse via the same registry. Dispatch is a `switch opts.Persistence` in `cmd/orch/spawn.go` — no `Engine` interface. Verify (`--verify`) and `--headless` are tmux-only for now; cmux returns a clean operator-facing error for both. Tests cover flag parsing, registry rejection of mixed pairs, cmux verb shape, and headless rejection.

**Phase C — shim wire change** *(future)*

`orch-agent-shim --watch-handle <type>:<id>` replaces `--pane %N`, so the shim is engine-agnostic. The shim today accepts both `%64`-style and `surface:30`-style locator strings unchanged — Phase C is the surface where the receiver learns the type explicitly.

**Phase D — interface extraction** *(when a third engine arrives)*

zmx is the named candidate (sessions-only, single-pane). When that lands, extract `Engine` + `Handle` + `LayoutEngine` (or just `Engine`, if `LayoutEngine` doesn't earn its keep) from the three concrete impls. Driven by the discovery that "I need to write this code three ways and the shapes are converging," not by a forecast.

(Future engines — libghostty, headless-none — earn entry to the registry when they have a real consumer.)

## Acceptance (umbrella)

Closeable when:

- [x] Phase A merged: closed composition registry + `orch-engines` probe binary; flag-parse rejects invalid combos. (Interface scaffolding subsequently dropped in #189C / #206 — see Status.)
- [x] Phase B merged (issue #207): cmux engine lands inline; composition table grows `{cmux, cmux}`; mixed pairs reject; `orch spawn --persistence cmux --layout cmux` spawns a worker in a cmux pane.
- [ ] Phase D — interface extraction — re-evaluated when zmx (or another third engine) earns entry to the registry.
- [ ] Migration guide for operators (when to use which engine; defaults).

## Open questions

1. ~~Does the LayoutEngine need to know about persistence to attach a Surface to an existing Instance?~~ **Re-opened by the Rule-of-Three deferral.** Today there is no `LayoutEngine` type — the layout work lives inline alongside each engine's spawn (pane title + alias write for tmux; the same alias write for cmux since `labelSlug` is locator-agnostic). When zmx forces the issue (sessions-only, no layout), the discovery moment will decide whether `LayoutEngine` is a separate interface or just a method on `Engine`.
2. `--verify` on cmux: deferred. `internal/tmuxctl.Verify` is tmux-specific. A `cmuxctl.Verify` (or generalized `engine.Verify(handle, agent)`) earns its keep when a cmux-using bench / operator workflow asks for it. Until then, `orch spawn --persistence cmux --verify` returns a clear error.
3. `--headless` on cmux: deferred. cmux has no headless-session concept; we reject explicitly rather than mis-spawn into a foreground surface.
4. `LayoutEngine` in true-headless mode: deferred. Per Ousterhout review's open question — punted until a real headless consumer (CI fleet, server-side runner) forces the design. Until then `layout=none` is rejected as ambiguous.
5. Does the shim still attach to a process, or does the PersistenceEngine become the shim's "what to watch"? Current shape: shim's lifecycle is the locator-watchdog (tmux pane-watch from orch#167; cmux equivalent TBD if it's a problem in practice). Wire-format change deferred to Phase C.
6. ~~Where do hybrid compositions land (e.g., persistence=tmux, layout=libghostty)?~~ **Resolved:** closed registry rejects them until explicit cross-engine support is built. No accidental ambiguity.

## Related

- #181 / Proposal 0009 — stable slug as worker identity. Landed `aa68ba3`. Prerequisite for clean ID() semantics.
- #142 / Proposal 0003 — executor backends. Similar pattern (heavyweight engines as sister repos). cmux could land that way too. The hybrid `resolve_executor` discovery (#200) already covers the executor seam; this proposal covers the orthogonal persistence/layout split.
- #167 — orphaned shims (closed). The pane-watchdog fix generalizes to the `handle.Wait()` interface.
- assume-orch skill — recognition that "Pane ids change every recycle" is the fact-of-life this proposal challenges. Slug-as-identity (Proposal 0009) supersedes that fact for operator-facing flows.
