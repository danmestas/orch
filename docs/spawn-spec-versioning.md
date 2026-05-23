# SpawnSpec versioning policy

How wire-format changes to [`SpawnSpec` and `WorkerHandle`](./spawn-spec.md) are
versioned. Schema lives at `dist/schema/spawn-spec.v1.json` and
`dist/schema/worker-handle.v1.json`; Go types are canonical
(`internal/spawnspec/`).

## Status

`v1` is GA. The schema is frozen.

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

## Reserved for v2

The following are explicitly out of scope for `v1` and will land together
under `v2`:

- **Agent registry / plugin discovery** — opens the `agent:` enum. Rides
  on [Proposal 0003](./proposals/0003-extract-executor-backends.md) (executor
  backend extraction).
- **Multi-spawn workflows** — `spawns:` list, `depends_on`, `when:`,
  `$nodeId.output[.json.path]` substitution. Lands when the workflow
  compile work (Proposal 0007 / issue #146) ships.
- **Archon-grammar extensions** for workflow compile — per-node `timeout`,
  `idle_timeout`, `trigger_rule`, and other node-level controls that
  generalise the single-spawn shape into a DAG.

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
  types on every PR and fails if the working tree diverges. This keeps the
  canonical Go structs and the published schema in lockstep.
- Schema regeneration is `go run ./cmd/spawnspec-schema -out dist/schema`.
