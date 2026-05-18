# Proposal 0006 — Subtree Topology YAML

**Status:** draft
**Depends on:** Proposal 0002 (SpawnSpec — topology composes spawn-specs)
**Composes with:** Proposal 0007 (Workflow compiles to tasks for a subtree)

## Mental model

Orch is the **operator** — the supervisor running above the fleet. Orch is **NOT** in the topology; orch *applies* topologies.

A **subtree** is a fleet of workers + their sesh context + their seed state. Orch instantiates a subtree, hands it a workflow (Proposal 0007) and seed tasks, then steps back to monitor via bus events. The subtree is self-driving once apply completes — workers pull tasks autonomously via CAS, complete them, unblock downstream.

```
  ┌─────────────────────────────────────────────────────────────┐
  │  ORCH (operator — you + me, monitoring)                     │
  └─────────────────────────────────────────────────────────────┘
            │ apply                       │ monitor
            ▼                             ▲
  ┌─────────────────────────────────────────────────────────────┐
  │  SUBTREE A (declared in subtree-A.yaml)                     │
  │  ├── sesh hub  (own or shared with orch)                    │
  │  ├── workers   (lead-engineer, verifier, codex-eng, ...)    │
  │  ├── workflow  (compiled to sesh task DAG)                  │
  │  └── tasks     (seed state in KV scope)                     │
  └─────────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────────┐
  │  SUBTREE B (declared separately, different workflow)        │
  │  ├── ...                                                    │
  └─────────────────────────────────────────────────────────────┘
```

Recursion is natural: a subtree's worker can itself become an operator of *its own* subtree (the sesh-orc-supervising-sesh-issues pattern from this session). The topology YAML describes the fleet that worker provisions — orch's topology yaml is for the root operator's perspective only.

## Why

Today subtree provisioning is **implicit** — operator runs N `orch-spawn` commands, sets up sesh, seeds tasks ad-hoc. Symptoms:

- Reproducing a fleet across machines requires re-running ad-hoc commands (shell history is the source of truth)
- Bench setups, demo recipes, multi-machine ops all reinvent the same `for h in ...; do orch-spawn ...; done`
- No diff/review of fleet shape (PRs that change the demo topology are invisible)
- No teardown primitive — "kill everything from yesterday's demo" requires grepping ps

Topology YAML makes the subtree declarative, reviewable, reproducible.

## Goals

