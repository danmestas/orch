# SpawnSpec versioning policy

How wire-format changes to [`SpawnSpec` and `WorkerHandle`](./spawn-spec.md) are
versioned. Schema lives at `dist/schema/spawn-spec.v1.json` and
`dist/schema/worker-handle.v1.json`; Go types are canonical
(`internal/spawnspec/`).

## Status

- `v1` is GA. The schema is frozen.
- `v2` is GA. It is additive over v1: adds `cmux` + `zmx` to the executor
  enum (both for `SpawnSpec` and `WorkerHandle`), introduces the `CmuxBlock`
  and `ZmxBlock` discriminator blocks, and widens the tmux block's `layout:`
  enum to accept `none` (only valid paired with `executor: zmx` per the
  Proposal 0008 composition table).
- v1 and v2 are accepted indefinitely. orch-spawn's parser routes on the
  document's `spec_version:` field; a document without `spec_version:`
  defaults to v1 (back-compat).

## Stability contract

- `v1` is immutable. The published JSON Schemas
  (`dist/schema/spawn-spec.v1.json`, `dist/schema/worker-handle.v1.json`) will
  not change.
- Tooling MAY pin to `v1` indefinitely. orch-spawn will accept `spec_version:
  v1` documents for the lifetime of the project.
- Mirrors the Kubernetes GA stability promise: once a versioned shape ships,
  it is a forever shape.

## What triggers a v2 bump

Any wire-format change. Specifically:

- Adding a field (even optional).
- Removing or renaming a field.
- Changing a validation rule (e.g. relaxing the DNS-label constraint on
  `name`, broadening the env-key regex, allowing two executor blocks).
- Opening the `agent:` enum to discovered/plugin-provided values.
- Adding multi-spawn syntax (`spawns:`, `depends_on`, `$nodeId.output`
  substitution).
- Adding a new executor discriminator block (`tmux:`, `cf-worker:`,
  `cf-durable-object:`, ...) — adding a block changes the schema's `oneOf`.

If it shows up in `dist/schema/*.json`, it is a wire change. Wire change → new
version.

## What does NOT trigger a v2 bump

- Edits to `description:` tags inside Go struct fields.
- Validation error-message rewording (the error MUST still cite the same
  rule).
- Internal `internal/spawnspec/` Go refactors that do not change the
  marshalled YAML/JSON shape.
- Adding documentation, examples, or tests.

## What landed in v2

- New `executor` enum values: `cmux`, `zmx`. Mirrors the persistence-engine
  registry (Proposal 0008 Phase B + Phase 2). v1 binaries reject these
  values; v2 binaries accept them.
- New discriminator blocks: `cmux:` (parallel to `tmux:`, surface-based
  multiplexer), `zmx:` (sessions-only, no in-session subdivision).
- Tmux block `layout:` enum gains `none`. Only valid paired with
  `executor: zmx`; the validator rejects `tmux: { layout: none }` because
  the marker "no in-pane layout" only makes sense for the zmx engine.
- The published schemas are `dist/schema/spawn-spec.v2.json` and
  `dist/schema/worker-handle.v2.json`. Both are added to the CI drift gate
  alongside the v1 files.

## Still reserved for a future version

The following are explicitly out of scope for both `v1` and `v2`:

- **Agent registry / plugin discovery** — opens the `agent:` enum. Rides
  on [Proposal 0003](./proposals/0003-extract-executor-backends.md) (executor
  backend extraction).
- **Multi-spawn workflows** — `spawns:` list, `depends_on`, `when:`,
  `$nodeId.output[.json.path]` substitution. Lands when the workflow
  compile work (Proposal 0007 / issue #146) ships.
- **Archon-grammar extensions** for workflow compile — per-node `timeout`,
  `idle_timeout`, `trigger_rule`, and other node-level controls that
  generalise the single-spawn shape into a DAG.
- **Dead-field cleanup** on `WorkerHandle.Abort` (AbortKind/Verb/Keys) —
  deferred to a v3 bump or a non-version cleanup PR so v2 stays purely
  additive.

## Migration policy when v2 arrives

- v1 acceptance is indefinite. orch-spawn will not exit 2 on a v1 document
  just because v2 exists.
- v1 and v2 documents are dispatched side-by-side; the `spec_version:` field
  selects the parser.
- Single-version gates are ops-fragile (one tool upgrade ahead of the
  fleet shouldn't break spawns); we do not adopt them.
- Deprecation, if it ever happens, is announced via an ADR with a fixed
  sunset date. No silent breakage.

## Drift enforcement

- A CI gate (issue #141a) regenerates `dist/schema/*.json` from the Go
  types on every PR and fails if the working tree diverges. The gate
  covers v1 (`spawn-spec.v1.json`, `worker-handle.v1.json`) and v2
  (`spawn-spec.v2.json`, `worker-handle.v2.json`).
- Schema regeneration is `go run ./cmd/spawnspec-schema -out dist/schema`.
  The same command writes all four files (v1 + v2).
