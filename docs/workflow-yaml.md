# Workflow YAML reference

This page documents the YAML grammar accepted by `orch workflow`. It tracks
the locked decisions in [Proposal 0007](proposals/0007-workflow-yaml-compiled-to-task-dag.md).

> **Phase A scope.** This package today ships the parser, the
> compile-time DAG validator, and a diagnostic `compile --print` that
> emits the planned task DAG as JSON. Apply / status / cancel land in
> Phase B once [orch#141](https://github.com/danmestas/orch/issues/141)
> (SpawnSpec) and [orch#145](https://github.com/danmestas/orch/issues/145)
> (Topology) merge.

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

# Show the planned task DAG as JSON (Phase A diagnostic)
orch workflow compile --print workflows/build-feature.yaml

# Phase B — not yet wired:
# orch workflow apply  workflows/build-feature.yaml --subtree bench-fleet
# orch workflow status build-feature --subtree bench-fleet
# orch workflow cancel build-feature --subtree bench-fleet
```

Exit codes:

- `0` — success (validate passed; compile succeeded)
- `1` — parse / IO error
- `2` — workflow is invalid (one or more error-severity diagnostics)
- `3` — subcommand not implemented in this phase

## Reference workflow

See [`workflows/build-feature.yaml`](../workflows/build-feature.yaml).
It mirrors the example in Proposal 0007 and is exercised by
`internal/workflow/validate_test.go` so the spec example and the
validator never drift.
