# Proposal 0004 — Absorb goal accounting into sesh

**Status:** draft (spec only; Dan does NOT plan to implement soon — captured for future reference)
**Depends on:** sesh team's roadmap alignment
**Blocks:** none

## Why

Today orch owns `cmd/orch-goal-stop-account-daemon/` — a sidecar daemon that:

1. Subscribes to `agents.> terminator` chunks on the bus
2. Parses each turn's metadata (agent, owner, session, instance_id, goal_id from metadata.goal_id)
3. Computes token usage per (harness, goal)
4. Writes decrement entries to sesh's `sesh_goals_<scope>_<id>` KV bucket via sesh-ops

Plus `hooks/orch-goal-stop-account.sh` — a Claude Code hook that triggers the daemon's accounting path.

Plus `bin/orch-goal-pursue` / `bin/orch-goal-status` — operator-facing CLIs.

**This whole subsystem is conceptually a sesh primitive.** sesh-ops already has `goal create`, `goal account`, `goal complete`, `goal pause` etc. The token accounting daemon is the missing piece — and it lives in orch rather than sesh because that's where it originally landed.

If goal accounting moves to sesh:

- sesh becomes the single source of truth for goal lifecycle including budget enforcement
- orch loses 1 daemon, 1 hook, 2 bin scripts, and a sesh-ops dependency
- Other Synadia consumers (dagnats-agents, sesh-ref-agent, alternative orchs) get goal accounting for free
- The accounting logic gets tested against sesh's own integration tests, not orch's bench

## Goals

1. Move the accounting daemon's logic into sesh (or sesh-ops) as a built-in capability
2. `bin/orch-goal-pursue` and `bin/orch-goal-status` either move to sesh-ops too, OR stay in orch as thin wrappers around sesh-ops
3. The orch hook (`hooks/orch-goal-stop-account.sh`) is no longer needed — sesh subscribes directly
4. orch keeps its operator UX for goals (the slash-command flow, the env-var injection on session start) but the accounting plumbing is sesh's

## Non-goals

- Changing the OPERATOR-FACING goal CLI (`sesh-ops goal create --objective`, `goal pause`, etc.) — those stay as-is
- Disturbing existing goals in flight at migration time
- Removing orch's `orch-goal-session-context.sh` hook (that's UX, not accounting)

## Architectural shape after absorption

```
                ┌─────────────────────────────────────┐
                │              sesh                    │
                │                                      │
                │  ┌──────────────────────────────┐    │
                │  │  goal-accountant (NEW)       │    │
                │  │  - subs agents.> terminator  │    │
                │  │  - parses metadata.goal_id   │    │
                │  │  - writes KV decrements      │    │
                │  └──────────────────────────────┘    │
                │                                      │
                │  ┌──────────────────────────────┐    │
                │  │  goal-management (existing)  │    │
                │  │  - sesh-ops goal CRUD        │    │
                │  │  - state machine             │    │
                │  └──────────────────────────────┘    │
                │                                      │
                │  ┌──────────────────────────────┐    │
                │  │  task-management (existing)  │    │
                │  └──────────────────────────────┘    │
                └─────────────────────────────────────┘
                              ▲
                              │ NATS + sesh-ops
                              │
                ┌─────────────────────────────────────┐
                │              orch                    │
                │  - operator UX                       │
                │  - bin/orch-goal-pursue (wrapper)    │
                │  - bin/orch-goal-status (wrapper)    │
                │  - hooks/orch-goal-session-context   │
                │  (no accounting daemon)              │
                └─────────────────────────────────────┘
```

The arrows: orch publishes terminator chunks on the bus (already does); sesh's new accountant listens; sesh-ops CLI exposes the read side. orch's CLIs become thin sesh-ops wrappers.

## Migration plan (when sesh team is ready)

### Step 1: sesh ships the accountant daemon

1. Sesh adds a built-in `goal-accountant` mode (probably `sesh hub serve` opt-in via flag, or a separate `sesh goal-accountant` subcommand)
2. The accountant subscribes to `agents.> terminator` AND to `agents.> response` chunks (the latter carries token counts in metadata, depending on the harness)
3. The accountant updates the goal's `used_tokens` KV field via CAS on every terminator that has `metadata.goal_id`
4. Test coverage: sesh's own integration tests assert the accountant behavior

### Step 2: orch removes the daemon

1. Delete `cmd/orch-goal-stop-account-daemon/`
2. Delete `hooks/orch-goal-stop-account.sh`
3. Update orch's install.sh to no longer install the daemon as a launchd / systemd service
4. orch's docker-sesh bench tests that goal accounting works through sesh, not through orch

