# orch-agent-shim

A single Go binary that wraps an arbitrary agent CLI and exposes it via
the [Synadia Agent Protocol v0.3](https://github.com/synadia-io/agents)
on a sesh hub (or standalone NATS server). v1 ships with one adapter
(`claude-code`). The keystone for orch's adoption of Synadia.

## Why

orch already runs panes that publish events to NATS via the
`hooks/orch-nats-publish-*.sh` scripts. Those scripts are ad-hoc — a
4-subject convention that orch invented in `docs/nats-bridge.md` —
and they're emit-only: callers can't *prompt* a pane over NATS, only
observe it.

Synadia's protocol fixes both gaps: callers discover panes via
`$SRV.INFO.agents`, prompt them on a documented subject, and receive
typed streamed chunks back. Heartbeats and a status request/reply
endpoint give first-class liveness. The shim is the bridge between
orch's existing pane lifecycle (tmux + marker files + JSONL transcripts)
and that protocol.

The shim is **additive** to v1. Marker files and the existing publish
hooks keep working; `orch-tell` and `orch-listen` are unchanged. The
shim makes every spawned pane *additionally* discoverable on the
Synadia bus.

## Architecture

```
            ┌─────────────────────────────────────────────────────────┐
            │  caller (orch-tell v2 / Synadia SDK / nats req / ...)    │
            └─────────────────────────────────────────────────────────┘
                          │ $SRV.INFO.agents  + agents.prompt.cc.<owner>.pct<pane>
                          ▼
            ┌─────────────────────────────────────────────────────────┐
            │           NATS micro service `agents`                    │
            │                                                          │
            │   shim.go    ── registration · chunk encoding ·          │
            │                  heartbeat · prompt dispatch              │
            │   adapter.go ── Adapter interface (OnPrompt, Events,     │
            │                  Close) and Chunk wire types              │
            └─────────────────────────────────────────────────────────┘
                          │
                          ▼
            ┌─────────────────────────────────────────────────────────┐
            │  claudecode/cc.go      sibling background process         │
            │                                                          │
            │  • tails ~/.claude/projects/<enc-cwd>/<sid>.jsonl         │
            │    → emits one `response` chunk per assistant text block │
            │    + `thinking` / `tool_use` chunks for other blocks     │
            │  • watches ~/.cache/orch-stop/<pane>.event               │
            │    → emits terminator → closes active stream             │
            │  • watches ~/.cache/orch-notify/<pane>.notify            │
            │    → emits §7 query chunk                                │
            │  • inbound prompt → tmux send-keys -l + Enter            │
            └─────────────────────────────────────────────────────────┘
                          │
                          ▼
            ┌─────────────────────────────────────────────────────────┐
            │  tmux pane: `claude --dangerously-skip-permissions ...`  │
            └─────────────────────────────────────────────────────────┘
```

## Modules

| File                                  | Purpose                                                                                |
|---------------------------------------|----------------------------------------------------------------------------------------|
| `cmd/orch-agent-shim/main.go`         | Flag/env parsing, adapter selection, signal plumbing, `shim.Run` invocation.            |
| `internal/shim/shim.go`               | Service registration, chunk encoding, heartbeat loop, prompt dispatcher, status reply. |
| `internal/shim/adapter.go`            | `Adapter` interface and `Chunk` wire types.                                             |
| `internal/adapter/claudecode/cc.go`   | claude-code adapter: JSONL tail, marker watch, tmux send-keys.                          |

## Configuration

CLI exposes two required flags; everything else falls back to env vars,
then resolved defaults.

| Flag       | Env             | Fallback                                  | Notes                                                      |
|------------|-----------------|-------------------------------------------|------------------------------------------------------------|
| `--agent`  | —               | (required)                                | `claude-code` or `claude` (alias) in v1.                   |
| `--pane`   | —               | (required)                                | Raw tmux pane id, e.g. `%37`.                              |
| `--owner`  | `ORCH_OWNER`    | `$USER` / passwd lookup                   | Lands in metadata.owner.                                   |
| `--session`| `SESH_SESSION`  | `""` (omitted from metadata)              | Marks the agent as session-aware per §3.2.                 |
| `--nats`   | `NATS_URL`      | `~/.sesh/hub.url` → `nats://127.0.0.1:4222` | URL resolution per `shim.ReadNATSURL`.                     |
| `--outfit` | `ORCH_OUTFIT`   | `""`                                      | orch-specific metadata (forward-compat per §12).           |
| `--role`   | `ORCH_ROLE`     | `worker`                                  | orch-specific metadata.                                    |
| `--cwd`    | —               | `tmux display-message -p '#{pane_current_path}'` | Used by the claude-code adapter to find the transcript.   |
| `--interval`| —              | `30s`                                     | Heartbeat cadence. Clamped to ≥ 1s per §8.2.               |

## Subjects

The shim follows the §2.3 channel-plugin default layout, using `cc` as
the abbreviated subject token for claude-code (Synadia convention).
The raw `%`-bearing pane id is preserved in `metadata.pane_id`; the
subject form replaces `%` with `pct`.

| Verb       | Subject                                              |
|------------|------------------------------------------------------|
| Prompt     | `agents.prompt.cc.<owner>.pct<n>`                    |
| Status     | `agents.status.cc.<owner>.pct<n>`                    |
| Heartbeat  | `agents.hb.cc.<owner>.pct<n>`                        |

Discovery happens through `$SRV.INFO.agents` (and `$SRV.PING.agents`
for liveness probes) — callers MUST read the subject off the endpoint
record (§2.1), not construct it from identity (§12 caller checklist).

## §12 conformance map

| §12 requirement                                                        | Implemented in                                         |
|-------------------------------------------------------------------------|--------------------------------------------------------|
| Registers as `agents` micro service                                     | `shim.go: start` → `micro.AddService` with `name=agents`. |
| `metadata.agent / owner / protocol_version` declared                    | `shim.go: serviceMetadata`. Protocol pinned to `"0.3"`. |
| `metadata.session` added when session-aware                             | `serviceMetadata`: emitted iff `cfg.Session != ""`.    |
| `prompt` endpoint with queue group `agents`, `subject` agent-chosen      | `start: AddEndpoint("prompt", ..., WithEndpointQueueGroup("agents"))`. |
| `prompt` endpoint metadata: `max_payload`, `attachments_ok`             | `start: WithEndpointMetadata({max_payload, attachments_ok})`. |
| `status` endpoint with queue group `agents`, §8.3 heartbeat-shaped reply | `start: AddEndpoint("status")` + `handleStatus`.       |
| Accepts plain-text + JSON envelopes                                     | `parseEnvelope` (discrimination on leading `{`).        |
| Rejects malformed / empty / oversize / attachments-when-disallowed → 400 | `handlePrompt: respondError(400, …)`.                  |
| Tolerates and preserves unknown envelope fields                         | `requestEnvelope` ignores unknown keys via `encoding/json`. |
| `ack` is first chunk on reply subject (§6.4)                            | `handlePrompt: publishChunk(reply, NewStatusChunk("ack"))`. |
| Typed chunks `{type, data}` in publication order                        | `encodeChunk` + `eventPump`.                            |
| Empty-payload headerless terminator (§6.5)                              | `publishTerminator` (uses `nc.Publish(reply, nil)`).    |
| Errors precede terminator with §9 headers (pre-ack AND mid-stream)      | `respondError` (or `publishErrorOnReply`) + `publishTerminator`. Every error path emits exactly two messages: the header-bearing signal, then the empty terminator. |
| Heartbeats on `agents.hb.<agent>.<owner>.<name>` at configured cadence  | `heartbeatLoop` + `publishHeartbeat`.                   |
| All §8.3 fields in heartbeat payload                                    | `buildHeartbeat` / `heartbeatPayload`.                  |
| Responds to `$SRV.PING.agents` / `$SRV.INFO.agents`                     | Handled by `nats.go/micro` framework.                   |
| Mid-stream queries conform to §7                                        | `claudecode/cc.go: markerLoop` → `shim.NewQueryChunk`.  |
| `Nats-Service-Error-Code` from §9.2 taxonomy on errors                  | `respondError` / `publishErrorOnReply` set code + body. |

## Lifecycle

The shim is a sibling background process of the pane it wraps —
lifetime bound to the pane via the parent shell's death (and an
optional `wait` on a sentinel pid in orch-spawn's WRAP).

Startup ordering (matches §8.2's "agents SHOULD begin heartbeats only
after service registration" guidance):

1. Dial NATS.
2. Register the `agents` micro service with metadata + endpoints.
3. Call `adapter.Start(shimCtx)` — binds the adapter's long-lived
   watchers (file tailers, marker watchers) to the shim's lifetime
   context, NOT to a per-prompt context. This is the contract the
   Adapter interface documents: prompt-scope cancellation must never
   dismantle the adapter.
4. Start the heartbeat loop (immediate publish, then every `interval`).
5. Start the adapter's event pump.
6. Block on `ctx.Done()`; on signal, drain the connection and close
   the adapter. `Close()` MUST close the channel returned by `Events()`
   so the event pump exits; `Close` is idempotent.

## Wire-compat

Tests in `test/wire-compat/` drive the shim from a Synadia-protocol
caller built against `@synadia-ai/agents` (the upstream TS SDK), so
any wire drift from the spec surfaces immediately. The smoke runner is
`test/test-orch-agent-shim.sh`.

## Open work (out of scope for this PR)

- **codex / pi / gemini adapters.** Plans 11-13. Each is an
  `adapter/<name>/<name>.go` with the same three-method interface.
- **`orch-spawn --with-shim` default flip.** v1 defaults the flag off;
  Plan 8 turns it on once the shim has burned in.
- **Marker-file retirement.** The Stop hook keeps writing markers for
  Plan-10 listeners. Plan 9 (orch-tell over Synadia) is what motivates
  removing the dependency.
- **Per-query reply routing.** The adapter emits notify markers as §7
  query chunks but doesn't wire the caller's reply back through
  `tmux send-keys`. That requires a reply-subject subscriber and is
  Plan 9 territory.
