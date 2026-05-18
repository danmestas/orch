# Proposal 0007 — Workflow YAML (compiled to sesh task DAG)

**Status:** draft
**Depends on:** Proposal 0002 (SpawnSpec — workflow can include `spawn:` nodes), Proposal 0006 (topology — workflow targets a subtree's KV scope)
**Inspired by:** [coleam00/Archon](https://github.com/coleam00/Archon) workflow grammar

## Mental model — the key insight

Orch is **NOT** a workflow engine. Sesh's task model (with `depends_on`, CAS pull, completion events) is already a distributed workflow runtime.

Orch's workflow yaml is just a **compile target** — Archon-style grammar in, sesh task DAG out. orch compiles the yaml to task records, seeds them into the subtree's KV scope, then steps back. Workers pull tasks autonomously (already in sesh-ops). orch monitors via bus events.

```
   workflow.yaml           ── compile ──>          sesh tasks
   ┌──────────────────┐                     ┌─────────────────┐
   │ nodes:           │                     │  task: plan     │
   │   - id: plan     │                     │    deps: []     │
   │     prompt: ...  │                     │  task: implement│
   │   - id: implement│                     │    deps: [plan] │
   │     deps: [plan] │                     │  ...            │
   └──────────────────┘                     └─────────────────┘
                                                      │ orch seeds
                                                      ▼
                                            [sesh KV scope]
                                                      │ workers pull (CAS)
                                                      ▼
                                            [subtree workers execute]
                                                      │ completion events
                                                      ▼
                                            [orch monitors bus]
```

This means:
- **No new workflow engine** — sesh's task model IS the engine
- **No Archon runtime dependency** — we borrow grammar, not code
- **Workers are pull-based** — they choose what to work on next via sesh-ops CAS, not pushed by a scheduler
- **Failure recovery** is free — task `attempts` / `max_attempts` / sweeper already in sesh
- **Distribution** is free — multi-worker, multi-machine fan-out via sesh's NATS leafs

## Why

Today orch has `bin/orch-goal-pursue` — a hardcoded sequence (create goal → drive worker → watch terminator → mark complete). It works but it's the only workflow primitive and it's not reusable.

A workflow yaml generalizes this:

- Multi-step DAG (plan → implement → review → PR) instead of one-shot prompt
- Conditional branches, loops, approval gates
- Cross-step variable substitution (`$plan.output`)
- Reusable: commit `workflows/build-feature.yaml` to the repo, run anywhere
- Composable: subtree topology + workflow + tasks = full subtree assignment

Operators want this; Archon proved the grammar works at scale.

## Goals

1. YAML grammar compatible with Archon's (so operators familiar with Archon transfer their mental model)
2. Compiler from workflow yaml → sesh task records
3. Variable substitution at task-pull time using sesh's existing `result` field on upstream tasks
4. New node type `spawn:` (Proposal 0002) for in-workflow worker provisioning
5. New node type `assign:` to route a prompt to a specific named worker in the subtree

## Non-goals

- **Not a workflow runtime** — orch compiles + monitors; sesh executes via task pull
- **Not Archon-runtime-compatible** — borrows grammar, not engine. Archon workflows that depend on Archon-specific runtime behaviour won't port unchanged.
- **Not a replacement for orch-goal-pursue** — both coexist; goal-pursue is the convenience CLI for the simple case, workflow yaml is for the structured case

## Public interface — Workflow YAML

```yaml
# workflows/build-feature.yaml
name: build-feature
description: "Plan, implement, validate, review, PR — Archon-shaped"

# Optional: which subtree's KV scope to seed (overridable by --scope-id on apply)
# Defaults to the subtree's first workflow scope.
scope-id: e2ecafe1

nodes:
  # AI prompt — compiled to a sesh task. Worker pulls; description = prompt.
  - id: plan
    prompt: "Explore the codebase and create an implementation plan"
    # Optional: which named worker pulls this. If omitted, any worker
    # whose role/outfit matches can pull (sesh-ops's puller filter).
    assign: lead-engineer

  # Loop — task with `loop_until` metadata; worker iterates until predicate
  - id: implement
    depends_on: [plan]
    loop:
      prompt: "Read the plan from $plan.output. Implement next task. Validate."
      until: ALL_TASKS_COMPLETE
      max_iterations: 5
      fresh_context: true
    assign: lead-engineer

  # Deterministic bash — task type=bash; any worker (or orch itself) can run
  - id: run-tests
    depends_on: [implement]
    bash: "bun run validate"

  # AI review
  - id: review
    depends_on: [run-tests]
    prompt: "Review all changes against the plan. Fix any issues."
    assign: verifier

  # Human approval gate — interactive task; orch surfaces to operator
  - id: approve
    depends_on: [review]
    approval:
      prompt: "Present the changes for review. Address any feedback."
      until: APPROVED

  # Create PR
  - id: create-pr
    depends_on: [approve]
    prompt: "Push changes and create a pull request"
    assign: lead-engineer

  # New orch-flavoured node: provision a worker mid-workflow
  - id: spawn-verifier
    depends_on: [plan]
    spawn:
      name: verifier
      agent: claude-code
      tmux: { headless: true }
      outfit: { bundle: backend/verifying }
    # Workers that depend on this can `assign:` against the spawned name
```

## Node types

Direct from Archon:
- `prompt:` — single AI prompt; compiles to a task with type=prompt
- `loop:` — iterative prompt with `until:` predicate + `max_iterations:`; compiles to a task with loop metadata
- `bash:` — shell command; compiles to a task with type=bash
- `script:` — named script + runtime (bun/uv); compiles to a task with type=script
- `command:` — named command file (`.orch/commands/<name>.md`); compiles to a task with type=command
- `approval:` — interactive gate; compiles to a task that pauses for operator input

Orch additions:
- `spawn:` — provision a worker (embeds Proposal 0002 SpawnSpec); compiles to a task with type=spawn that orch handles
- `assign:` — modifier on any node; routes the compiled task to a named worker

DAG features (from Archon):
- `depends_on: [id1, id2]`
- `when: "$id.output.field == 'value'"`
- `trigger_rule: all_success | any_success | all_done`
- `$nodeId.output` variable substitution
- `idle_timeout` / `timeout` per-node

## Compilation rules

The compiler transforms `workflow.yaml` to a set of `sesh-ops task add` invocations:

```
node:
  id: implement
  depends_on: [plan]
  prompt: "Read $plan.output. Implement."
  assign: lead-engineer

→ becomes:

sesh-ops task add \
  --title="implement" \
  --description="Read \$plan.output. Implement." \
  --depends-on=plan \
  --metadata='{"node_type":"prompt","assign":"lead-engineer","loop":null}'
```

**Variable substitution** happens at task-pull time, not compile time:

When `lead-engineer` pulls the `implement` task, the puller (or orch-side resolver) reads `plan.result` from sesh KV and substitutes `$plan.output` in the description. This keeps the substitution declarative AND allows runtime data flow.

**Idempotency**: task ids are derived from `workflow.id + node.id` (e.g., `build-feature.implement`). Re-running compile against the same scope doesn't duplicate tasks; it diff-applies (`sesh-ops task get <id>` → update only if changed).

## Compile-time DAG validation

Per the Ousterhout-review adjustment (2026-05-18): the compiler MUST reject invalid workflows at compile time, not runtime. Make invalid states unrepresentable at the point of submission.

The compiler rejects:

1. **Cycles**: `A.depends_on=B; B.depends_on=A` → "cyclic dependency: A → B → A"
2. **Dangling node references**: `$nodeId.output` for an undeclared id → "unknown node reference: $foo.output"
3. **Discriminator violations**: a node with both `prompt:` AND `bash:` → "node X has multiple kind discriminators"
4. **Unreachable nodes**: depends_on a node that's never reachable (chain-broken or behind an always-false `when:`) → "node X is unreachable; depends_on chain has no source"
5. **Required-field violations**: missing `id:`, missing kind discriminator → "node X missing id"
6. **Assign without target**: `assign: foo` where `foo` is neither a declared `spawn:` node nor a worker in the targeted topology → "assign references unknown worker: foo"
7. **Variable substitution type mismatches**: `$nodeId.output.json.path` on a node whose result is known-non-JSON → warning (not error) since result type may vary at runtime

Validation runs:

- Implicitly during `orch workflow apply` (compile rejects → exit non-zero, nothing seeded)
- Explicitly via `orch workflow validate <yaml>` (CI-friendly; exit code conveys validity)
- Diagnostic via `orch workflow compile --print <yaml>` (shows the flattened DAG without applying)

The validator is the interface-contract test for this proposal (per the Ousterhout-review cross-cutting note about contract tests).

## CLI

```sh
# Compile workflow.yaml + apply to a subtree's KV scope
orch workflow apply build-feature.yaml --subtree bench-fleet

# Or composed via subtree apply (Proposal 0006)
orch subtree apply bench-fleet.yaml --workflow build-feature.yaml

# Inspect compiled task DAG before applying
orch workflow compile build-feature.yaml --print

# Validate yaml shape against schema
orch workflow validate build-feature.yaml

# Status: live view of task progress in the subtree's scope
orch workflow status build-feature --subtree bench-fleet
# Output:
#   plan        completed  (lead-engineer, 2m ago)
#   implement   in_progress (lead-engineer, since 8m)
#   run-tests   blocked    (waiting on: implement)
#   review      pending    (assign: verifier)
#   ...

# Cancel: mark all pending tasks as cancelled
orch workflow cancel build-feature --subtree bench-fleet
```

## How orch monitors execution

Workers pull tasks autonomously via `sesh-ops task pull`. orch doesn't drive the workflow — it observes:

1. Subscribes to `agents.> terminator` chunks (already happening for goal accounting)
2. Watches `sesh_tasks_<scope>_<id>` KV bucket via `nats kv watch`
3. On each task completion event:
   - Aggregate progress (X of N tasks done)
   - Surface stop events / questions / errors to operator
   - Detect stuck state (tasks not pulled within `idle_timeout`)
4. On final task completion: mark goal achieved (if a goal is associated)

Orch's monitor is a NATS subscriber, not a controller. Pull-based execution means the workflow runs even if orch crashes — workers keep pulling tasks until done. Orch restart resumes monitoring without reconciliation issues.

## Composition with Proposal 0006 (topology)

```sh
orch subtree apply bench-fleet.yaml --workflow build-feature.yaml
```

This is the canonical operator command. Mechanics:

1. Apply topology (Proposal 0006): spawn workers, attach sesh, ensure scope bucket exists
2. Compile workflow (this proposal): produces sesh-ops task add invocations
3. Seed tasks into the subtree's KV scope (idempotent via task id)
4. Workers in the subtree pull tasks autonomously
5. Orch monitors via bus subscriptions

The topology + workflow + tasks together form a complete subtree assignment. Orch hands them off, steps back, watches.

## Why borrow Archon grammar (not Archon runtime)

| | borrowing grammar | embedding Archon |
|---|---|---|
| **Familiarity** | operators who know Archon transfer their mental model | same |
| **Maintenance** | orch maintains a yaml compiler; ~500 LoC | orch tracks Archon's release cadence; cross-language integration; TS deps |
| **Failure semantics** | sesh's task model (CAS retries, sweeper, depends_on cascade) | Archon's runtime (different retry / failure shape) |
| **Distribution** | sesh's NATS leafs → multi-machine fan-out free | Archon's runtime is single-machine; would need NATS adapter |
| **Pull vs push** | workers pull from sesh — autonomous swarm | Archon engine pushes nodes — central controller |
| **Orch-specific extensions** | trivial (add `spawn:` / `assign:` to compiler) | requires Archon adapter PRs |

The grammar borrow lets orch sit on sesh's runtime (already in production) instead of importing Archon's runtime (a new dependency). orch users get Archon-familiar grammar AND sesh's distributed pull-model AND no Archon dependency.

For operators who want literal Archon — they can still use Archon directly. orch's workflow yaml is an alternative path, not exclusive.

## Backwards compatibility

- `bin/orch-goal-pursue` continues to work (the convenience-CLI for the simple case)
- A future improvement: `orch-goal-pursue` becomes a wrapper that generates a one-node workflow yaml internally — eliminates duplication. Out of scope for this proposal.
- Existing sesh-ops task workflows (hand-written task add scripts) continue to work — the workflow yaml is just a compile target for the same primitive

## Acceptance criteria

- [ ] `orch workflow compile <yaml>` produces correct sesh-ops task add invocations
- [ ] `orch workflow apply <yaml> --subtree <name>` seeds tasks into the subtree's scope idempotently
- [ ] `orch workflow status` shows live DAG progress from the bus + KV
- [ ] `orch workflow cancel` marks pending tasks cancelled
- [ ] All Archon node types compile correctly: `prompt`, `loop`, `bash`, `script`, `command`, `approval`
- [ ] Orch node types compile: `spawn`, `assign`
- [ ] Variable substitution (`$nodeId.output`) resolves at task-pull time
- [ ] Bench: a small workflow yaml exercised end-to-end through the docker-sesh hub
- [ ] Demo: `workflows/build-feature.yaml` ships in repo as the reference example
- [ ] Docs: `docs/workflow-yaml.md` reference + comparison to Archon

## Decisions deferred to design phase

1. ~~**Variable substitution location**~~ → **Mixed-time: cleanest split** (Dan: 2026-05-18, "pick the cleanest way"). Different ref types resolve at different phases:

   - **Compile-time** (substituted into task `description` as literals before seeding KV): env vars (`$ENV.NATS_URL`), workflow-static refs (`$WORKFLOW.scope_id`, `$WORKFLOW.name`), constants
   - **Pull-time** (resolved by the puller against sesh KV when task is claimed): cross-task data flow (`$nodeId.output`, `$nodeId.output.json.path`)

   Rationale: static refs are knowable at compile and don't need runtime indirection; cross-task refs MUST be pull-time because the upstream task hasn't run yet at compile. This is also the pattern used by GitHub Actions / GitLab CI / most CI/CD tools (mixed-time substitution by ref type). Cleaner than "everything one way" because each ref's semantics matches its actual data availability.
2. **`approval:` node UX** — how does orch surface an interactive approval to the operator? Telegram? Slack? CLI prompt? Lean: CLI prompt v1, integrations later.
3. **Failure semantics for `spawn:` nodes** — if spawn fails, does the workflow abort or retry? Lean: respect sesh's `max_attempts` task field — spawn failures retry until exhaustion, then the task is marked failed and the workflow stops at that branch.
4. **Loop iteration storage** — each loop iteration's output stored as `<node>.iter[N].output`? Or just `<node>.output` for the final iteration? Lean: final only (matches Archon).
5. **Cross-workflow references** — can workflow A's tasks reference workflow B's tasks? Lean: not in v1 (scope isolation enforces independence); v2 if needed.
6. **Reusable command files** — Archon has `.archon/commands/<name>.md` as named command refs. Orch should mirror with `.orch/commands/<name>.md`. Lean: yes, same convention.

## Risks

- **Grammar drift from Archon** — if Archon adds features orch's compiler doesn't track, operators get confused. Mitigation: document the compiler's supported subset; bump compiler version when Archon ships major changes; offer "Archon-compatible only" mode if needed.
- **Pull-based latency** — workers poll-or-watch the task bucket; there's a few-second latency between task completion and next task starting. Acceptable for AI workflows (each task is minutes, not milliseconds). Mitigation: use sesh's KV watch (push-based) instead of polling.
- **Workflow yaml complexity** — operators write deeply nested yaml, debug becomes hard. Mitigation: `orch workflow compile --print` shows the flattened task DAG; `orch workflow validate` enforces schema; reference workflows live in `workflows/` for copying.

## Effort estimate

~3 weeks:
- Week 1: yaml schema + validator + compiler (Archon node types only)
- Week 2: orch-flavoured nodes (`spawn:`, `assign:`); variable substitution at pull time; `orch workflow apply/status/cancel` subcommands
- Week 3: integration with Proposal 0006 (`orch subtree apply --workflow`); demo workflows; bench validation; docs
