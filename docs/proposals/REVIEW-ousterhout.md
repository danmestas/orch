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

**Deep or shallow?** Deep. Adapter interface (`Start`, `OnPrompt`, `Events`, `Close`, optional `Abort`) = 4-5 methods. Implementation is ~3000 LoC Go: NATS plumbing, chunk encoding, watchdog, envelope headers, signal dispatch, terminator state machine. Five existing adapters prove the seam.

**Information leakage?** One real leak: the subject prefix `agents.>` and the signal prefix `orch.signal.>` are hardcoded in the shim. They're orch-naming, not Synadia spec. A non-orch consumer (e.g., a future dagnats agent) inherits orch's namespace whether they want it or not.

🟡 **Recommendation:** add `SubjectPrefix` (default `"agents"`) and `SignalPrefix` (default `"orch.signal"`) fields to the public `shim.Config` struct. Pulls the convention out of the implementation and into the interface. Orch keeps the defaults; other consumers override. Tiny change, large leverage gain.

**Pull complexity downward?** Yes — orch becomes simpler post-extraction (drops Go entirely from npm package; shim's release cadence is independent).

**Define errors out of existence?** Mostly. The Adapter interface accepts any `Chunk`, including malformed ones, and errors propagate through `Events()`. Could be tighter (chunk constructors only, no raw struct literals) but the existing helpers (`NewResponseChunk`, etc.) already steer correctly.

**Strategic vs tactical?** Strategic. Extracting accepts a week of design pain to release the biggest module. Worth it.

---

## 0002 — Typed Executor Contract 🟡

**Deep or shallow?** Borderline. The SpawnSpec has many fields; the WorkerHandle has many fields. The interface surface is wide. Implementation is dispatcher + per-executor backends — substantial but not overwhelming relative to the interface.

The Archon-style flat YAML helps — it's wider than necessary by feature, narrower than alternatives (Kubernetes apiVersion/kind/metadata/spec). Acceptable depth, not great depth.

**Information leakage?** Two real leaks:

1. **Executor discriminator forces operators to know the implementation.** `tmux:` vs `cf-worker:` vs `cf-do:` is implementation-detail leaking into the spec. A deeper interface would say "spawn this worker; runtime picks the best executor." But that's premature optimization — operators *do* want control over where their workers run. **Acceptable leak**, document the intent.

2. **`outfit:` shape leaks `suit prepare`'s output shape.** If suit's bundle structure changes, every spawn-spec consumer updates. Mitigation: `outfit: { bundle: <name> }` is a string handle, not a path. Suit owns resolution; spawn-spec is opaque downstream. 🟢 Good as drafted.

**Pull complexity downward?** Yes. Spawn-spec lets operators write declarative requests; the dispatcher does the messy work (resolving outfit bundles, dispatching to executors, parsing handles).

**Define errors out of existence?** Partial. The XOR rule on executor blocks (`tmux:` XOR `cf-worker:` XOR `cf-do:`) is enforced by the validator — invalid spec rejected at parse time. Good. But: missing required fields (`agent:`, `cwd:`) are also caught early. Compile-time substitution catches missing env vars before tasks land in KV.

🟡 **Recommendation:** Make `agent:` an enum (`claude-code | codex | pi | gemini | echo`) in the JSON Schema. Today it's free-string ("anything matching an adapter name"). Enforcing the enum eliminates a class of typo failures and makes "unknown agent" a *parse* error, not a *runtime* error. Add new agents by extending the enum (small, controlled).

**Strategic vs tactical?** Strategic. Designing the YAML shape carefully now saves years of churn. The decision to mirror Archon's grammar (not Kubernetes ceremony) is the strategic move.

---

## 0003 — Extract executor backends 🟡

**Deep or shallow?** Mixed. Tmux backend is ~50 LoC bash (shallow). CF Worker backend is substantial TS + wrangler + DO bindings (deep enough to deserve its own repo).

🟡 **Recommendation:** Don't extract tmux. Extract only the heavyweight backends (`cf-worker`, `cf-durable-object`, future browser-tab, future devcontainer). Keep `executors/tmux/` in orch's main repo — it's small, cohesive with the dispatcher, and earns its keep by proximity.

Ousterhout's view: extracting a shallow module creates overhead (release coordination, version skew, install complexity) for no leverage gain. Extract only when the seam pays for itself.

Adjustment to the proposal: rename to "Extract heavyweight executor backends" and explicitly carve tmux out.

**Information leakage?** Each backend's `--probe-version` / `--validate` / `--abort` flags expand the per-backend CLI surface. If different backends invent different CLI shapes, the dispatcher has to special-case each. Mitigation: include a `backend.json` manifest spec that backends ship — declares their CLI surface and version, dispatcher reads it.

**Pull complexity downward?** Yes, for heavyweight backends. No (or negative) for tmux.

**Strategic vs tactical?** Strategic for the heavy backends; tactical-and-wrong for tmux.

---

## 0004 — Goal accounting absorption into sesh 🟢

**Deep or shallow?** This is pure "pull complexity downward into sesh." The accounting daemon belongs in sesh — sesh owns goals, sesh-ops already has the `goal account` primitive, the data lives in sesh's KV. Orch hosting it today is a historical accident.

**Information leakage?** Eliminates a leak. Today orch's daemon knows sesh's KV schema (`sesh_goals_<scope>_<id>`); cross-repo coupling. Post-absorption, sesh owns its own schema — orch doesn't need to track it.

**Pull complexity downward?** This is the textbook example. Orch loses a daemon, a hook, two bins; sesh gains a built-in capability. Net complexity drops.

**Define errors out of existence?** Wire-shape concerns disappear — sesh's accountant is in-process to sesh's hub, so terminator chunks reach it without a NATS hop. Race conditions between orch's daemon and sesh's KV writes vanish.

**Strategic vs tactical?** Strategic. The cleanest architectural move in the batch. Worth the cross-team conversation when bandwidth allows.

🟢 Ship as drafted (when bandwidth allows). No adjustments needed.

---

## 0005 — Operator registry consolidation 🟢

**Deep or shallow?** This is THE classic Ousterhout deepening pattern. Five sources of truth joined behind one interface (`Snapshot()`, `Watch()`, `Lookup()`). Interface = ~3 methods + one struct (`Worker`). Implementation = NATS sub + heartbeat correlator + file readers + join logic. Very deep.

**Information leakage?** Today's state is a leak palace — five sources, every consumer joins them differently. Post-consolidation, the join is hidden behind the interface. Zero leakage.

**Pull complexity downward?** Yes. Today every consumer (orch-tell, orch-ask, orch-peek, orch-spy, UI) re-implements the join. Post-consolidation, one implementation; consumers just call the interface.

**Define errors out of existence?** The "worker not registered on the bus" error from `orch-tell` (which bit us during the topology demo) was an information-leakage bug — the registry was lying because it had stale state. With a proper registry that reads live bus state, the error is impossible.

**Strategic vs tactical?** Strategic. Probably the highest-quality refactor in the batch. Ousterhout would point at this as the textbook example of deepening.

🟢 Ship as drafted. No adjustments.

---

## 0006 — Subtree Topology YAML 🟡

**Deep or shallow?** Medium. The interface (yaml shape + `apply`/`status`/`destroy`/`diff` commands) is moderately wide. Implementation has real depth (idempotent apply, partial-failure cleanup, state cache, sesh provisioning, executor fan-out). Acceptable.

**Information leakage?** One concern: `state.kv-scopes` and `state.goals` are subtree-seed primitives that leak sesh-ops's schema into the topology yaml. If sesh changes how scopes work, every topology yaml updates.

🟡 **Recommendation:** Make `state:` a thin pass-through to sesh-ops (resolves to literal `sesh-ops task add` / `sesh-ops goal create` invocations at apply time). Don't define new schemas; just accept sesh-ops's input shape with minor adaptation.

**Pull complexity downward?** Yes. Operators stop writing imperative bash loops; topology yaml is declarative.

**Define errors out of existence?** Partial. Push-once apply (v1 per Dan's decision) means drift is possible but visible via `status`. Reconciliation isn't claimed, so the operator isn't surprised when drift exists. Good — Ousterhout favors honest interfaces over claims you can't keep.

**Strategic vs tactical?** Strategic. Investing in the declarative model now pays dividends as fleets grow.

**Subtler concern — temporal decomposition risk:**

The `apply` command is implicitly time-ordered: resolve sesh → spawn workers → seed state. Ousterhout warns against temporal decomposition. Here it's appropriate (each step DEPENDS on the prior — workers need sesh; state needs scope buckets). Document this in the interface so operators know the order; don't pretend it's "all at once."

🟡 Ship with two adjustments:
1. `state:` thin pass-through to sesh-ops (avoid schema leak)
2. Document apply's time-ordered semantics in the interface, not just implementation

---

## 0007 — Workflow YAML (Archon-grammar → sesh task DAG) 🟢

**Deep or shallow?** Deep. The compiler (Archon yaml → sesh task records) is small (~500 LoC). The runtime behaviour (DAG execution, retries, completion cascade, parallel fan-out) lives in sesh — already deep, already production.

**This is the strongest "pull complexity downward" move in the batch.** Orch could have built a workflow engine; instead it pushes the runtime to sesh and stays a compiler. Sesh's task model already handles failure recovery, distribution, persistence — orch reuses it.

**Information leakage?** The compiler MUST mirror sesh's task model (`depends_on`, `attempts`, `result`, etc.) — but it does this by *being* a translation layer, not by inventing parallel concepts. Acceptable transparency.

The mixed-time substitution (Dan's decision: compile-time for static refs, pull-time for cross-task refs) is the cleanest split. Each ref category resolves at the phase where its data is actually available. No artificial uniformity.

**Define errors out of existence?**

🟡 **Recommendation:** Validate the DAG at compile time. Today's draft doesn't explicitly say the compiler rejects:

- Cycles (`A.depends_on=B; B.depends_on=A`)
- Dangling refs (`$nodeId.output` for an undeclared node)
- Type mismatches (node has `prompt:` AND `bash:` — but the discriminator XOR should already catch this)
- Unreachable nodes (depends_on a node that can never complete)

Add a `orch workflow validate` step that does full DAG analysis before apply. Makes invalid workflows unrepresentable at the point of submission, not at runtime.

**Strategic vs tactical?** Strategic. The decision NOT to import Archon's runtime (only borrow grammar) is the strategic move that prevents years of cross-language maintenance burden.

🟢 Ship as drafted with the validator-tightening adjustment.

---

## Cross-cutting observations

### What's missing across all proposals

**Test plans for the interfaces, not just the implementations.** Each proposal lists "acceptance criteria" but mostly in terms of implementation (e.g., "63/63 tests pass"). Ousterhout would push for tests *at the interface boundary*: given this YAML shape, this Go interface, this CLI surface — what's the test that proves the interface is honest about its behavior?

🟡 **Recommendation:** Each proposal should explicitly call out an "interface contract test" — a test that consumes only the public surface and asserts the documented behavior. Implementation-level tests (e.g., adapter behavior) are good; interface-level contract tests are what catch information leaks and accidental breaking changes.

### Coupling map

```
0002 (SpawnSpec) ◄── 0006 (Topology)  ◄── 0007 (Workflow)
       ▲                  ▲                     ▲
       │                  │                     │
       ▼                  ▼                     ▼
0003 (Executor    0001 (Shim         0005 (Registry —
      backends)         extraction)        independent of all)
```

0001, 0005 are independent of the others — they can ship anytime. 0002 is the keystone for the YAML chain (0006, 0007). 0003 follows 0002.

0004 sits alone — pull complexity to sesh, no orch-side prereqs.

**Sequencing recommendation:** ship 0001 + 0005 in parallel first. Then 0002. Then 0006 + 0003 in parallel. Then 0007. Then 0004 when sesh team is ready.

### One missing proposal — observability

Orch's monitoring story is currently per-consumer (orch-peek polls, the UI polls, bus monitors poll). Post-0005 (registry consolidation) opens the door to a deeper deepening: a single observability event stream that consumers subscribe to.

🟡 **Recommendation:** Consider proposal 0008 — "operator-side observability event stream." Builds on 0005's registry; exposes `orch.observe.events` subject for UI / dashboards / `orch subtree watch` / future tooling. Out of scope for this review; flagging for future work.

---

## Summary verdicts

| # | Proposal | Verdict | Adjustments |
|---|---|---|---|
| 0001 | Shim extraction | 🟢 strong | add `SubjectPrefix` + `SignalPrefix` to Config |
| 0002 | Spawn-spec YAML | 🟡 with adjustment | enum-lock `agent:` field in schema |
| 0003 | Executor extraction | 🟡 with adjustment | extract heavyweight only; keep tmux in orch |
| 0004 | Goal absorption | 🟢 strong | none — textbook pull-complexity-down |
| 0005 | Registry | 🟢 strong | none — textbook deepening |
| 0006 | Topology yaml | 🟡 with adjustments | `state:` as sesh-ops pass-through; document apply ordering |
| 0007 | Workflow yaml | 🟢 with adjustment | DAG validator at compile time |

**Overall:** strategic, not tactical. The proposals invest in design for long-term leverage. 0005 (registry) and 0004 (goal absorption) are the highest-quality Ousterhouts in the batch. 0003 needs one carve-out (tmux stays in-tree). Otherwise solid.

---

*Review applied 2026-05-18 against the proposals as committed to `docs/decomposition-proposals` branch.*
