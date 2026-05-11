---
status: accepted
date: 2026-05-10
---

# Use suit directly as the outfit manager; no orch-side outfit-pack abstraction

Orch needs configuration-as-code for workers (per-outfit system prompt, tool allowlist, skills, hooks, model, etc.). The agent-harness predecessor considered a "bundler-hook contract" that would let core consume any outfit-pack manager interchangeably. We rejected that abstraction for orch: suit (`@agent-ops/suit`) is the outfit manager. Orch calls `suit prepare --outfit X --cut Y` directly. No bundler-hook indirection, no `orch-outfits` sibling package.

## Considered options

- **Bundler-hook contract** (rejected): orch defines a hook spec (executable at `~/.config/orch/bundler` returning a bundle path); a separate `orch-outfits` package supplies the script that wraps suit. Premature generalization for a one-implementation use case.
- **Suit directly** (accepted): orch hardcodes `suit prepare` invocations when `--outfit` is passed. Errors helpfully if suit isn't installed.

## Why this is the right call for orch

- One implementation in flight (suit), no contention, no need to abstract.
- Outfit users install suit anyway. The bundler-hook indirection adds an install step (the bundler script) without removing any.
- If a non-suit outfit manager appears, the indirection is one PR away — replacing the direct call with a hook lookup is mechanical. The Ousterhout argument from agent-harness ADR-0001 applies: defer the abstraction until it has more than one consumer.

## Implications

- `orch-spawn --outfit X` requires `suit` on `PATH`. Error message points at the suit install when absent.
- `orch-spy` hardcodes `claude --outfit stasi --cut wait-watch` and requires suit + an outfit pack that defines those outfit names.
- The bash-wrapper deterministic tier (§6.2 of the README) is the only intercept point in core — there is no per-outfit substrate intercept above what suit's bundle provides.

## When to revisit

A second outfit manager exists, *and* a user wants to use both. Until then the indirection is pure cost.