1. YAML file describes a complete subtree: sesh context + workers + seed state
2. Orch applies the YAML: spawn workers, attach sesh, seed KV — idempotent (don't re-spawn alive workers)
3. Orch can diff (desired vs actual), destroy (tear down everything), status (snapshot live state)
4. Composes with Proposal 0007: topology + workflow yaml = subtree-with-workflow

## Non-goals

- **Not** Kubernetes — no controller loop, no reconciliation, no drift remediation in v1
- **Not** Terraform — no state file backend, no provider abstractions, no plan/apply separation
- **Not** Docker Compose — no networking abstractions, no service discovery (sesh handles that)
- **Not** orch itself — operator's own pane / session never appears in topology yaml

## Public interface

```yaml
# .orch/subtrees/bench-fleet.yaml
name: bench-fleet
description: "5-worker fleet for e2e bench validation"

# ── Sesh context: where the subtree's bus lives ─────────────────────────
sesh:
  # One of:
  # - existing: join an existing sesh hub by NATS URL
  # - spawn:    bring up a fresh sesh session for this subtree

  existing: $ORCH_NATS_URL    # default — share operator's hub

  # OR:
  # spawn:
  #   session: bench-fleet
  #   scope:   session         # or `project`
  #   cwd:     /tmp/bench       # optional; defaults to operator's cwd

# ── Workers in this subtree ─────────────────────────────────────────────
# Each entry inlines a SpawnSpec (Proposal 0002). Operator-readable names
# become the 5th subject token; bus subjects auto-derive.
workers:
  - name: lead-engineer
    agent: claude-code
    outfit: { bundle: backend/executing+pr-policy }
    tmux: { headless: true }

  - name: verifier
    agent: claude-code
    outfit: { bundle: backend/verifying }
    tmux: { headless: true }

  - name: codex-eng
    agent: codex
    tmux: { headless: true }

  - name: pi-eng
    agent: pi
    tmux: { headless: true }

  - name: gemini-eng
    agent: gemini
    tmux: { headless: true }

# ── Seed state (optional, pass-through to sesh-ops) ─────────────────────
# Each entry under `state:` is a thin pass-through to a `sesh-ops` invocation.
# We don't redefine schemas — sesh owns the goal/task/KV shape and orch
# just orchestrates the seeding calls. If sesh-ops's accepted input
# changes, this section's shape follows automatically.
#
# Compiled-from-workflow tasks (Proposal 0007) ALSO land here; this
# section is for additional state the workflow doesn't generate.
state:
  # Tasks: each entry is exactly a `sesh-ops task add` payload.
  # See ~/projects/sesh/docs/task-management.md for the schema.
  tasks:
    - scope: workflow
      scope-id: e2ecafe1
      title: "seed task example"
      depends_on: []
      max_attempts: 3
      metadata: {}

  # Goals: each entry is exactly a `sesh-ops goal create` payload.
  # See ~/projects/sesh/docs/goal-management.md.
  goals:
    - scope: workflow
      scope-id: e2ecafe1
      objective: "Run the bench and report results"
      budget_tokens: 50000

# ── Cross-cutting labels (optional) ─────────────────────────────────────
labels:
  purpose: bench
  owner: dmestas
```

## CLI

```sh
# Apply: spawn missing workers, attach sesh, seed state. Idempotent.
orch subtree apply bench-fleet.yaml

# Apply with a workflow (Proposal 0007) — compiles workflow to seed tasks
orch subtree apply bench-fleet.yaml --workflow workflows/run-bench.yaml

# Status: desired vs actual (read live state from bus + KV; compare to yaml)
orch subtree status bench-fleet

# Watch: stream events from the subtree (Monitor-friendly)
orch subtree watch bench-fleet

# Destroy: kill workers in the subtree, optionally tear down sesh if `spawn:`'d
orch subtree destroy bench-fleet

# List: show all subtrees orch knows about (from ~/.cache/orch-subtrees/)
orch subtree list

# Diff: print what apply WOULD change without applying
orch subtree diff bench-fleet.yaml
```

## Apply semantics — time-ordered, by design

`apply` is **temporally decomposed by necessity** — each phase depends on the prior. This ordering is part of the public interface, not an implementation detail; operators should be able to predict where in the sequence apply will fail.

The five phases, in strict order:

1. **Parse**: validate the yaml, including embedded SpawnSpecs (Proposal 0002's validator). Fails early on schema violations BEFORE touching live state.

2. **Resolve sesh context**: connect to an `existing:` hub, OR bring up a `spawn:`'d hub. Subsequent phases need the NATS URL to do anything.

3. **Spawn missing workers** (parallel within this phase):
   - Compare desired workers (yaml) vs live workers (`$SRV.INFO.agents`)
   - For each missing: invoke `orch-spawn` (Proposal 0002 dispatch) with the embedded SpawnSpec
   - Workers that already exist with matching name+agent are NOT re-spawned (idempotent on name)
   - Phase waits for ALL spawned workers to register on the bus before proceeding

4. **Seed state** (parallel within this phase): for each entry under `state.tasks` / `state.goals`, invoke the corresponding `sesh-ops` command. Idempotent on the entity id — repeat calls are no-ops, not duplicates.

5. **Persist**: write `~/.cache/orch-subtrees/<name>.applied.yaml` for later `diff` / `destroy`. Includes resolved NATS URLs, actual pane IDs, all info needed to tear down later.

**Why phase boundaries are explicit:**

- A partial-failure at phase 3 leaves workers on the bus but no state seeded — operator can re-apply to seed.
- A failure at phase 4 leaves state partially seeded — operator can re-apply (idempotent) to complete.
- A failure at phase 5 means the subtree IS up but orch's local cache wasn't written — operator can `orch subtree adopt <name>` to reconstruct.

**Failure mode summary**: apply is idempotent at every phase boundary; re-running cleans up partial state without operator intervention.

## Status semantics

`orch subtree status <name>` queries live state and compares to the cached `applied.yaml`:

```
Subtree: bench-fleet
Sesh:    nats://127.0.0.1:58413  (shared with operator)
Workers:
  ✓ lead-engineer    (alive, hb=8s ago)
  ✓ verifier         (alive, hb=12s ago)
  ✓ codex-eng        (alive, hb=15s ago)
  ✗ pi-eng           MISSING (declared, not on bus)
  ✓ gemini-eng       (alive, hb=10s ago)
Tasks:
  3 pending, 1 in_progress, 1 completed
Goals:
  bench-validation   pursuing  (used 12000 / 50000 tokens)
```

The missing worker (`pi-eng` ✗) is a drift indicator. Operator decides: re-apply (auto-respawn) or accept (mark in cache).

## Destroy semantics

`orch subtree destroy <name>`:

1. For each worker in the cached applied.yaml: `tmux kill-pane`, shim cleanup (per executor)
2. If sesh was `spawn:`'d: `sesh down --session=<name>`
3. KV state: **preserved by default** (operator's tasks/goals survive subtree teardown). Optional `--purge-state` flag wipes the scope buckets.
4. Remove `~/.cache/orch-subtrees/<name>.applied.yaml`

## What changes for operators

- Today's `for h in ...; do orch-spawn "$h" --headless; done` becomes `orch subtree apply fleet.yaml`
- Bench setups commit a `test/topologies/bench.yaml` instead of hard-coding worker count in bash
- Demo recipes are reproducible: `orch subtree apply demos/ui-grid-5.yaml`
- Disaster recovery: `orch subtree destroy old && orch subtree apply old` rebuilds clean

## Composition with Proposal 0007 (workflow)

```sh
orch subtree apply bench-fleet.yaml --workflow build-feature.yaml
```

1. Apply topology: spawn workers, attach sesh
2. Compile workflow (Proposal 0007): produces N task records
3. Seed tasks into the subtree's KV scope
4. Workers pull tasks autonomously via CAS (already in sesh-ops)
5. Orch monitors completion via bus events

The topology and workflow yamls are independent — same topology can run multiple workflows in sequence; same workflow can target multiple topologies.

## Backwards compatibility

- `orch-spawn` continues to work for ad-hoc one-off spawns
- Existing operator workflows unaffected
- `orch subtree apply` is purely additive

## Acceptance criteria

- [ ] `orch subtree apply <yaml>` spawns missing workers idempotently
- [ ] `orch subtree status <name>` shows desired-vs-actual diff
- [ ] `orch subtree destroy <name>` cleans up cleanly
- [ ] `orch subtree diff <yaml>` shows changes without applying
- [ ] `orch subtree apply --workflow Y.yaml` composes with Proposal 0007
- [ ] State persisted to `~/.cache/orch-subtrees/<name>.applied.yaml`
- [ ] Bench's docker-sesh setup uses a topology yaml instead of inline bash loops
- [ ] Demo recipes documented in `docs/topologies/`

## Decisions deferred to design phase

1. ~~**Reconciliation loop?**~~ → **Push-once for v1** (Dan: 2026-05-18). Operator runs `orch subtree apply` imperatively; drift surfaces via `orch subtree status` and is fixed by re-applying. No controller loop, no daemon, no watch-and-repair. Avoids reinventing Kubernetes; matches the operator's mental model (apply is an imperative, not a desired-state contract). Reconciliation can be a v2 add-on gated on a `--watch` flag if multi-machine ops earn its keep.
2. **`sesh: spawn:` semantics** — does the subtree's sesh hub live within the operator's hub mesh (as a leaf) or fully independent? Lean: leaf attachment by default; flag for full independence.
3. **Cross-subtree references** — can a worker in subtree-A reach workers in subtree-B via the bus? Lean: yes if they share a hub; no isolation enforced at the topology layer (sesh's KV scoping handles isolation).
4. **Templating** — variables like `$ORCH_NATS_URL` resolve from env. Should there be Go-template-style `{{ .NATS_URL }}` for richer interpolation? Lean: env-var only for v1.
5. **State storage** — apply state in `~/.cache/orch-subtrees/<name>.applied.yaml` is local. For multi-machine ops, the topology yaml is the canonical source (committed). The applied.yaml is just a local cache for diff/destroy.

## Risks

- **Subtree sprawl** — operators apply many subtrees, forget which is which. Mitigation: `orch subtree list` + last-applied timestamps; aggressive default name conflict checks.
- **Sesh hub coupling** — if `sesh: existing:` and the operator's hub goes down, the subtree's workers lose connectivity. Mitigation: `sesh: spawn:` for independence when needed.
- **Idempotency edge cases** — re-applying with renamed workers leaves orphans. Mitigation: declarative diff before destructive operations; `--force-prune` flag for cleanup.

## Effort estimate

~2 weeks:
- Days 1-3: yaml schema + validator (Go), reuses SpawnSpec from 0002
- Days 4-6: `apply` / `status` / `destroy` / `diff` / `list` / `watch` subcommands
- Days 7-8: state cache, idempotency tests
- Days 9-10: bench migration to topology yaml; demo recipes; docs
