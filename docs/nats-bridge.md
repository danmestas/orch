# NATS comms bridge (HISTORICAL)

> **Retired in orch#94 (2026-05-16).** This document describes the legacy
> NATS comms bridge — `bin/orch-nats-bridge-in`, the per-harness
> `orch-nats-publish-*` hooks, and the filesystem-marker hooks
> (`orch-stop-marker.sh`, `orch-notify-marker.sh`, `orch-session-jsonl.sh`).
> All of those have been deleted. The bridge has been superseded by the
> Synadia Agent Protocol path implemented in `orch-agent-shim` plus the
> per-harness adapters under `internal/adapter/`. See
> [`docs/orch-agent-shim.md`](orch-agent-shim.md) and
> [`docs/synadia-comparison.md`](synadia-comparison.md) for the live
> architecture. This file is preserved as the migration record.

**Status:** Retired. Originally landed via #49 (subscriber + 3 publish hooks) with multi-harness coverage (codex / pi / gemini publish-side) added alongside. Retired in #94 once the Synadia path subsumed the bridge.

A small adapter that lets a parent Claude Code session ("orchestrator") drive N spawned orch builder panes ("subagents") through NATS pub/sub in addition to orch's default tmux + filesystem-marker IPC. Marker behavior is preserved; NATS is additional fan-out.

The bridge consists of:

- **3 publish hooks** that fire from the orch builder side (Stop, Notification, SessionStart) and publish to NATS
- **1 subscriber daemon** that consumes prompts from NATS and dispatches them via the existing `orch-tell`

Total: ~250 LOC of shell.

---

## Why an alternative to tmux + fswatch IPC

Today's orch comms work fine for one-orchestrator-one-machine setups. They start to friction when:

- **Multiple subscribers want the same events.** fswatch on `~/.cache/orch-stop/` works for one listener; two listeners both watch the same dir and contend. NATS pub/sub fans out cleanly.
- **The orchestrator and workers aren't on the same host.** Filesystem-marker IPC is single-host. NATS leafs across hosts (especially via sesh's hub-and-leaf topology).
- **You want durable replay.** A late subscriber can't see Stop events that fired before it started. With JetStream behind `orch.events.*`, late subscribers replay from any point.
- **You want live transcript streaming.** Reading per-session JSONL from disk works. Streaming JSONL lines through NATS gives a multi-subscriber-friendly observability surface for fleets.

None of these block one-machine work — they expand the topology orch can run under.

---

## Wire shape

