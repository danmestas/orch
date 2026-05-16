# orch vs Synadia Agent Protocol — and where each layer belongs

Companion to `sesh/docs/synadia-comparison.md`. This document analyzes orch
(the agentic-layer control plane that consumes sesh's substrate) against the
Synadia Agent Protocol and SDKs, and proposes a clean three-way division of
responsibilities.

## The four-layer cake (refined)

```
┌─────────────────────────────────────────────────────────────┐
│ Operator workflows · goals · classifier policy · dark factory │  application
├─────────────────────────────────────────────────────────────┤
│ ORCH: agent control plane                                    │  control plane
│   • spawn/place/govern: tmux/docker/ssh/wasm executors       │
│   • outfit bundles (config-as-code via suit)                 │
│   • role taxonomy: worker / observer / operator              │
│   • per-harness hook adapters                                │
│   • operator UX: orch-listen, orch-peek, orch-spy, skills    │
├─────────────────────────────────────────────────────────────┤
│ SYNADIA PROTOCOL: addressable agents on NATS           ADOPT │  wire contract
│   • $SRV.INFO.agents discovery + metadata                    │
│   • prompt + status endpoints                                │
│   • typed chunks: response/status/query                      │
│   • heartbeats + instance_id + queue group                   │
├─────────────────────────────────────────────────────────────┤
│ SESH: substrate                                              │  substrate
│   • hub-and-leaf NATS, embedded auto-spawn                   │
│   • session lifecycle, project-code routing                  │
│   • Fossil + autosync (artifacts)                            │
│   • scoped KV memory · task · goal · envelope                │
├─────────────────────────────────────────────────────────────┤
│ NATS micro + JetStream                                       │  transport
└─────────────────────────────────────────────────────────────┘
```

The middle layer is currently missing. orch reinvents fragments of it in
`docs/nats-bridge.md`. Adopting Synadia closes that gap and lets orch focus on
the control-plane work only orch can do.

## What orch implements today (catalog)

### Primitives (`bin/`)
| Concern | Today |
|---|---|
| Spawn | `orch-spawn <agent>` — tmux split + outfit + role; multi-executor sketched in `docs/multi-executor-workers.md` |
| Address | tmux pane id (`%37`) + alias file `~/.config/orch-aliases` |
| Send | `orch-tell` (publishes to `agents.prompt.…` via `$SRV.INFO.agents` discovery), `orch-ask` (= `orch-tell --collect`: streams response chunks until terminator) |
| Listen | `orch-listen` (one-shot or `--stream` over fswatch markers), `orch-subscribe` (peer push) |
| Track | `orch-register` (per-pane JSON cache), `orch-claim-operator`, `orch-peek`, `orch-spy` |
| Lifecycle | `orch-up`, `orch-down`, `orch-bundle-gc` |
| Goal harness | `orch-goal-pursue`/`orch-goal-status` + Stop & SessionStart hooks + `goal-complete` skill |

### Two IPC mechanisms, both ad-hoc
1. **Filesystem markers + fswatch** (default). `~/.cache/orch-stop/*.event` and `*.notify`. Synchronous, no daemon. Reliable on a single machine.
2. **NATS bridge** (additive, landed in #49). Four subjects, schema-by-convention:
   - **Outbound** (per-harness hook scripts): `orch.stop.<num>`, `orch.notify.<num>`, `orch.events.<num>` (raw Claude Code JSONL transcript)
   - **Inbound** (one subscriber): `orch.tell` with JSON body `{pane:"%37", prompt:"…"}`
   - The doc explicitly notes two ergonomic scars: NATS subject tokens can't contain `%`, and `nats sub --translate` doesn't expose `$NATS_SUBJECT` to the translator.

### Per-harness coverage (today)
| Harness | Stop | Notification | Transcript tail |
|---|---|---|---|
| claude-code | ✓ | ✓ | ✓ |
| codex | ✓ | ✗ no event | ✓ |
| pi | ✓ | ✗ no event | ✓ |
| gemini | ✓ (as `AfterAgent`) | ✓ | ✗ deferred (path encoding) |

Four parallel hook implementations (`.sh`/`.ts`) per event, gated on
`$ORCH_PANE_ID`, with `--timeout=1s` best-effort publishes.

## The diagnostic moment

orch's `docs/nats-bridge.md` is the smoking gun: orch already invented
fragments of an agent protocol (publish hooks for events/notify/stop, single
subscriber for inbound prompts), and the doc itself catalogues the
workarounds (`%` not allowed in subject tokens; per-harness Stop event name
divergence; gemini path-encoding deferred). Synadia §2 (verb-first subjects)
and §6 (typed chunks) solve every one of those problems by construction.

## What orch should adopt from Synadia

### 1. Replace the 4-subject ad-hoc wire with Synadia §2 + §6 + §8

| Today | After |
|---|---|
| `orch.tell {pane, prompt}` | `agents.prompt.<agent>.<owner>.<pane>` (verb-first) |
| `orch.events.<num>` raw JSONL firehose | typed `response` chunks (and optional `tool_use`/`thought` chunks — §6.6 says unknown types are silently ignored, so orch can extend without breaking) |
| `orch.notify.<num>` | mid-stream `query` chunks (§7) — exactly the "agent is asking for input" semantic |
| `orch.stop.<num>` + marker | §6.5 zero-byte terminator + final `status` chunk |
| (none) | §8 heartbeats on `agents.hb.<agent>.<owner>.<pane>` → structural liveness without inference from missing Stop events |

Wins: typed envelope, pane-id-in-subject scar fixed, per-harness publisher
scripts collapse to one shape per stream type, codex/pi gain Notification
parity (the shim emits `query` chunks even when the underlying harness lacks
a native event).

### 2. Retire the registry — use `$SRV.INFO.agents` as the truth

`~/.cache/orch-registry/<pane>.json` exists to answer "what's running and
what's its agent type / cwd / role / outfit." That's exactly what Synadia §3
metadata + `$SRV.INFO.agents` answer natively. Each pane registers with:

```json
{
  "agent": "claude-code",
  "owner": "dmestas",
  "session": "<sesh-session-or-tmux-window>",
  "instance_id": "<framework-generated>",
  "metadata": {
    "pane_id": "%37",
    "outfit": "engineer:focused",
    "role": "worker",
    "cwd": "/Users/dmestas/projects/...",
    "harness": "claude-code"
  }
}
```

Discovery becomes `nats req '$SRV.INFO.agents'`. Stale entries vanish on
service shutdown. No GC needed.

### 3. Ship `orch-agent-shim` — one Go binary, four adapters

The single highest-leverage change orch can make. The shim:

- connects to `$NATS_URL` (sesh-published or standalone)
- registers as `agents` micro service with metadata above
- accepts prompts on its `prompt` endpoint and feeds them to the wrapped
  CLI (tmux send-keys for tmux executor; stdin for headless; native input
  mode for docker/ssh)
- runs a per-harness adapter that translates the CLI's idiosyncratic
  transcript or event stream into Synadia typed chunks:
  - claude-code adapter: tails JSONL, emits `response` chunks for messages,
    `query` chunks on Notification, terminator on Stop
  - codex adapter: reads codex event stream, emits chunks, synthesizes
    `query` from idle-with-prompt detection
  - pi adapter: subscribes to pi extension events, same shape
  - gemini adapter: handles `AfterAgent` and `Notification`, defers
    JSONL once path-encoding rules are mapped
- emits heartbeats on configured interval
- serves the `status` endpoint

**This single binary retires ~12 per-harness hook scripts** (`hooks/`,
`codex-hooks/`, `pi-extensions/`, `gemini-hooks/`). Every harness gets
Notification parity. The wire shape is uniform. New harness support is one
new adapter file, not three new hook scripts plus a settings snippet
fragment.

### 4. `orch-tell` is a thin Synadia caller (Plan 9 — landed in #59)

Today: `orch-tell` resolves `<pane|alias>` → Synadia subject via
`$SRV.INFO.agents`, publishes the prompt to `endpoints[name=="prompt"].subject`,
and (with `--collect`) consumes the response chunk stream until the
inactivity timeout fires. Keystroke injection is the implementation detail
of the shim (operator UX unchanged; wire under it is now standard).

`orch-ask` is `orch-tell --collect` — streams `response` chunk `.data` to
stdout as each chunk arrives.

### 5. Channel-plugin pattern matches orch's multi-executor proposal

orch's `docs/multi-executor-workers.md` describes executors: tmux, docker,
ssh, cf-worker, cf-durable-object, wasmtime, browser. Each runs the same
worker-bootstrap contract.

Synadia's `agents/{claude-code, openclaw, pi, hermes, open-agent}` is the
same idea on a different axis: harness plurality with a uniform wire.

These compose. orch contributes the **placement axis** (where the agent
runs); Synadia contributes the **harness axis** (which agent CLI). orch's
WASM phase 4-7 doesn't need to be invented — wire the existing Synadia
`agents/open-agent/` plugin (already has a `LocalSandbox` seam swappable
for `@vercel/sandbox`) as the cf-worker executor's harness.

### 6. Adopt mid-stream `query` chunks for the operator-attention pattern

Synadia §7 specifies: agent emits `{type:"query", data:{prompt, reply_subject}}`,
caller publishes on `reply_subject`, agent continues. This is exactly what
`orch.notify.<num>` is trying to be. With §7:

- The "codex/pi have no Notification event" gap closes — the shim emits
  `query` on idle-with-prompt detection.
- The operator's response back to the agent goes through a clean
  request/reply path, not "the operator types into the tmux pane."
- The Notification hook becomes a typed event with semantics, not a free-text
  ping.

### 7. Inherit `protocol_version` discipline (§11)

orch has no versioning today. As multi-executor lands and shims diversify,
this will bite. Synadia's MAJOR.MINOR-in-metadata convention is essentially
free once you adopt the registration shape.

## What Synadia can learn from orch

orch's design pressure surfaces gaps in the Synadia spec worth contributing
back upstream:

1. **Role taxonomy.** Synadia agents are symmetric. orch's
   worker/observer/operator distinction (with default-exclude observers
   from listeners, worker→observer redirection guard) is real for human-
   in-the-loop fleets. Propose: optional `metadata.role` filterable on
   discovery.

2. **Outfit / config-as-code identity.** Synadia metadata says *which*
   harness; not *which configuration*. Propose: optional
   `metadata.outfit` + `metadata.outfit_hash` for "show me all
   engineer-outfit panes."

3. **Third-party attestation of attention events.** §7 query chunks are
   agent-initiated. Some harnesses don't expose a "I'm waiting" event,
   but an *external observer* (orch's idle-detection in `orch-tell`)
   can see the pane is showing a prompt. A spec extension for "external
   observer can publish a synthetic query chunk on behalf of an agent"
   would close this gap formally.

4. **Multi-executor / placement metadata.** orch's executor abstraction
   (tmux/docker/ssh/wasm) is exactly what large-fleet operators want.
   Propose: an informative appendix on placement metadata
   (`executor: "tmux"|"docker"|"ssh"|"wasm"`, `host`, sandbox bounds).

5. **Hook-event-name canonicalization across harnesses.** The
   claude→gemini Stop→AfterAgent mismatch is real. orch already shipped
   a translation table. The Synadia plugin index could publish the
   canonical mapping per harness (claude `Stop` ↔ gemini `AfterAgent` ↔
   codex `(none — use stdout EOF)` ↔ pi `extension.afterTurn`).

## Where each functionality belongs (the precise division)

### Goes in (or stays in) sesh — substrate

Anything multiple agentic systems must *agree* on to share state:

- NATS server lifecycle (embedded auto-spawn, hub-and-leaf)
- Session container (`sesh up/down`, state JSON)
- Fossil repo + autosync
- Scoped KV memory (5 scopes, deterministic naming)
- Task records + CAS pull
- Goal records + state machine
- W3C traceparent envelope
- Project-code routing

### Moves from orch to Synadia — wire contract

orch is currently doing in-house what Synadia formalizes. Push down:

| orch concern | Synadia §  | After |
|---|---|---|
| Agent identity (`~/.cache/orch-registry/`) | §3 metadata | `metadata.agent/owner/session/instance_id` |
| Prompt injection (`orch.tell`) | §2 + §5 | `agents.prompt.…` endpoint |
| Turn-end (`orch.stop.<num>` + marker) | §6.5 | zero-byte terminator + final status chunk |
| Attention request (`orch.notify.<num>`) | §7 | `query` chunks |
| Transcript stream (`orch.events.<num>`) | §6.3 | typed `response` chunks |
| Liveness (today: inferred from missing Stop) | §8 | heartbeat + status endpoint |
| Discovery (today: registry walk) | §4 | `$SRV.INFO.agents` |

### Stays in orch — control plane

These are orch's unique value-add. Nothing else in the stack provides them:

- **Placement.** Multi-executor spawn (tmux/docker/ssh/wasm/browser).
  Outfit-bundle distribution. Tmux pane choreography, --position,
  --headless, suit prepare integration, bundle gc.
- **Governance.** Role taxonomy, worker→observer redirection refusal,
  operator-claim semantics.
- **Per-harness adaptation.** The shim's adapter layer — translating
  each agent CLI's hook events / transcript shape to Synadia chunks.
  Per-harness install snippets, gemini AfterAgent quirk, codex first-run
  bypass, gemini path encoding, etc. This is **where orch's complexity
  belongs** — at the edge between idiosyncratic CLIs and the uniform
  wire above it.
- **Operator UX.** `orch-listen --stream` (Monitor-wrapped push), the
  installed skill suite (`assume-orch`, `orch-driver`, `goal-complete`,
  `tmux-agent-panes`), the operator/worker visibility tooling.
- **Goal-harness hooks.** Token accounting, SessionStart context
  injection, completion-audit skill. sesh's goal-management spec
  explicitly says these are harness-side; orch is that harness side.
- **Fleet topology / classifier policy / audit log** (roadmap).

**Rule of thumb**: if the concern is *"how does process X discover and
talk to process Y over NATS"*, Synadia. If *"how does a human operator
drive a tmux-visible fleet of LLM CLIs and survive"*, orch. If *"how do
multiple processes share state across reboots"*, sesh.

## Proposed PR roadmap for orch

| # | PR | Scope | Why this order |
|---|---|---|---|
| 1 | `orch-agent-shim v1` — Go binary, claude-code adapter only | `cmd/orch-agent-shim/`, ~500 LOC | Proves the architecture; makes `$SRV.INFO.agents` light up; retires 3 claude hook scripts |
| 2 | orch-spawn launches the shim alongside the pane | `bin/orch-spawn` patch | Wires every new pane onto the bus |
| 3 | `orch-tell` resolves via `$SRV.INFO.agents` and publishes to `agents.prompt.…` | `bin/orch-tell` patch | Operator UX unchanged; wire underneath is now standard |
| 4 | Retire `~/.cache/orch-registry/`; `orch-register` becomes a no-op for hook-registered harnesses | `bin/orch-register` patch, doc | Single source of truth |
| 5 | codex adapter | shim adapter file | Notification parity for codex |
| 6 | pi adapter | shim adapter file | Same for pi |
| 7 | gemini adapter | shim adapter file | Closes the matrix |
| 8 | Migrate goal-harness Stop hook to read terminator events | `hooks/orch-goal-stop-account.sh` patch | Cleaner edge; same logic |
| 9 | Wire `agents/open-agent/` (Synadia) as orch's WASM executor | `docs/multi-executor-workers.md` phase 4 | Phase 4-7 reuses Synadia plugins instead of inventing |
| 10 | Upstream PRs to `synadia-agent-sdk-docs`: role metadata, outfit metadata, third-party attestation, placement appendix | docs PR | orch's pressure shapes the spec |

The 80/20 first step is PR 1 alone. It validates the layering, lights up
discovery for free, and every later PR slots in naturally.

## What NOT to do

1. **Don't have orch implement its own agent protocol.** That's the
   nats-bridge trap. Adopt Synadia; let it standardize the wire.
2. **Don't push outfits / roles / executor abstraction down into sesh.**
   Those are control-plane concerns. They stay in orch and travel as
   metadata over the Synadia wire.
3. **Don't fold Synadia into either sesh or orch.** Keep all three layers
   separable — sesh runs the bus, Synadia speaks on it, orch tells the
   speakers what to do.
4. **Don't deprecate marker files all at once.** Same-machine,
   no-NATS workflows are real and the markers are fast. Keep them as a
   fallback the shim can also tail.

## References

- `~/references/synadia-agents/` — TS + Python SDKs, channel plugins
- `~/references/synadia-agent-sdk-docs/core-protocol.md` — protocol spec v0.3
- `../sesh/docs/synadia-comparison.md` — companion analysis from the substrate side
- `docs/nats-bridge.md` — orch's current bridge (the gap this proposal closes)
- `docs/multi-executor-workers.md` — orch's executor plurality plan (composes naturally with Synadia channel plugins)
- `docs/working-with-sesh.md` — orch ↔ sesh practitioner guide
