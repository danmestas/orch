---
name: migrating-to-synadia
description: Operator cheatsheet for the orch → Synadia Agent Protocol cutover. Use when asked to "migrate orch-tell", "what replaces orch-listen", "translate old orch CLI", "Synadia migration cheatsheet", "how do I use NATS instead of orch-tell", "what happens to the registry after #58", "update a skill for the new wire", "what changes in #59 / #60", or any variation of "old orch primitives → new equivalent". Covers the side-by-side translation table, common workflows shown in the Synadia-native form, and the step-by-step checklist for updating skills that embed legacy primitives.
---

# migrating-to-synadia

Operator cheatsheet for the protocol cutover from orch's ad-hoc wire to
the [Synadia Agent Protocol](~/references/synadia-agent-sdk-docs/core-protocol.md).
Reference this whenever a skill, a prompt, or a workflow uses pre-Synadia primitives
(`orch-tell`, `orch-listen`, `orch-peek`, the local registry) and you need to know the
current shape.

## Status

Tracking issue: **#55** (parent epic for the Synadia integration campaign).

| Child issue | Title | State |
|---|---|---|
| #70 | `orch-agent-shim` v1 — Synadia bridge in Go | **merged** |
| #72 | `orch-spawn` launches shim by default | **merged** |
| #57 / #68 | Docker test benches | **merged** |
| #58 | New `orch` CLI verb for Synadia prompt | open |
| #59 | `orch-listen` Synadia backend | open |
| #60 | Registry retirement / service-discovery migration | open |

The shim is live and every `orch-spawn`ed pane is registered on the Synadia bus.
The legacy primitives (`orch-tell`, `orch-listen`, `orch-peek`, local registry files)
are retired. Use the Synadia path exclusively.

## Side-by-side translation table

The five operator-facing primitives and their Synadia-native replacements.

| Legacy primitive | What it did | Current equivalent | Notes |
|---|---|---|---|
| `orch-tell %NNN "hello"` | Injected prompt into tmux pane via `send-keys` | `nats req agents.prompt.cc.<owner>.pct<N> "hello"` (or new `orch` verb — see #58) | Subject token for `%NNN`: replace `%` with `pct`, e.g. `%37` → `pct37`. Caller MUST read the subject off the endpoint record (§4.3), not construct it from the pane id. |
| `orch-listen [--stream]` | `fswatch` loop over `~/.cache/orch-stop/*.event` marker files | `nats sub 'agents.hb.>'` + chunk-stream sub (see #59) | Heartbeats fire on `agents.hb.cc.<owner>.pct<N>` every 15 s; Stop events are modelled as a status-chunk sequence. |
| `orch-peek [pane...]` | Read `~/.cache/orch-registry/<pane>.json` | `nats req '$SRV.INFO.agents'` | Returns all live shim instances; filter by `metadata.pane_id` for a single worker. `nats micro info agents` is the human-readable alias. |
| `~/.cache/orch-registry/<pane>.json` | Local JSON file per pane — source of truth for pane→agent→cwd→session | `nats req '$SRV.INFO.agents'` + `jq` on `metadata.pane_id` / `metadata.owner` | The shim publishes the same fields to `$SRV.INFO.agents` metadata. Local registry files are retired. |
| `orch-tell --force <pane> "/rename foo"` | Sent harness-specific slash command via tmux | Routes through `metadata.session` once the session-control channel is defined — see #59 | |

**Subjects quick-reference** (shim v1, §2.3 channel-plugin layout, `cc` token for claude-code):

```
agents.prompt.cc.<owner>.pct<N>    # send a prompt to pane %N
agents.status.cc.<owner>.pct<N>    # on-demand status (§8.7)
agents.hb.cc.<owner>.pct<N>        # periodic heartbeat (15 s cadence)
$SRV.INFO.agents                   # discover all live agents
$SRV.PING.agents                   # liveness probe
```

`<owner>` defaults to `$USER`; set `ORCH_OWNER` to override.

## Common workflows

Four canonical patterns in their current Synadia-native form.

### Spawn a worker and send it a prompt

```sh
PANE=$(orch-spawn claude --project myapp --outfit backend --cut executing)
# shim is launched automatically; discover the subject
INFO=$(nats req '$SRV.INFO.agents' '' 2>/dev/null | jq -r \
  ".services[].endpoints[] | select(.metadata.pane_id == \"$PANE\") | .subject")
# prompt over NATS; response is a streamed chunk sequence
nats req "$INFO" "summarize the auth module"
# TBD — full streaming reply handling: see #58 for the new CLI verb
```

### Broadcast a question to all workers

```sh
# Discover all live panes, send in parallel
nats req '$SRV.INFO.agents' '' | jq -r '.services[].endpoints[].subject' | \
  xargs -P0 -I{} nats req '{}' "what did you last complete?"
# TBD — see #58 for the orch verb wrapper that handles streaming replies
```

### Watch for a worker becoming idle (Stop event)

```sh
# Heartbeat subscription — fires every 15 s per live pane
nats sub 'agents.hb.>'
# TBD — see #59 for the orch-listen Synadia backend that models Stop as a status chunk
```

### Spy on a session

```sh
orch-spy operator "audit operator session for the last hour"
# spy pane is auto-registered on the bus; its own heartbeats appear on
# agents.hb.cc.<owner>.pct<spy-N>; observable by any NATS subscriber
# TBD — once #59 lands, spy Stop events arrive as chunk-stream terminations
```

## Skill update checklist

For operators who maintain skills that embed legacy primitives:

1. **Grep for legacy primitives.** Search the skill body for `orch-tell`, `orch-listen`,
   `orch-peek`, `orch-registry`, `orch-register`, `orch-subscribe`.

2. **For each hit, apply the replacement:**
   - `orch-tell <pane> <prompt>` → `nats req agents.prompt.cc.<owner>.pct<N> <prompt>`
     (or new `orch` verb — see #58 announcement).
   - `orch-listen [--stream]` → `nats sub 'agents.hb.>'` + chunk-stream sub (see #59);
     Monitor wrapper stays — only the inner command changes.
   - `orch-peek` → `nats micro info agents` or `nats req '$SRV.INFO.agents'`.
   - Registry file reads → `nats req '$SRV.INFO.agents'` + `jq` filter
     on `metadata.pane_id` / `metadata.owner`.
   - `orch-register` calls → remove; the shim self-registers on spawn.
   - `orch-subscribe` → TBD — see #59 (worker-side push).

3. **Update the "Tools" or cheat-sheet table** in the skill body to reflect the new subjects
   and CLI verbs.

4. **Verify** by reading the updated skill body and confirming no legacy primitive remains
   without a fully resolved replacement.

5. **Commit** with `refactor(skills): update <skill-name> for Synadia cutover (#58 #59)`.

## Cross-references

- `docs/orch-agent-shim.md` — shim architecture, subject layout, §12 conformance map
- `docs/synadia-comparison.md` — four-layer architectural rationale
- `~/references/synadia-agent-sdk-docs/core-protocol.md` — Synadia Agent Protocol spec (v0.3)
- Tracking epic: **#55**
- CLI shape: **#58** (new `orch` verb), **#59** (`orch-listen` backend), **#60** (registry retirement)
