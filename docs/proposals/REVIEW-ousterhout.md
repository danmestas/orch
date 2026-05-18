# Ousterhout review of the decomposition proposals

Applied lens: *A Philosophy of Software Design* (Ousterhout). Key principles invoked:

- **Deep modules**: small interface, large implementation; high leverage and locality
- **Information leakage**: implementation details bleeding through the interface
- **Pull complexity downward**: accept implementation pain so callers stay simple
- **Define errors out of existence**: make invalid states unrepresentable
- **General-purpose > special-purpose**: usually deeper, even if a little more work upfront
- **Strategic vs tactical**: invest in design even when shipping is the easier path
- **Temporal decomposition is dangerous**: split by abstraction not by time-order

Review verdicts and per-proposal concerns below. Recommendations are graded:

- 🟢 = strong fit; ship as drafted
- 🟡 = ship with named adjustment
- 🔴 = reconsider; risk of regression

---

## 0001 — Extract synadia-agent-shim 🟢

**Deep or shallow?** Deep. Adapter interface = 4-5 methods. Implementation is ~3000 LoC Go: NATS plumbing, chunk encoding, watchdog, envelope headers, signal dispatch, terminator state machine. Five existing adapters prove the seam.

**Information leakage?** One real leak: the subject prefix `agents.>` and signal prefix `orch.signal.>` are hardcoded. A non-orch consumer inherits orch's namespace whether they want it or not.

🟡 **Adjustment applied:** added `SubjectPrefix` (default `"agents"`) and `SignalPrefix` (default `"orch.signal"`) to public `shim.Config`. Pulls the convention out of the implementation into the interface. Orch keeps defaults; other consumers override.

**Pull complexity downward?** Yes — orch becomes simpler post-extraction (drops Go from npm package; shim release cadence independent).

**Define errors out of existence?** Mostly. Chunk constructors steer correctly.

**Strategic vs tactical?** Strategic. Highest-leverage move in the batch.

---

## 0002 — Typed Executor Contract 🟡

**Deep or shallow?** Borderline. Wide interface (SpawnSpec has many fields); substantial implementation. Most fields optional with sane defaults — minimal spec is tiny.

**Information leakage?** Two leaks:

1. **Executor discriminator forces operators to know the implementation.** `tmux:` vs `cf-worker:` is implementation-detail leaking. Acceptable — operators *want* control over where their workers run.
2. **`outfit:` shape leaks `suit prepare`'s output.** Mitigated: `bundle:` is a string handle, not a path. Suit owns resolution.

**Define errors out of existence?** Partial. Discriminator XOR rule enforced. Missing required fields caught early.

🟡 **Adjustment applied:** `agent:` field is enum-locked in JSON Schema. Unknown agents fail at PARSE, not runtime. Extending the enum is the controlled extension path for new adapters.

**Strategic vs tactical?** Strategic.

---

## 0003 — Extract executor backends 🟡

**Deep or shallow?** Mixed. Tmux backend is ~50 LoC bash (shallow). CF Worker is substantial TS + wrangler + DO bindings (deep).

🟡 **Adjustment applied:** scope narrowed to "heavyweight only." Tmux STAYS in orch's main repo. Extracting shallow modules creates overhead (release coordination, version skew, install complexity) for no leverage gain. Extract only `cf-worker`, `cf-durable-object`, future heavy backends.

**Information leakage?** Per-backend CLI surface (`--probe-version`, `--validate`, `--abort`) could fragment. Mitigation: `backend.json` manifest spec.

**Strategic vs tactical?** Strategic for heavy backends; tactical-and-wrong for tmux (corrected).

---

## 0004 — Goal accounting absorption into sesh 🟢

**Deep or shallow?** Pure pull-complexity-downward into sesh.

**Information leakage?** Eliminates a leak. Today orch knows sesh's KV schema; cross-repo coupling. Post-absorption, sesh owns its own schema.

**Pull complexity downward?** Textbook example.

**Define errors out of existence?** Wire-shape concerns vanish; in-process accountant has no NATS hop.

**Strategic vs tactical?** Strategic. Cleanest architectural move in the batch.

🟢 Ship as drafted (when sesh-team bandwidth allows).

---

## 0005 — Operator registry consolidation 🟢

**Deep or shallow?** Classic Ousterhout deepening. 5 sources joined behind 3-method interface. Very deep.

**Information leakage?** Today's state is a leak palace — five sources, every consumer joins differently. Post-consolidation, zero leakage.

**Pull complexity downward?** Today every consumer (orch-tell, orch-ask, orch-peek, orch-spy, UI) re-implements the join. Post-consolidation, one implementation.

**Define errors out of existence?** The "worker not registered on the bus" error (which bit during the topology demo) becomes impossible.

**Strategic vs tactical?** Strategic. Probably the highest-quality refactor in the batch.

