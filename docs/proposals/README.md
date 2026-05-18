# Proposals

Design specs for the orch decomposition / modularization effort.

| # | Title | Status | Depends on |
|---|---|---|---|
| [0001](0001-extract-synadia-agent-shim.md) | Extract `synadia-agent-shim` to a sister repo | draft (Dan: yes, spec now, design later) | none |
| [0002](0002-typed-executor-contract.md) | Typed Executor Contract (YAML SpawnSpec / WorkerHandle) — Archon-shaped | draft (Dan: design + implement fully) | none |
| [0003](0003-extract-executor-backends.md) | Extract executor backends to sister repos | draft (Dan: enhance execution targets) | 0002 |
| [0004](0004-goal-accounting-absorption-into-sesh.md) | Absorb goal accounting into sesh | draft (Dan: spec only, not soon) | sesh team alignment |
| [0005](0005-operator-registry-consolidation.md) | Operator registry consolidation | draft (Dan: definitely, as follow-on) | none |
| [0006](0006-subtree-topology-yaml.md) | Subtree topology YAML (declarative fleet state — orch operator NOT in topology) | draft | 0002 |
| [0007](0007-workflow-yaml-compiled-to-task-dag.md) | Workflow YAML — Archon-grammar compiled to sesh task DAG | draft | 0002, 0006 |

## Reading order

If you're new to this:

1. Read [0001](0001-extract-synadia-agent-shim.md) first — the highest-leverage move, releases the biggest module
2. Then [0002](0002-typed-executor-contract.md) — the spawn-spec foundation (Archon-shaped YAML)
3. Then [0006](0006-subtree-topology-yaml.md) — composition above spawn-spec; declarative fleets
4. Then [0007](0007-workflow-yaml-compiled-to-task-dag.md) — composition above topology; Archon-grammar compiled to sesh task DAG
5. Then [0005](0005-operator-registry-consolidation.md) — operator-side deepening, independent of the others
6. Then [0003](0003-extract-executor-backends.md) — relevant once 0002 lands and Dan adds more execution targets
7. Finally [0004](0004-goal-accounting-absorption-into-sesh.md) — long-tail, not soon

## Composition picture

```
0002 SpawnSpec     ── "how to provision ONE worker"
       │
       ▼ composes into
0006 SubtreeTopology ── "what's in MY FLEET" (orch operator NOT in it)
       │
       ▼ orch operator runs:
       │      orch subtree apply <topology>.yaml --workflow <workflow>.yaml
       ▼
0007 Workflow      ── "what the FLEET DOES" (Archon-grammar → sesh task DAG)

Orch operator = supervisor running ABOVE all three. Orch applies, monitors, steps back.
Workers PULL tasks autonomously (sesh-ops CAS). Orch doesn't push — it observes.
```

## Notes

- Each proposal has its own "Decisions deferred to design phase" section — those are the architectural questions surfaced for Dan's judgment before implementation starts.
- Estimates are for one focused engineer; multiply for context-switching.
- Bench impact is called out per proposal — none should break the current 64/0/0 unless explicitly noted.
