---
name: migrating-to-synadia
description: Operator cheatsheet for the orch → Synadia Agent Protocol cutover. Use when asked to "migrate orch-tell", "what replaces orch-listen", "translate old orch CLI", "Synadia migration cheatsheet", "how do I use NATS instead of orch-tell", "what happens to the registry after #58", "update a skill for the new wire", "dual-emit window", "what changes in #59 / #60", or any variation of "old orch primitives → new equivalent". Covers the side-by-side translation table, dual-emit window rules, common workflows shown both ways, and the step-by-step checklist for updating skills that embed today's primitives.
---

# migrating-to-synadia

Operator cheatsheet for the protocol cutover from orch's ad-hoc wire to
the [Synadia Agent Protocol](~/references/synadia-agent-sdk-docs/core-protocol.md).
Reference this whenever a skill, a prompt, or a workflow uses pre-Synadia primitives
(`orch-tell`, `orch-listen`, `orch-peek`, the local registry) and you need to know the
post-cutover shape.

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

**Current state (as of this PR):** The shim is live and every `orch-spawn`ed pane
is already registered on the Synadia bus. The *caller* side — the commands an
operator types to reach a worker — still defaults to the legacy primitives.
The dual-emit window lets both paths coexist until #58/#59/#60 close.

## Side-by-side translation table

The five operator-facing primitives that change shape when #58/#59/#60 land.

| Legacy primitive | What it does today | Post-cutover equivalent | Notes |
|---|---|---|---|
| `orch-tell %NNN "hello"` | Injects prompt into tmux pane via `send-keys` | `TBD — see #58` (`nats req agents.prompt.cc.<owner>.pct<N> "hello"` or new `orch` CLI verb) | Subject token for `%NNN`: replace `%` with `pct`, e.g. `%37` → `pct37`. Caller MUST read the subject off the endpoint record (§4.3), not construct it from the pane id. |
| `orch-listen [--stream]` | `fswatch` loop over `~/.cache/orch-stop/*.event` marker files | `TBD — see #59` (`nats sub 'agents.hb.>'` + chunk-stream sub) | Heartbeats fire on `agents.hb.cc.<owner>.pct<N>` every 15 s; Stop events will be modelled as a status-chunk sequence. |
| `orch-peek [pane...]` | Reads `~/.cache/orch-registry/<pane>.json` | `nats req '$SRV.INFO.agents'` | Returns all live shim instances; filter by `metadata.pane_id` if you want a single worker. `nats micro info agents` is the human-readable alias. |
| `~/.cache/orch-registry/<pane>.json` | Local JSON file per pane — source of truth for pane→agent→cwd→session | `TBD — see #60` (service-discovery query, retired after sunset) | The shim already publishes the same fields to `$SRV.INFO.agents` metadata; the local file is kept during the dual-emit window for back-compat. |
| `orch-tell --force <pane> "/rename foo"` | Sends harness-specific slash command via tmux | Harness-specific; TBD — see #59 | `/rename` will route through `metadata.session` once the session-control channel is defined. |

**Subjects quick-reference** (shim v1, §2.3 channel-plugin layout, `cc` token for claude-code):

```
agents.prompt.cc.<owner>.pct<N>    # send a prompt to pane %N
agents.status.cc.<owner>.pct<N>    # on-demand status (§8.7)
agents.hb.cc.<owner>.pct<N>        # periodic heartbeat (15 s cadence)
$SRV.INFO.agents                   # discover all live agents
$SRV.PING.agents                   # liveness probe
```

`<owner>` defaults to `$USER`; set `ORCH_OWNER` to override.

## Dual-emit window

During the migration, **both paths work simultaneously**. The shim is additive:
`orch-tell` and marker files keep working; the shim makes every pane *additionally*
reachable on the Synadia bus.

```
   operator
      │
      ├─── orch-tell %37 "hello" ──────────► tmux send-keys (legacy path, still works)
      │
      └─── nats req agents.prompt.cc.… ────► shim → orch-tell internally
```