Subjects use the prefix `orch` by default. Override per-session via `ORCH_NATS_SUBJECT_PREFIX` env — this is the seam for session-scoped subjects when orch runs under a [sesh](https://github.com/danmestas/sesh) session container (`sesh.<session>.orch.*`).

### Outbound (orch → NATS) — three publish hooks

| Subject | Trigger | Body (JSON) |
|---|---|---|
| `orch.stop.<pane_num>` | Stop hook (assistant turn ends) | `{event:"stop", pane_id, session_id, cwd, ts_ns, ts_iso}` |
| `orch.notify.<pane_num>` | Notification hook (attention-needed) | `{event:"notify", pane_id, session_id, message, cwd, ts_ns, ts_iso}` |
| `orch.events.<pane_num>` | SessionStart hook → `tail -F` on transcript JSONL | one raw Claude Code transcript JSONL line per message |

`<pane_num>` is the numeric suffix of the tmux pane id (`%37` → `37`), because NATS subject tokens cannot contain `%`. The full `pane_id` (with `%`) is in the JSON body.

Each publish-hook gates on `$ORCH_PANE_ID` being set, so they no-op for non-orch Claude sessions — safe to install globally. Each publish has `--timeout=1s` so a missing or unreachable NATS server doesn't stall the host hook past its budget.

### Inbound (NATS → orch) — one subscriber

| Subject | Body (JSON) | Effect |
|---|---|---|
| `orch.tell` | `{pane:"%37", prompt:"…"}` | `orch-tell %37 -` with prompt piped in |

Single fixed subject + JSON-shape body, not per-pane subjects. Two CLI-shaped reasons:

- NATS subject tokens can't contain `%`, so per-pane subjects need lossy encoding of pane ids.
- `nats sub --translate` receives the message body on stdin but does NOT expose `$NATS_SUBJECT` to the translator command — so a single subscriber can't reliably pair subject with body in shell. JSON-body sidesteps both issues.

Outbound (publishing) keeps subject-per-pane because the publisher already knows the subject — no parsing problem on that side.

---

## Hook touchpoints

Three lifecycle hooks slot in as siblings to orch's existing marker hooks. Settings-snippet additions:

```jsonc
{
  "hooks": {
    "Stop": [{ "hooks": [
      { "type": "command", "command": "bash $HOME/.claude/hooks/orch-stop-marker.sh",        "timeout": 5 },
      { "type": "command", "command": "bash $HOME/.claude/hooks/orch-nats-publish-stop.sh",  "timeout": 5 }
    ]}],
    "Notification": [{ "hooks": [
      { "type": "command", "command": "bash $HOME/.claude/hooks/orch-notify-marker.sh",        "timeout": 5 },
      { "type": "command", "command": "bash $HOME/.claude/hooks/orch-nats-publish-notify.sh",  "timeout": 5 }
    ]}],
    "SessionStart": [{ "hooks": [
      { "type": "command", "command": "bash $HOME/.claude/hooks/orch-nats-publish-jsonl.sh",   "timeout": 5 }
    ]}]
  }
}
```

The `SessionStart` hook backgrounds a `tail -F | nats pub` of the session's JSONL transcript. Claude Code stores transcripts at `~/.claude/projects/<cwd-encoded>/<session_id>.jsonl`, where the encoding replaces both `/` AND `.` with `-` — so `/Users/x/.claude/worktrees/pr1` encodes as `-Users-x--claude-worktrees-pr1` (note the `--` from the `.`). The hook is PID-gated to prevent double-spawn under `/resume`.

---

## Quick example: orchestrator drives 3 builders

```sh
# Spawn 3 builders, capture pane ids
PANE1=$(orch-spawn claude --cwd ./worktree1 --quiet)
PANE2=$(orch-spawn claude --cwd ./worktree2 --quiet)
PANE3=$(orch-spawn claude --cwd ./worktree3 --quiet)

# Kick off each via NATS — bridge-in subscriber routes to the right pane
nats pub orch.tell "$(jq -nc --arg p "$PANE1" '{pane:$p, prompt:"execute plan A"}')"
nats pub orch.tell "$(jq -nc --arg p "$PANE2" '{pane:$p, prompt:"execute plan B"}')"
nats pub orch.tell "$(jq -nc --arg p "$PANE3" '{pane:$p, prompt:"execute plan C"}')"

# Watch completions via NATS instead of fswatch on marker files
nats sub 'orch.stop.>' --count=3

# Live transcript stream for any builder
nats sub --raw 'orch.events.373' | jq -c 'select(.message.content[]?|.type=="text")|.message.content[].text'
```

Compared to the bare `orch-tell` / `orch-listen` / fswatch path: equivalent capability, but the events flow over a substrate that supports multiple subscribers and (with JetStream behind it) durable replay.

---

## Multi-harness coverage

The original snapshot only shipped claude-code publish hooks. Coverage for the
other harnesses orch supports lives alongside, in per-harness directories:

| Harness | Stop | SessionStart-JSONL | Notification | Files |
|---|---|---|---|---|
| **claude-code** | ✓ | ✓ | ✓ | `hooks/orch-nats-publish-{stop,notify,jsonl}.sh` |
| **codex** | ✓ | ✓ | ✗ no event | `codex-hooks/orch-nats-publish-{stop,jsonl}.sh` |
| **pi** | ✓ | ✓ | ✗ no event | `pi-extensions/orch-nats-publish-{stop,jsonl}.ts` |
| **gemini** | ✓ (as `AfterAgent`) | ✗ deferred (path encoding) | ✓ | `gemini-hooks/orch-nats-publish-{stop,notify}.sh` |

**Notification gap (codex / pi):** neither harness exposes a mid-turn
"agent waiting for input" event analogous to claude-code's Notification hook.
The Stop signal is the only reliable substitute today — when one of these
agents finishes a turn, it is by definition idle and waiting. Future work:
proxy via `UserPromptSubmit` (codex) or extension events (pi) once we map
their semantics.

**Gemini event-name pitfall:** gemini-cli's turn-end event is named
`AfterAgent`, NOT `Stop`. Wiring a hook under `Stop` in `~/.gemini/settings.json`
silently fails — gemini-cli prints `⚠ Invalid hook event name: "Stop" from
project config. Skipping.` and continues. The canonical claude→gemini event
mapping (from gemini-cli v0.42.0 `hooks migrate`) is:

| claude-code | gemini-cli |
|---|---|
| `Stop` | `AfterAgent` |
| `Notification` | `Notification` |
| `SessionStart` | `SessionStart` |
| `PreToolUse` | `BeforeTool` |
| `PostToolUse` | `AfterTool` |
| `UserPromptSubmit` | `BeforeAgent` |
| `PreCompact` | `PreCompress` |

`gemini-settings-snippet.json` wires `AfterAgent` and `Notification` directly.

**Gemini SessionStart-JSONL deferral:** gemini-cli supports `SessionStart`
as an event, but the transcript file path encoding (`~/.gemini/tmp/<scope>/chats/session-<ts>-<sessionId>.jsonl`)
varies by project context in ways the discovery probe could not normalize.
Deferred until the path-resolution rule is mapped from gemini-cli source.

**Subject namespace stays uniform across harnesses.** Each publisher emits on
`orch.{stop,notify,events}.<pane_num>` with a `harness:` field in the body so
subscribers can filter or aggregate by harness without parsing subject tokens.

**Install:**
- `claude-code` — merge `settings-snippet.json` into `~/.claude/settings.json`
- `codex` — merge `codex-hooks-snippet.json` into `~/.codex/hooks.json`
- `pi` — auto-discovered, no merge needed (extensions land in `~/.pi/agent/extensions/`)
- `gemini` — merge `gemini-settings-snippet.json` into `~/.gemini/settings.json`

The npm postinstall (`scripts/postinstall.js`) symlinks every harness's hook files into the right
per-harness directory, but only when that harness's home dir already exists —
no `~/.codex` means no codex symlinks, etc., so installs on machines that
don't run a given harness stay clean.

## NATS server: sesh hub or standalone

The bridge does not care which NATS server it talks to — it reads `NATS_URL` from the environment and publishes / subscribes there. Two reasonable deployments:

- **`sesh hub serve`** — sesh's per-user hub gives you JetStream replay, session-scoped subject namespaces (via `ORCH_NATS_SUBJECT_PREFIX=sesh.<session>.orch`), and the hub-leaf topology that lets multiple orch instances on different hosts share one mesh. This is the long-term home; see sesh issue [#18](https://github.com/danmestas/sesh/issues/18) for the gap analysis on full sesh-affinity.
- **Bare `nats-server`** — for single-host experiments or environments without sesh, run `nats-server` directly and point the bridge at it via `NATS_URL=nats://localhost:4222`. Loses JetStream replay (unless enabled manually) and session scoping, but gives you the multi-subscriber + machine-portable wire format for free.

Both are uniform from the bridge's view. The publish hooks already use `--timeout=1s` so the bridge degrades gracefully when the configured server is gone.

---

## What full sesh-affinity would add

This bridge gives orch the wire layer. Closing the gap to a fully sesh-aware integration needs work on the sesh side:

| Gap | Current state | With sesh-affinity |
|---|---|---|
| Spawn integration | `orch-spawn` is sesh-naive — flat env, no session context | `sesh orch spawn <agent> --session=<label>` exports `NATS_URL` + `FOSSIL_URL` + `ORCH_NATS_SUBJECT_PREFIX=sesh.<session>.orch` automatically |
| Subject namespace | flat `orch.*` | session-scoped `sesh.<session>.orch.*` |
| Event durability | core NATS pub/sub (fire-and-forget) | JetStream stream in the session's domain, late-subscriber replay |
| Code substrate | git worktrees, builders commit + push directly | per-session Fossil leaf; `sesh promote` translates to git PR branches (TODO upstream) |
| Tool restrictions | spawn full-power; rely on prompt-injected interrupts | spawn-time suit-level denials (e.g., no `git push` / `gh pr create`) |

The bridge is designed so closing those gaps is mostly configuration, not rewrite:

- Publish hooks honour `ORCH_NATS_SUBJECT_PREFIX` — set it to `sesh.<session>.orch` in the spawn env and existing publish code Just Works.
- Publish hooks honour `NATS_URL` — point at `~/.sesh/sessions/<label>.json`'s `nats_url` field and publishes route through the sesh hub.

See sesh issue [#18](https://github.com/danmestas/sesh/issues/18) for the full gap analysis and proposed work items, and sesh's [`docs/orch-bridge.md`](https://github.com/danmestas/sesh/blob/main/docs/orch-bridge.md) for the substrate-side view.

---

## Implementation snapshot

A working prototype lives at `~/projects/orch-nats-bridge-snapshot/` on the operator's machine — 4 source files already renamed to orch conventions (ORCH_PANE_ID, orch-tell, orch.* subjects), 3 git patches from the original commit history, and a README with porting instructions.

To land into this repo:

```sh
cd ~/projects/orch
git checkout -b feat/nats-bridge

cp ~/projects/orch-nats-bridge-snapshot/orch-nats-bridge-in        bin/
cp ~/projects/orch-nats-bridge-snapshot/orch-nats-publish-stop.sh  hooks/
cp ~/projects/orch-nats-bridge-snapshot/orch-nats-publish-notify.sh hooks/
cp ~/projects/orch-nats-bridge-snapshot/orch-nats-publish-jsonl.sh hooks/
chmod +x bin/orch-nats-bridge-in hooks/orch-nats-publish-*.sh
```

Then:

- Add the three publish hooks + the SessionStart entry to `settings-snippet.json`
- Update `scripts/postinstall.js` to symlink the new files (the `linkDirEntries(hooks)` + `linkDirEntries(skills)` patterns already pick them up automatically — verify)
- Optionally add a doc-link entry to the README's tooling list

---

## Experiment record

This bridge was prototyped on 2026-05-12/13 against the predecessor project (`agent-harness`, since renamed to orch). An orchestrator-driven experiment ran three autonomous Claude Code builders, each executing an architectural-refactoring plan, driven by NATS prompts from a parent orchestrator. Result: 4 PRs merged on `FireStorm-Flight-Services/Ember`.

The experiment also surfaced the sesh-affinity gap explicitly: builders worked entirely in git worktrees + committed to git + pushed to GitHub. They never touched the Fossil leaf a sesh session would have provided. The bridge carries the comms wire layer; closing the substrate gap is the sesh-side work tracked at [sesh#18](https://github.com/danmestas/sesh/issues/18).
