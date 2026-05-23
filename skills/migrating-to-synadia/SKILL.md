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
| #58 | New `orch` CLI verb for Synadia prompt | merged |
| #94 | Retire legacy bridge + fs-marker hooks + legacy listener | **merged** |

**Current state (as of #94):** The Synadia path is the only path. The legacy
fs-marker hooks (`orch-stop-marker.sh`, `orch-notify-marker.sh`,
`orch-session-jsonl.sh`), per-harness NATS-publish hooks
(`orch-nats-publish-*.sh|ts`), the comms bridge daemon
(`orch-nats-bridge-in`), and the fs-watch listener (`orch-listen`) have all
been deleted. Skills that still reference them are obsolete — use the
translation table below.

## Side-by-side translation table

The five operator-facing primitives and their Synadia-native replacements.

| Legacy primitive | What it did | Replacement | Notes |
|---|---|---|---|
| `orch-tell %NNN "hello"` | Injected prompt via tmux `send-keys` | `orch-tell %NNN "hello"` (now publishes to `agents.prompt.cc.<owner>.pct<N>` via the shim) | The CLI name is unchanged — internals switched to bus publish. Caller MUST read the subject off the endpoint record (§4.3), not construct it from the pane id. |
| `orch-listen [--stream]` | `fswatch` loop over `~/.cache/orch-stop/*.event` marker files | `nats sub 'agents.>' --raw` (wrap in Monitor for push-notifications) | Subscribe to `agents.events.>` for typed chunks, `agents.hb.>` for heartbeats. Subject namespacing: `<plugin>.<harness>.<owner>.pct<N>`. |
| `orch-peek [pane...]` | Read `~/.cache/orch-registry/<pane>.json` | `nats req '$SRV.INFO.agents'` (CLI wrapper: `orch-peek`) | Returns all live shim instances; filter by `metadata.pane_id` for a single worker. `nats micro info agents` is the human-readable alias. |
| `orch-subscribe <peer>` | Worker-side fswatch daemon that injected `[peer event]` prompts | Subscribe directly to the bus from the worker's wrap shell (recipe in `orch-driver`). No first-class CLI replacement. |
| `orch-current-jsonl` | Read sidecar mapping `~/.orch/sessions/<pane>.json` written by the SessionStart hook | Read `metadata.transcript_path` from `$SRV.INFO.agents` — the shim's claudecode adapter advertises it. |
| `orch-register <pane> ...` | Wrote `~/.cache/orch-registry/<pane>.json` | No-op stub (shim auto-registers on `$SRV.INFO.agents`). The stub remains so legacy callers don't error. |

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
```

### Broadcast a question to all workers

```sh
# Discover all live panes, send in parallel
nats req '$SRV.INFO.agents' '' | jq -r '.services[].endpoints[].subject' | \
  xargs -P0 -I{} nats req '{}' "what did you last complete?"
```

### Watch for a worker becoming idle (turn-end)

```python
Monitor(
    command="nats sub 'agents.>' --raw",
    description="Synadia Agent Protocol events",
    persistent=True,
)
# each line: a typed chunk JSON. Turn-end appears as
# {"type":"status","data":"ack"} followed by an empty-body terminator.
```

For heartbeat-only liveness:


```sh
nats sub 'agents.hb.>'   # fires every 15s per live pane
```

### Spy on a session

```sh
orch-spy operator "audit operator session for the last hour"
# spy pane is auto-registered on the bus; its own heartbeats appear on
# agents.hb.cc.<owner>.pct<spy-N>; observable by any NATS subscriber.
```

## Skill update checklist

For operators who maintain skills that embed legacy primitives. Run this
checklist against each skill body.

1. **Grep for legacy primitives.** Search the skill body for `orch-listen`,
   `orch-subscribe`, `orch-register`, `orch-current-jsonl`, `orch-nats-bridge-in`,
   `orch-stop-marker`, `orch-notify-marker`, `orch-nats-publish`,
   `~/.cache/orch-stop`, `~/.cache/orch-notify`, `fswatch ... orch-stop`,
   `~/.cache/orch-registry`.

2. **For each hit, apply the replacement from the translation table.**

3. **Update the "Tools" or cheat-sheet table** in the skill body to reflect
   the new subjects and CLI verbs.

4. **Verify** by reading the updated skill body and confirming no legacy
   primitive remains.

5. **Commit** with `refactor(skills): update <skill-name> for Synadia cutover`.

## For claude-code: the Synadia channel plugin is the default bridge

Validated 2026-05-19, default-flipped in #182 (Proposal 0010 Phase A).
**`synadia-ai/synadia-agents/agents/claude-code`** is a NATS channel plugin
that bridges claude-code natively to the Synadia Agent Protocol bus.
`orch-spawn claude` now loads it by default; `orch-spawn` no longer launches
the shim sidecar for claude. Codex/pi/gemini still use the shim adapter
pattern until they get equivalent plugins (Phase B).

### Why

The shim's claude-code adapter (JSONL transcript tailing + `tmux send-keys` for input) hits a known bug class (sister-shim #11, #13, #15, #16). The Synadia channel plugin:

- Receives bus prompts as pushed turns via claude-code's `--dangerously-load-development-channels` mechanism (no tmux, no send-keys)
- Publishes responses via an MCP tool the plugin exposes to claude (no JSONL tailing, no symlink-path bugs)
- Uses session-name as the subject token (matches proposal 0009's design natively)
- Heartbeats at 5s cadence with full envelope (`Sesh-Envelope`, `Sesh-Role`, `Sesh-Attempt`, traceparent)
- v0.4.0 of the Synadia SDK (vs our shim's 0.3.0); supports attachments

### Setup (one-time, per machine)

Inside any claude-code session:

```
/plugin marketplace add synadia-ai/synadia-agents
/plugin install nats-channel@synadia-plugins
```

User-scope installs to `~/.claude/plugins/`. Done — every subsequent
`orch-spawn claude` picks up the plugin automatically.

### Default spawn (post-#182)

```sh
orch-spawn claude --project myapp --outfit engineer
# orch-spawn passes --dangerously-load-development-channels under the hood;
# no orch-agent-shim sidecar is launched. The plugin advertises itself on
# $SRV.INFO.agents from inside claude.
```

### Fallback: shim-adapter

Use `--bridge=shim-adapter` when the plugin isn't installed on the worker's
machine, or for environments that can't load development channels (e.g.
hardened CI runners). The shim adapter remains supported for the migration
window — both paths coexist:

```sh
orch-spawn claude --project myapp --bridge=shim-adapter
# Launches orch-agent-shim alongside the pane with the JSONL-tail adapter
# (pre-#182 default behaviour).
```

### Bridge defaults by agent (post-#182)

| Agent | Default `--bridge` | Notes |
|---|---|---|
| `claude` | `synadia-plugin` | Plugin loads inside claude; no shim sidecar |
| `codex` | `shim-adapter` | No Synadia plugin yet; rejects `--bridge=synadia-plugin` |
| `pi` | `shim-adapter` | No Synadia plugin yet; rejects `--bridge=synadia-plugin` |
| `gemini` | `shim-adapter` | No Synadia plugin yet; rejects `--bridge=synadia-plugin` |

### Verified end-to-end

```sh
nats req agents.prompt.cc.<owner>.<session> '{"prompt":"respond with: OK"}' \
  --replies=20 --reply-timeout=20s --timeout=45s
# → {"type":"response","data":"OK"} → nil body (terminator)
```

Round-trip in ~8s (one full claude turn). Visible in the pane:

```
← nats: respond with: OK            ← inbound prompt pushed as user turn
  Called plugin:nats-channel:nats   ← MCP tool call publishes response
⏺ Replied OK.
```

### What this replaces

- The shim's claude-code adapter (JSONL tailing + send-keys) is no longer
  on the default path; use `--bridge=shim-adapter` to opt back in.
- sister-shim issues #11 / #13 / #15 / #16 are moot for claude-code on the
  default path.

### Open questions / not yet validated

- codex / pi / gemini: no equivalent Synadia plugin shipped yet. Continue
  using the shim's adapters (or wait for Phase B's NATS↔ACP bridge).
- Per-session config: the `/nats-channel:configure` skill manages
  connection + session-override. Not strictly needed; the plugin reads
  `$NATS_URL` and the cwd-basename as session.

## Cross-references

- `docs/orch-agent-shim.md` — shim architecture, subject layout, §12 conformance map
- `docs/synadia-comparison.md` — four-layer architectural rationale
- `docs/nats-bridge.md` — historical record of the retired bridge (do not implement against this)
- `~/references/synadia-agent-sdk-docs/core-protocol.md` — Synadia Agent Protocol spec (v0.3)
- Tracking epic: **#55**
- Retirement PR: **#94**