### Step 3: orch-goal-pursue / orch-goal-status become thin wrappers

1. `bin/orch-goal-pursue` calls `sesh-ops goal create --objective` + does its env var setup (SESH_GOAL_ID etc.)
2. `bin/orch-goal-status` calls `sesh-ops goal get | jq ...` + formats for the operator

Or — if Dan prefers — `bin/orch-goal-*` move to sesh-ops as `sesh-ops goal pursue` and `sesh-ops goal status` directly, and orch users just use sesh-ops natively.

### Step 4: Cross-repo docs alignment

1. Update `docs/working-with-sesh.md` to point at sesh's accountant docs
2. Update `~/projects/sesh/docs/goal-management.md` with the accountant section
3. Update `migrating-to-synadia` skill if relevant

## What changes for operators

- Day-of-cutover: no observable change. Token accounting continues. The daemon hosting it changed.
- New: `sesh-ops goal account` can be called directly to inspect decrement history (already exists; just becomes more useful)
- Deprecation: `bin/orch-goal-pursue` and `bin/orch-goal-status` either disappear (if absorbed) or stay as compatibility wrappers (operator preference)

## What changes for sesh

- New daemon to maintain
- New responsibility for the wire shape of token-accounting NATS subjects (currently orch-defined)
- Sesh's integration tests grow to cover accounting end-to-end

## What changes for orch

- Fewer moving parts: 1 daemon and 1 hook removed
- One less sesh-ops dependency hidden inside orch
- The goal subsystem is no longer a "shared between orch and sesh" thing — it's purely sesh's, with orch as a consumer

## Backwards compatibility

- Token counts already in KV survive (same schema, sesh just maintains them now)
- In-flight goals at cutover keep working — orch's daemon stops, sesh's daemon starts; no data loss as long as the cutover is coordinated

## Acceptance criteria

- [ ] Sesh ships an accountant daemon (separate PR upstream; orch is consumer)
- [ ] orch's `cmd/orch-goal-stop-account-daemon/` removed
- [ ] orch's `hooks/orch-goal-stop-account.sh` removed
- [ ] orch's `bin/orch-goal-pursue` / `bin/orch-goal-status` are either thin wrappers or deleted (Dan decides)
- [ ] `docs/working-with-sesh.md` updated
- [ ] Existing goals in flight continue to accumulate decrements without operator intervention
- [ ] Bench (docker-sesh) Group 15 (goal-task linkage + token accounting) still passes — accounting now via sesh

## Decisions deferred

1. **Where does the accountant daemon run?** Inside `sesh hub serve`? As a separate `sesh goal-accountant` process? Lean: inside hub serve as a built-in mode, opt-in via flag, default-on.
2. **Token count extraction**: Claude Code's terminator chunks don't carry token counts directly — they're inferred from the transcript JSONL or from Claude Code's hooks. Where does that parsing live? Lean: in sesh's accountant, mirroring orch's current logic.
3. **Should orch-goal-pursue migrate or stay?** UX call. Lean: keep `orch-goal-pursue` as a thin wrapper that adds orch-specific session env-var setup; the underlying primitive is sesh-ops.
4. **Cross-harness token accounting**: codex/pi/gemini have different token reporting conventions. Sesh's accountant needs adapter logic per harness, OR orch's adapters must normalize to a single envelope-header convention before publishing terminator. Lean: orch's adapters normalize (cleaner sesh impl).

## Risks

- **Sesh team disagreement on direction**: if sesh wants goals as a side concern, not an owned primitive. Mitigation: surface the proposal to the sesh side before implementing.
- **Cutover race**: if orch's daemon stops before sesh's starts, in-flight decrements lost. Mitigation: dual-write window (both daemons running for 1 week).
- **Token-count format drift across harnesses**: each harness's terminator metadata is different. Mitigation: normalize at the shim's adapter layer (orch's job before extraction); sesh's accountant assumes normalised input.

## Why not now

Dan explicitly noted he doesn't plan to implement this soon. Captured for:
- Future reference when goal accounting needs investment
- Pre-discussion with sesh team about ownership
- Documenting the architectural intent so we don't accidentally entrench the current shape further

## Effort estimate

~2-3 weeks across both repos:
- 1 week sesh-side: accountant daemon + tests + docs
- 1 week orch-side: removal + wrapper rewrites + bench validation
- 1 week cutover + dual-write verification + monitoring