🟢 Ship as drafted.

---

## 0006 — Subtree Topology YAML 🟡

**Deep or shallow?** Medium. Wide interface; real implementation depth (idempotent apply, partial-failure cleanup, state cache, sesh provisioning).

**Information leakage?** `state:` section could leak sesh-ops's schema.

🟡 **Adjustment applied (1):** `state:` reshaped as a thin pass-through to sesh-ops commands. Don't redefine schemas — sesh owns the goal/task/KV shape.

**Pull complexity downward?** Yes. Operators stop writing imperative bash loops.

**Define errors out of existence?** Push-once apply (Dan's decision) means drift is possible but visible via `status`. Honest interface — Ousterhout favors honesty over false claims.

**Temporal decomposition risk:** `apply` is inherently time-ordered (each phase depends on prior). Ousterhout warns against temporal decomposition; here it's appropriate.

🟡 **Adjustment applied (2):** apply's five phases documented as part of the public interface, not implementation. Per-phase failure modes called out.

---

## 0007 — Workflow YAML 🟢

**Deep or shallow?** Deep. The strategic decision — DON'T reinvent a workflow engine; sesh's task model IS the runtime. Compiler is ~500 LoC; runtime is sesh's existing production code.

**Pull complexity downward?** Strongest move in the batch. Orch could have built an engine; instead pushes runtime to sesh.

**Information leakage?** Compiler MUST mirror sesh's task model — but does so as transparent translation, not parallel abstractions.

Mixed-time substitution (Dan's decision): static refs at compile, cross-task refs at pull. Each ref category resolves at the phase where its data is actually available. No artificial uniformity.

**Define errors out of existence?**

🟡 **Adjustment applied:** compile-time DAG validator. Rejects cycles, dangling refs, discriminator violations, unreachable nodes, missing fields, assign-to-unknown-worker. Makes invalid workflows unrepresentable at submission.

**Strategic vs tactical?** Strategic. The decision NOT to import Archon's runtime (only borrow grammar) prevents years of cross-language maintenance burden.

🟢 Ship with validator-tightening adjustment.

---

## Cross-cutting observations

### Interface-contract tests, not just implementation tests

Each proposal's acceptance criteria are mostly implementation-level (e.g., "63/63 tests pass"). Ousterhout would push for tests *at the interface boundary*: consume only the public surface; assert the documented behavior.

🟡 **Recommendation:** Each proposal adds one explicit "interface contract test." Implementation tests catch regressions; interface tests catch information leaks and accidental breaking changes.

For 0007 specifically: `orch workflow validate` IS the contract test — its existence is captured in the adjustment.

### Coupling map

```
0002 (SpawnSpec) ◄── 0006 (Topology)  ◄── 0007 (Workflow)
       ▲                  ▲                     ▲
       │                  │                     │
       ▼                  ▼                     ▼
0003 (Executor    0001 (Shim         0005 (Registry —
      backends)         extraction)        independent of all)
```

0001 + 0005 are independent — can ship anytime. 0002 is keystone for the YAML chain.

### One missing proposal — observability event stream

Orch's monitoring is per-consumer today (poll). Post-0005, opens the door: a single `orch.observe.events` event stream consumers subscribe to.

🟡 **Recommendation:** Future Proposal 0008 — operator-side observability event stream. Out of scope here.

---

## Summary verdicts

| # | Proposal | Verdict | Adjustment applied |
|---|---|---|---|
| 0001 | Shim extraction | 🟢 strong | SubjectPrefix + SignalPrefix in Config |
| 0002 | Spawn-spec YAML | 🟡 with adjustment | enum-lock `agent:` field in schema |
| 0003 | Executor extraction | 🟡 with adjustment | extract heavyweight only; tmux stays in orch |
| 0004 | Goal absorption | 🟢 strong | none — textbook pull-complexity-down |
| 0005 | Registry | 🟢 strong | none — textbook deepening |
| 0006 | Topology yaml | 🟡 with adjustments | `state:` as sesh-ops pass-through; document apply ordering |
| 0007 | Workflow yaml | 🟢 with adjustment | DAG validator at compile time |

**Overall:** strategic, not tactical. 0005 (registry) and 0004 (goal absorption) are the highest-quality Ousterhouts. 0003 needed one carve-out (tmux stays). All adjustments applied to the spec docs.

## Sequencing recommendation

```
Phase 1 (parallel): 0001 + 0005     ← independent, ship first
Phase 2:            0002             ← spawn-spec foundation
Phase 3 (parallel): 0006 + 0003     ← compose on 0002
Phase 4:            0007             ← workflow on top of topology
Phase 5:            0004             ← when sesh team is ready
```

---

*Review applied 2026-05-18 against the proposals as committed to `docs/decomposition-proposals` branch (#147, merged).*
