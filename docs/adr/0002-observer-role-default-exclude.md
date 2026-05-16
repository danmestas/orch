---
status: accepted (mechanism amended 2026-05-16 per #94)
date: 2026-05-09
---

# Observers are a meta-class, default-excluded from event subscribers

> **Amendment (orch#94, 2026-05-16):** `orch-listen` was retired. The
> observer-exclusion role is preserved — it now lives in the shim's
> `metadata.role` on `$SRV.INFO.agents`, and bus subscribers filter
> observers themselves (`nats sub` consumers should drop messages whose
> source pane carries `metadata.role == "observer"` unless they
> explicitly want them). The decision in this ADR still stands; the
> implementation locus moved from a fswatch-listener CLI to bus-side
> subscriber filters.

Spawned panes are tagged with a `role` in the registry — `worker` (default) or `observer`. `orch-listen` default-excludes observers; only explicit `--include-observers` surfaces their Stop events. `orch-tell` refuses worker→observer messaging unless `--force` is passed. The operator (no `ORCH_PANE_ID` set) can redirect anyone. We took this shape because observers exist to *watch*, not to *do work* — they are a third class alongside operator and worker. The amplification-loop incident on 2026-05-09 (a stasi spy's monitor woke the operator's listener every turn, oscillating) was the proof that treating spies as plain workers is wrong: an event-equivalence bug made observable as a feedback oscillation.

## Considered options

- **Reading A — observers are a meta-class** (accepted): `feedback_listen_to_all_workers` still holds for everything in the worker class; observers get their own default. Pass-through behavior for the operator only.
- **Reading B — every pane is a worker, the rule is universal**: rejected. Would mean fixing the amplification on the spy side via Pattern B in `monitoring-the-operator` rather than in the listener; load-bearing on operator + spy discipline holding.

## Why this is hard to reverse

The role tag is persisted in registry JSON; auto-detection lives in `orch-spawn` (stasi outfit / wait-watch cut → observer); the listener exclusion is the default branch in `orch-listen`; `orch-tell`'s refusal is a guard before send. Reversing means either redefining roles (touching every binary) or accepting that observers will wake the operator on every turn (the original bug).

## Implications

- Workers cannot redirect observers (worker→observer is refused). Observers report up to the operator via `orch-tell`; operator can tell observers freely.
- New outfits auto-detected as observer must be added to the role-map (currently inline in `orch-spawn`'s case statement, future: `~/.config/harness/role-map.json` per ADR-0001).
- The `--exclude` and `--exclude-self` flags on `orch-listen` are now narrower fixes; the role tag handles the common case structurally.

## Migration note (2026-05-16 — issue #60)

`~/.cache/orch-registry/<pane>.json` was the prior storage for the role tag. As of this migration, `orch-listen`'s `is_observer()` function queries `$SRV.INFO.agents` (NATS micro-service discovery) instead of reading registry JSON. The shim populates `metadata.role` at registration time from `$ORCH_ROLE`; `orch-spawn` sets that env var in the shim's process env from the resolved `$ROLE` before launch. Semantics are identical: `metadata.role == "observer"` ⇒ excluded by default. When NATS is unavailable the function falls back to treating the pane as a worker (include), matching the pre-migration behavior for unregistered panes.
