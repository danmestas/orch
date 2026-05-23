# Proposal 0009: stable slug as worker identity — operator migration guide

Closes [#181](https://github.com/danmestas/orch/issues/181). Consumes
[`@agent-ops/synadia-agent-shim@1.0.0`](https://www.npmjs.com/package/@agent-ops/synadia-agent-shim),
which gained `--instance-id` and dual-publish on slug-keyed subjects.

This guide is for operators upgrading orch across the 0009 cutover. It does
not change the design — it documents what behaviour changed and what action
(if any) you need to take per release.

## TL;DR

- New flag `orch-spawn --instance-id <slug>` (synonym `--slug` from PR #176).
- New env var `ORCH_INSTANCE_ID` exported into the spawned pane alongside
  the existing `ORCH_PANE_ID`.
- Subject tokens now publish on BOTH `pct<pane>` (legacy) and `<slug>`
  (new) — shim handles the dual-publish, gated by `ORCH_SLUG_DUAL_PUBLISH`
  (default on).
- Two-release deprecation window for `pct<pane>` subjects + `ORCH_PANE_ID`.

If you do nothing: workers keep spawning, the legacy pct-keyed track keeps
working, and you'll see one warning per spawn telling you to migrate.

## What changed

### orch-spawn

Three new flags:

- `--instance-id <slug>` — explicit stable identity. Same regex as `--slug`
  (`[a-zA-Z0-9._-]+`), passed straight through to the shim as
  `--instance-id <slug>`.
- `--slug <name>` (already existed, PR #176) — now treated as a synonym of
  `--instance-id`. The two share state; specifying both is fine (last one
  wins).
- `--force-slug` — bypass the new alias-file collision check. Without this,
  spawning a worker with a slug already in use by a different pane fails
  fast. With it, the alias file is overwritten and the previous worker on
  that slug loses its name (the worker itself keeps running).

One new derivation path:

- When neither `--instance-id` nor `--slug` is given, orch-spawn derives the
  slug from `$SESH_SESSION` (if the parent shell has it set and its value
  matches the slug regex). This is the auto-migrate path for workers spawned
  inside a sesh session.
- When no slug source resolves at all, orch-spawn emits a warning to stderr
  and proceeds with the legacy pct-keyed identity only. The next release
  will hard-fail in this case.

One new env var:

- `ORCH_INSTANCE_ID=<slug>` exported into the spawned pane alongside the
  existing `ORCH_PANE_ID`. Worker-side code that needs the identity should
  prefer `ORCH_INSTANCE_ID`; `ORCH_PANE_ID` remains exported for backward
  compatibility but is on the deprecation path.

### Shim (`@agent-ops/synadia-agent-shim@1.0.0`)

The shim now:

- Accepts `--instance-id <slug>` and reads `$ORCH_INSTANCE_ID` as a default.
- Registers slug-keyed endpoints: `agents.{prompt,status}.<token>.<owner>.<slug>`.
- Publishes heartbeats on the slug-keyed `agents.hb.<token>.<owner>.<slug>`
  subject in addition to the pct-keyed legacy subject.
- Adds `metadata.instance_id: "<slug>"` to its `$SRV.INFO.agents` reply.
- Honours `ORCH_SLUG_DUAL_PUBLISH=0` to opt out of the legacy track. Default
  is `1` (legacy on) for this release.

### Registry (internal/registry/)

- `Worker.InstanceID` now surfaces `metadata.instance_id` (the slug) when
  present; the per-process micro-service `info_response.id` (a UUID) is the
  fallback for shims that haven't been upgraded yet.
- Name resolution precedence is now `alias > metadata.instance_id (slug) >
  metadata.session > pct-form`. Operator-set aliases still win; the slug
  takes precedence over a session label below that.
- `Lookup` now resolves by `InstanceID` as a fallback when neither the pane
  id nor the display name match. Callers that pass either the alias name or
  the underlying slug reach the same worker.

## Required action per release

### This release (`0009` lands)

Nothing required. The legacy pct-keyed track stays live. You may opt in
early by:

1. Upgrading to `@agent-ops/synadia-agent-shim@1.0.0` globally (orch's
   `package.json` already pins `^1.0.0`; `npm install` picks it up).
2. Passing `--instance-id <slug>` on `orch-spawn` invocations you want to
   identify by name on the bus.

### Next release (deprecation warning ramps up)

- Spawning without a resolvable slug source becomes a hard error. Either
  pass `--instance-id`, pass `--slug`, set `$SESH_SESSION` in the parent
  shell, or run inside a sesh session.
- `ORCH_PANE_ID` is still exported but emits a deprecation warning on read
  from hooks / accessories. Worker-side code should switch to
  `ORCH_INSTANCE_ID`.

### Two releases out (legacy retired)

- `ORCH_SLUG_DUAL_PUBLISH=0` becomes the default. Shims stop publishing on
  `pct<pane>` subjects.
- `ORCH_PANE_ID` is removed from orch-spawn's exports. Workers that still
  reference it stop seeing it.
- `pct<pane>`-keyed lookups in `orch-tell` / `orch-peek` / `orch-spy`
  return "not found" once the shim is no longer publishing them.

## Collision behaviour

Spawning two workers with the same slug:

```bash
$ orch-spawn claude --instance-id lead-engineer
%121
$ orch-spawn claude --instance-id lead-engineer  # different pane
orch-spawn: slug 'lead-engineer' is already in use by %121 …
%122
$ orch-spawn claude --instance-id lead-engineer --force-slug
%123  # alias file now points to %123; %121 and %122 keep running but lose the alias
```

Re-spawning the same pane with the same slug (idempotent) is always allowed —
the alias-file entry already points at that pane.

## What you'll see in logs

```text
orch-spawn: no --instance-id / --slug given and $SESH_SESSION is unset/invalid — falling back to pct-keyed identity. Migrate to stable slug identity per docs/migration/0009-instance-id.md before the next release deprecates pct-keyed subjects.
```

If you see this on every spawn, decide whether to:

- pass `--instance-id <slug>` explicitly,
- set `SESH_SESSION` in the parent shell for one-shot inheritance,
- or run inside a sesh session (sesh sets `SESH_SESSION` automatically).

## Related

- Issue [#181](https://github.com/danmestas/orch/issues/181) — design rationale.
- Issue [#180](https://github.com/danmestas/orch/issues/180) — the umbrella that
  spawned 0009; persistence / layout abstraction lives there, independently.
- Issue [#176](https://github.com/danmestas/orch/issues/176) (PR) — original
  `--slug` flag.
- Shim repo: [github.com/danmestas/synadia-agent-shim](https://github.com/danmestas/synadia-agent-shim).