After the sunset date (TBD — will be announced when #59 and #60 close), the legacy
`orch-tell` / marker-file path will be soft-deprecated. A warning period precedes
removal; no hard cutover without operator notice.

**During the dual-emit window:**
- Skills that already reference `orch-tell` / `orch-listen` / `orch-peek` remain correct.
- New skills should prefer the Synadia path only once #58/#59 are merged and the CLI shape is stable.
- Do NOT update existing skill bodies before #58/#59 land — the new CLI shape isn't finalized.

## Common workflows translated

Four canonical patterns, shown legacy → Synadia. The Synadia column uses
`nats` CLI as a stand-in; the new `orch` verb (TBD — #58) will wrap it.

### Spawn a worker and ask it a question

**Legacy:**
```sh
PANE=$(orch-spawn claude --project myapp --outfit backend --cut executing)
orch-tell "$PANE" "summarize the auth module"
REPLY=$(orch-ask "$PANE" "summarize the auth module")
```

**Synadia (during dual-emit window — shim already up after orch-spawn):**
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

**Legacy:**
```sh
for PANE in %37 %38 %39; do
  orch-tell "$PANE" "what did you last complete?" &
done
wait
```

**Synadia:**
```sh
# Discover all live panes, send in parallel
nats req '$SRV.INFO.agents' '' | jq -r '.services[].endpoints[].subject' | \
  xargs -P0 -I{} nats req '{}' "what did you last complete?"
# TBD — see #58 for the orch verb wrapper that handles streaming replies
```

### Watch for a worker becoming idle (Stop event)

**Legacy:**
```python
Monitor(command="orch-listen --stream", description="harness events", persistent=True)
# each event: {"event_file":..., "pane_id":..., "ext":"event", ...}
```

**Synadia:**
```sh
# Heartbeat subscription — fires every 15 s per live pane
nats sub 'agents.hb.>'
# TBD — see #59 for the orch-listen Synadia backend that models Stop as a status chunk
```

### Spy on a session

**Legacy:**
```sh
orch-spy operator "audit operator session for the last hour"
```

**Synadia (shim path, same spawn mechanics — shim registers the spy too):**
```sh
orch-spy operator "audit operator session for the last hour"
# spy pane is auto-registered on the bus; its own heartbeats appear on
# agents.hb.cc.<owner>.pct<spy-N>; observable by any NATS subscriber
# TBD — once #59 lands, spy Stop events arrive as chunk-stream terminations
```

## Skill update checklist

For operators who maintain skills that embed today's primitives. Run this checklist
against each skill body **after #58 and #59 are merged** (not before — the new CLI
shape isn't stable until then).

1. **Grep for legacy primitives.** Search the skill body for `orch-tell`, `orch-listen`,
   `orch-peek`, `orch-registry`, `orch-register`, `orch-subscribe`.

2. **For each hit, determine the pattern:**
   - `orch-tell <pane> <prompt>` → replace with the new `orch` verb (see #58 announcement)
     or `nats req agents.prompt.cc.<owner>.pct<N> <prompt>`.
   - `orch-listen [--stream]` → replace with the new backend (see #59 announcement);
     Monitor wrapper stays — only the inner command changes.
   - `orch-peek` → replace with `nats micro info agents` or `nats req '$SRV.INFO.agents'`.
   - Registry file reads → replace with a `nats req '$SRV.INFO.agents'` + `jq` filter
     on `metadata.pane_id` / `metadata.owner`.
   - `orch-register` calls → remove; the shim self-registers on spawn, no manual step.
   - `orch-subscribe` → TBD — see #59 (worker-side push is the open part).

3. **Update the "Tools" or cheat-sheet table** in the skill body to reflect the new subjects
   and CLI verbs.

4. **Add a cross-ref to this skill** in the deprecation notice (already added to
   `orch-driver`, `orch-suiting`, `assume-orch`).

5. **Verify** by reading the updated skill body and confirming no legacy primitive remains
   without a corresponding "TBD — see #N" or a fully resolved replacement.

6. **Commit** with `refactor(skills): update <skill-name> for Synadia cutover (#58 #59)`.

## Cross-references

- `docs/orch-agent-shim.md` — shim architecture, subject layout, §12 conformance map
- `docs/synadia-comparison.md` — four-layer architectural rationale
- `~/references/synadia-agent-sdk-docs/core-protocol.md` — Synadia Agent Protocol spec (v0.3)
- Tracking epic: **#55**
- CLI shape: **#58** (new `orch` verb), **#59** (`orch-listen` backend), **#60** (registry retirement)
