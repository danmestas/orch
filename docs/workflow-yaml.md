# Workflow YAML reference

This page documents the YAML grammar accepted by `orch workflow`. It tracks
the locked decisions in [Proposal 0007](proposals/0007-workflow-yaml-compiled-to-task-dag.md).

> **Phase B status.** The full apply/status/cancel surface is now
> wired. The validator + diagnostic `compile --print` from Phase A
> still ship; `apply` seeds the compiled DAG into a sesh scope via
> `sesh-ops`, `status` aggregates per-node state from the same scope,
> and `cancel` flips every pending/blocked task to cancelled (running
> tasks are left alone — killing pullers is orch-spawn territory,
> tracked at #180).

## Top-level shape

```yaml
name: build-feature                 # required, unique within a scope
description: "human-readable blurb" # optional
scope-id: e2ecafe1                  # optional; resolved at apply time
nodes:                              # required, at least one
  - id: plan
    prompt: "Explore the codebase"
    assign: lead-engineer
```

Unknown top-level keys are rejected at parse time — typos like `nodez:`
fail loudly instead of being silently ignored.

## Node kinds

A node must declare **exactly one** kind discriminator. The validator
enforces this (`missing-kind` and `multiple-kind` codes).

| Kind        | YAML key       | Body                                    |
| ----------- | -------------- | --------------------------------------- |
| Prompt      | `prompt:`      | string — the AI prompt                  |
| Bash        | `bash:`        | string — shell command                  |
| Script      | `script:`      | `{name, runtime?, args?, env?}`         |
| Command     | `command:`     | `{name, args?}` (refs `.orch/commands/`) |
| Loop        | `loop:`        | `{prompt, until, max_iterations?, fresh_context?}` |
| Approval    | `approval:`    | `{prompt, until?}` — interactive gate   |
| Spawn       | `spawn:`       | SpawnSpec (orch#141 — Phase B forwards) |

## Modifiers

These attach to any node:

| Field           | Meaning                                                                |
| --------------- | ---------------------------------------------------------------------- |
| `id`            | Required identifier — alnum/underscore/dash, must start with letter or underscore |
| `depends_on`    | List of node IDs that must complete before this node runs             |
| `when`          | Predicate string (evaluated at pull time; not parsed in Phase A)       |
| `trigger_rule`  | `all_success` (default) / `any_success` / `all_done`                   |
| `timeout`       | Per-node wall clock                                                    |
| `idle_timeout`  | Per-node "stuck" detection                                             |
| `assign`        | Named worker to route this task to                                     |

## Variable substitution — mixed-time

Per the locked decision in Proposal 0007 §"Decisions deferred to design
phase #1", each reference resolves at the phase where its data is
available:

| Ref shape                             | Category    | Resolved at  |
| ------------------------------------- | ----------- | ------------ |
| `$ENV.NATS_URL`                       | env         | compile time |
| `$WORKFLOW.name` / `.scope_id`        | static      | compile time |
| `$plan.output`                        | node        | pull time    |
| `$plan.output.json.path.here`         | node + path | pull time    |

`compile --print` resolves env + static refs into the task description
and lists each pull-time ref in the task's `pull_refs[]` array. The
puller (or runtime resolver) substitutes pull-time refs against the
upstream task's `result` field when claiming the task.

Escape a `$` reference with a leading backslash if you want it as
literal text: `\$plan.output`.

## Compile-time validation rules

`orch workflow validate <file>` runs the same checks as `apply` and
exits non-zero on any error-severity diagnostic. Use it in CI.

| Code                       | Severity | Trigger                                                              |
| -------------------------- | -------- | -------------------------------------------------------------------- |
| `missing-workflow-name`    | error    | top-level `name:` absent                                             |
| `missing-id`               | error    | a node has no `id:`                                                  |
| `duplicate-id`             | error    | two nodes share an `id:`                                             |
| `invalid-identifier`       | error    | `id:` violates the identifier grammar                                |
| `missing-kind`             | error    | a node has no kind discriminator                                     |
| `multiple-kind`            | error    | a node sets >1 kind discriminator                                    |
| `missing-subfield`         | error    | a node kind's required body field is empty (e.g. `loop.prompt`)      |
| `unknown-dependency`       | error    | `depends_on:` lists a node id that isn't declared                    |
| `cycle`                    | error    | the depends_on graph contains a cycle                                |
| `dangling-ref`             | error    | a `$nodeId.output` interpolation references an undeclared node       |
| `unreachable`              | error    | a node's transitive depends_on closure can never start               |
| `unknown-assign-target`    | error    | `assign:` references a worker that is neither a declared `spawn:` node nor in the supplied fleet (`--fleet` / `WithFleet`) |
| `json-path-on-non-json`    | warning  | a node indexes JSON path into a `bash:` node's stdout                |

A workflow is **valid** iff no error-severity diagnostics are reported.
Warnings do NOT invalidate.

### Why is the validator the contract test?

Per the Ousterhout review (`REVIEW-ousterhout.md`): the validator IS
the public interface — making invalid workflows unrepresentable at
submission is the whole proposition. The acceptance criterion isn't
"63 tests pass against the implementation"; it's "every error code
listed above has a canonical failing fixture, and every passing case
the spec mentions parses + validates clean." See
[internal/workflow/validate_test.go](../internal/workflow/validate_test.go).

## CLI

```sh
# Validate without compiling
orch workflow validate workflows/build-feature.yaml
orch workflow validate workflows/build-feature.yaml --fleet lead-engineer,verifier

# Show the planned task DAG as JSON (diagnostic; no sesh writes)
orch workflow compile --print workflows/build-feature.yaml

# Phase B — seed compiled tasks into a sesh scope (idempotent)
orch workflow apply workflows/build-feature.yaml --session bench-fleet
orch workflow apply workflows/build-feature.yaml --session bench-fleet \
  --scope-id e2ecafe1                  # override workflow.scope-id

# Phase B — live progress
orch workflow status build-feature --session bench-fleet --scope-id e2ecafe1

# Phase B — cancel all pending/blocked tasks (in-flight tasks untouched)
orch workflow cancel build-feature --session bench-fleet --scope-id e2ecafe1
```

`apply`/`status`/`cancel` shell out to the `sesh-ops` binary on
`$PATH`. The `--server`, `--session`, `--scope`, and `--scope-id`
flags forward verbatim to sesh-ops so resolution rules are inherited
(see `sesh-ops --help`).

### Idempotency model

`apply` is the workhorse: it compiles the YAML, lists every task in
the targeted scope, and reconciles against the compiled plan. The
lookup key for "do I already have this task?" is the triple
`(workflow.name, node.id, fingerprint)` — the fingerprint is a
SHA-256 over the node body (kind + description + deps + assign +
pull-refs), so any body change flips the fingerprint and gets a
fresh sesh task:

- **Same triple already exists** — task is left in place. Apply
  reports `unchanged`.
- **Different fingerprint** — fresh sesh task is created with a new
  ULID; the old record is left alone (per the proposal's
  content-addressed-task model). Run `orch workflow cancel` if you
  want to retire stale pending work. Apply reports `created`.
- **No existing record** — a new task is created. Apply reports
  `created`.

Apply also ensures a per-workflow sesh goal exists and links every
applied task to it. The goal is metadata-tagged
(`owner=orch-workflow`, `metadata.orch_workflow_goal=true`) so
operators querying `sesh-ops goal list` can tell it apart from their
own goals.

### Cancel semantics

`cancel` looks up the workflow's anchor goal in the targeted scope
and invokes `sesh-ops goal cleanup-tasks`. cleanup-tasks CAS-flips
every linked task currently in `pending` to `cancelled`. Tasks in
`in_progress`, `blocked`, or any terminal status are **deliberately
left alone** — sesh-ops's public surface only cancels pending
records, and killing pullers is orch-spawn territory (issue #180).
The cancel report calls out the skipped set so operators know what's
still in flight.

Cancel against a workflow id that was never applied is a clean
no-op: the report comes back empty and no goal is created.

Exit codes:

- `0` — success
- `1` — parse / IO / sesh-ops error
- `2` — workflow is invalid (one or more error-severity diagnostics)
- `3` — reserved (unused now that all subcommands are wired)

## Reference workflow

See [`workflows/build-feature.yaml`](../workflows/build-feature.yaml).
It mirrors the example in Proposal 0007 and is exercised by
`internal/workflow/validate_test.go` so the spec example and the
validator never drift.
