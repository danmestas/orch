---
name: orch-driver
description: Drive interactive AI agent CLIs (claude, pi, codex, gemini) already running in tmux panes from a parent Claude Code session, and observe their lifecycle events (turn-completion, attention-needed) without polling. Use when the user asks to "send a prompt to <agent>", "drive the <agent> pane", "ask <agent> X and bring back the answer", "broadcast a prompt to all agents and time them", "wait for <agent> to finish", "fire when claude is done", "observe stop events", "auto-approve permission prompts in another claude pane", "remote control a tmux agent", "talk to my running pi/codex/gemini/claude", "wake me when <agent> finishes", or any variation involving sending prompts to and reading replies from agents already running in tmux panes. Pairs with `tmux-agent-panes` (which spawns and lays out the panes); this skill is for the after-spawn phase of orchestration.
---

# orch-driver

> **As of orch#94 (2026-05-16):** the legacy fs-marker IPC and the orch
> NATS comms bridge are retired. The Synadia Agent Protocol via
> `orch-agent-shim` is the only wire. Event-listening goes through the
> NATS bus (`nats sub 'agents.>'`); pane discovery goes through
> `$SRV.INFO.agents`. See `migrating-to-synadia` for the translation
> table and the bus-side recipes.

Drive interactive AI agents in adjacent tmux panes from a parent Claude Code session, and react to their lifecycle events without polling — over the Synadia Agent Protocol bus.

## Preferred primitives (fast reference)

Scan this before reaching for a tmux/bash workaround. Each row says: what you're trying to do, what to actually use, and the wrong-reflex it replaces.

| Operator intent | Preferred primitive | When NOT to use / antipattern it replaces |
|---|---|---|
| Send prompt + wait for reply | `orch-ask <pane> "..."` (tell + collect + diff) | Don't `orch-tell` then `tmux capture-pane` and try to diff yourself |
| Send prompt fire-and-forget | `orch-tell <pane> "..."` | Don't `tmux send-keys` directly — drops the Enter race; also `orch-tell` refuses worker→observer unless `--force` |
| Send multi-line prompt from script | `orch-tell <pane> -` (read prompt from stdin) | Don't heredoc into `harness-tell` or escape-quote a long string into `orch-tell` argv |
| Wait for one pane to settle | `orch-wait <pane>` (universal screen-stability) | Don't `sleep N && ls /path/to/expected.output` — harness rule blocks long leading sleeps anyway |
| Wait for an *event* (turn-end, query) | `Monitor` wrapping `nats sub 'agents.>' --raw` | Don't `Bash(run_in_background)` a polling loop — loses events between calls, doesn't survive session |
| Subscribe to all bus events for the session | Arm `Monitor` on `nats sub 'agents.>' --raw` once at session start | Don't open a fresh sub each time you "want to see what happened" |
| Narrow to one pane's events | `nats sub 'agents.events.>.<owner>.pct<N>'` (or `agents.hb.>` for heartbeats only) | Don't subscribe to `agents.>` and grep — wastes the push channel |
| Read worker state / what it just said | The worker's JSONL transcript (path in `$SRV.INFO.agents` metadata, or for claude: `~/.claude/projects/<cwd-enc>/<session>.jsonl`) | Don't `tmux capture-pane -p \| tail -N` — screen buffer is lossy and truncates |
| Find your own (operator) transcript | `orch-claim-operator` once at session start → reads `~/.cache/orch-operator.json` | Don't grep `ps`, scan `~/.claude/projects/` by mtime, or reverse-engineer the encoded cwd path |
| Find a worker's transcript | `nats req '$SRV.INFO.agents' '' --replies=0` → `metadata.transcript_path` | Don't tail `~/.cache/orch-registry/` (legacy, retired in #94) |
| Snapshot the live fleet | `orch-peek` (`--json`, `--since <dur>`, `--all`) | Don't iterate `tmux list-panes` and guess which is an agent |
| Pick a pane interactively | `orch-tell --list` | Don't ask the user "which pane?" — list them |
| Spawn a worker you'll drive | `orch-spawn <agent> [--outfit X --cut Y] [--headless]` | Don't `tmux split-window` + manual yolo flag — misses the shim, won't appear on the bus |
| Spawn a spy/observer on a target | `orch-spy <target> <mission>` | Don't `orch-spawn claude --outfit stasi` by hand and reconstruct the brief envelope |
| Move a worker between visible / hidden | `orch-show <pane>` / `orch-hide <pane>` | Don't `tmux break-pane`/`join-pane` raw — `orch-show`/`orch-hide` know about the `orch-headless` session |
| Detect drift between repo and live install | `orch-version [--json]` | Don't `diff -r` the bin dirs manually |

**Retired binaries — do NOT reach for these:** `orch-listen` (replaced by `nats sub 'agents.>'` + Monitor), `orch-subscribe` (no replacement; subscribe directly), `orch-register` (auto via shim's `$SRV.INFO.agents`), `orch-current-jsonl` (use the shim's `metadata.transcript_path`). Marker files under `~/.cache/orch-registry/` are gone too — read `$SRV.INFO.agents` instead.

## Tools (all in `~/.local/bin`)

| command | what it does |
|---|---|
| `orch-tell <pane> <prompt>` | publish a prompt to `agents.prompt.<token>.<owner>.<pane-enc>`; the shim delivers it into the pane's input box and submits it. `--collect` streams response chunks until the terminator. |
| `orch-wait <pane>` | block until the pane's screen is stable (works for any TUI; capture-pane based) |
| `orch-ask <pane> <prompt>` | tell + collect: returns the agent's full reply via the shim's chunk stream |
| `orch-peek [<pane>...] [--json] [--since <duration>] [--all]` | live agent surface from `$SRV.INFO.agents` — pane id, agent/outfit, role, last-heartbeat, recent events. `--json`, `--since <duration>`, `--all` flags. Useful as a periodic status report (e.g. via `/loop 5m orch-peek`). |
| `orch-tail <pane\|alias> [--once] [--patterns=<re>] [--tool-results-only]` | stream a builder's CC transcript JSONL through a built-in trouble-detection regex (`FAIL `/`panic:`/`undefined:`/`cannot use`/`ambiguous`/`build failed`/`test failed`); emits one line per match. Wrap in `Monitor()` to wake on builder trouble. Override the regex via `--patterns`; one-shot scan via `--once`. |
| `orch-spy <target> <mission>` | one-shot spy spawn: target is `operator` or `%pane_id`. Spawns claude `--outfit stasi --cut wait-watch` (auto-tagged `role=observer`; bus subscribers filter observers by `metadata.role`), builds the standard brief envelope (target jsonl + harness state pointers), sends it via `orch-tell`. `--mission-file <path>`, `--dry-run-brief`, `--quiet`, `--headed`. |
| `orch-version [--json] [--quiet]` | drift detection between the project repo and the live install — surveys every binary, hook, and skill; reports per-item state (match/drift/missing). Symlink-aware. Exit 0 sync, 1 drift, 2 hard error. |
| `orch-spawn <agent> [--cwd p] [--project n] [--headless] [--no-fleet] [--outfit X] [--cut Y] [--accessory A]...` | unified worker spawn. Defaults to headed (in current window); `--headless` puts it in the detached `orch-headless` tmux session — agent runs identically, just not visible. Auto-launches `orch-agent-shim` alongside the pane so it appears on the bus. With `--outfit` (claude only for now), runs `suit prepare` to generate an isolated config bundle. |
| `orch-show <pane>` | promote a headless pane into the orchestrator's window (`tmux join-pane`) |
| `orch-hide <pane>` | demote a headed pane back to the detached `orch-headless` session (`tmux break-pane`) |
| `orch-relayout [orch_pane] [--orch-width N] [--cols N]` | rebuild a precise custom layout (orch full-height left + N-col agent grid right). |
| `orch-tell --list` | list all tmux panes for picking targets |

**Retired in #94:** `orch-listen` (use `nats sub 'agents.>'` with a Monitor wrapper instead), `orch-subscribe` (the worker-side push-notification daemon — no replacement; subscribe to the bus directly), `orch-register` (registration is automatic via the shim's `$SRV.INFO.agents` advertisement; the legacy `orch-register` stub still exists and no-ops), `orch-current-jsonl` (resolved via the shim's metadata.transcript_path).

Aliases optional, in `~/.config/orch-aliases` (`name=%pane_id` per line). Pane ids change on every recycle.

## Operator vs worker model

Read this before the rest of the skill — half the table above splits along this distinction.

`orch-spawn` is for **workers** — sessions you intend to drive from a parent. It exports `ORCH_PANE_ID`, which is what causes the Stop/Notification hooks to write markers and lazy-register the pane.

The **operator** session (the one *you* type into) should be started directly, NOT through `orch-spawn`:

```bash
cd ~/projects/foo && claude          # plain — no ORCH_PANE_ID exported
```

That gives you the right asymmetry for free: workers are visible in the registry and emit Stop markers; the operator isn't and doesn't. No `--orchestrator` flag, no role enforcement layer, no filter logic — the env-var distinction *is* the role.

A1 added a third class: **observers** (stasi spies, wait-watch / spy-on-* cuts) — workers tagged `role: "observer"`. The shim publishes that role in its `$SRV.INFO.agents` metadata; bus subscribers filter observers by `metadata.role`. `orch-tell` refuses worker→observer unless `--force`. Observers exist to watch the operator; the operator can redirect them, workers cannot. Spawn an observer via `orch-spy <target> <mission>` (auto-tags) or explicitly via `orch-spawn ... --role observer`.

If an operator session was accidentally started through `orch-spawn` (the wrapper exports `ORCH_PANE_ID` unconditionally, baking it into the live process), it's stuck firing self-events forever. The durable fix is to start the next operator session plainly. Bus subscribers can filter their own pane id out of `agents.>` subscriptions when they need to ignore self-fires.

If you're the operator, run `orch-claim-operator` once at session start. That writes `~/.cache/orch-operator.json` with your pane id + transcript JSONL path, so other harness tools (`orch-peek`, `orch-spy`, observer skills) can find your session without prompting.

## Always be listening

If you're orchestrating, you should NOT only listen when you've explicitly launched something for a specific event. Workers can be driven by the user too (manual chat, hand-edits) and you'd miss that activity.

**Recommended pattern: `nats sub 'agents.>'` wrapped in a Monitor.** Self-rearming, one push notification per event. Arm once at session start; covers the whole session.

```python
Monitor(
    command="nats sub 'agents.>' --raw",
    description="harness events on the Synadia bus",
    persistent=True,
)
# parent does other work; each event lands as a push notification with the
# raw chunk body (JSON: {type:'response'|'status'|'query'|'thinking', data:...})
```

To narrow scope, subscribe to a specific subject family:
- `agents.hb.>` — heartbeats only (liveness signal)
- `agents.events.cc.<owner>.>` — only claude-code events for this owner
- `agents.events.>.<owner>.pct<N>` — only events from pane `%<N>`

Distinguishing "I sent the prompt" vs "user typed something": cross-check the event's pane against your own recent `orch-tell` calls (`~/.cache/orch-send.log`). If you didn't send to that pane recently, that's user activity.

## Choosing the right wait primitive

First: `ToolSearch(query: "select:Monitor")` to load Monitor's schema. Monitor is a deferred tool; without this you'll cycle through bash-polling alternatives before remembering you have a better option.

When you need to wait for a future state — workflow finish, deploy completion, periodic status check, long build — pick the right Claude Code persistence primitive instead of bash-polling inside `Bash(run_in_background=true)`.

| Primitive | Right for | Survives session end? |
|---|---|---|
| **Monitor** | "watch this stream and wake me on each new event" — fswatch, log tails, anything emitting one stdout line per event | no, but per-event push within the session |
| **CronCreate** | "check this in 4 hours" / "run every morning at 8am" — fires a remote agent at a specific time | yes |
| **/loop** | "keep checking every 5 min until I tell you to stop" — recurring task on fixed interval while session is active | no |
| **ScheduleWakeup** | self-pacing inside a `/loop` dynamic-mode iteration | no |

**Why the bash-bg-poll antipattern is wrong**: `Bash(... while-condition; do_thing, run_in_background=true)` (a) doesn't survive session restart, (b) doesn't compose with scheduling primitives, (c) conflates "I'm polling internally" with "the system is durably waiting on my behalf."

**How to apply**: when tempted to write a polling loop, ask which primitive owns the wait.

- `gh run list` until completed → Monitor on the workflow status stream, or CronCreate if longer than this session.
- npm registry shows new version → Monitor polling every 30s, or CronCreate to check in 5 min.
- "Wait until tomorrow morning" → always CronCreate.
- "Check every minute for the next hour" → /loop with an interval.
- Inside `/loop` dynamic mode, vary next-fire timing on what you saw → ScheduleWakeup.

The `Bash(run_in_background=true)` pattern is fine for in-the-moment "this should finish in <60s" waits. The moment the wait is open-ended or the trigger is state-of-the-world rather than a one-shot completion, switch to a real primitive.

For release flows specifically (build → publish → install → verify), see the `release-watch` skill — it codifies Monitor-based recipes for npm / cargo / pip / GitHub release waits and the multi-stage chain pattern.

## Reporting watch state honestly

"Standing by", "I'll be watching", "monitoring for…" claims a concrete vigilance posture. Only say it when something is *actually* keeping watch right now — an active Monitor on `nats sub 'agents.>'`, a scheduled cron, a live `/loop` iteration. Without one, the conversation just pauses until the operator speaks again; that's stopping, not standing by.

**Why**: claiming vigilance you don't have misrepresents posture. It undermines trust in everything else you claim about state.

**How to apply**: before typing a vigilance phrase, verify the watch is real (listener pid exists, cron scheduled, /loop active). If yes, name what's watching: *"listener `bX` armed for next Stop on %114."* If no, just end the message without the claim.

## Spawning panes you intend to drive

When the parent will send prompts via `orch-tell`, each agent's spawn command needs the right permission-bypass flag baked in. Otherwise mid-turn permission prompts pause the agent and watchers hang.

### Per-agent reference matrix

One table for everything you need per agent — permission flag, hook config, resume mechanism, session storage. Match each row to your spawn recipe.

| Agent | Yolo flag | Bus integration | Resume | Session dir |
|---|---|---|---|---|
| **claude** | `--dangerously-skip-permissions` (or `--permission-mode bypassPermissions`; or `--allowed-tools "Read,Glob,Grep,Bash"`) | shim's `claudecode` adapter tails the transcript JSONL + detects turn-end | `--resume <id>` / `--continue` | `~/.claude/projects/<cwd-encoded>/<session>.jsonl` |
| **codex** | `--dangerously-bypass-approvals-and-sandbox` (or `--ask-for-approval never --sandbox <mode>`) | shim's `codex` adapter tails rollouts + uses idle-heuristic for turn-end | `codex resume <id>` | `~/.codex/sessions/...` |
| **gemini** | `-y` / `--yolo` (or `--approval-mode yolo`) | shim's `gemini` adapter watches per-session events; transcript emission deferred | (project-scoped history, no flag) | `~/.gemini/history/` |
| **pi** | n/a — no gating by default; `--no-tools` / `--tools <allowlist>` only RESTRICT | shim's `pi` adapter tails `~/.pi/agent/sessions/...` | `--resume <id>` / `--continue` | `~/.pi/agent/sessions/<session>.jsonl` |

All per-agent eventing lives inside `orch-agent-shim` and the adapters under `internal/adapter/<harness>/`. The shim is auto-launched by `orch-spawn` alongside every pane.

### ORCH_PANE_ID env var (CC only)

Set `export ORCH_PANE_ID=$TMUX_PANE` in the spawn wrapper for **claude** panes. Lets the Stop / Notification hooks self-identify at fire time. tmux sets `TMUX_PANE` per-pane automatically; we just re-export it under the name the hooks look for. Setting it for non-CC agents is harmless; it's just unused.

### Per-worker outfits via `suit prepare`

When you want N workers with different outfits in the same project, pass `--outfit <name>` (claude only for now) to `orch-spawn`. Internally:

1. `suit prepare --outfit X --cut Y --target claude-code` generates a config bundle in `/var/folders/.../T/suit-prepare-*/`.
2. The worker's wrap shell does `cd <bundle> && claude --add-dir <project>` — bundle is the cwd (so claude auto-discovers `.claude/skills/`, agents/, hooks/, settings, CLAUDE.md from it), the real project is a workspace dir for tool access.
3. The wrap shell traps `EXIT`, so when the pane dies the bundle's tempdir is removed automatically. No accumulation of stale `/var/folders/.../suit-prepare-*` dirs.

```sh
orch-spawn claude --project myapp --outfit backend --cut executing
orch-spawn claude --project myapp --outfit reviewer --cut reviewing
# two workers, same project, different outfits, isolated config trees
```

**Quirks:**
- Worker's `pwd` is the bundle tempdir, not the project. Status bar shows the bundle name (e.g., `suit-prepare-Oc2A14`). Tools still see project files via `--add-dir`.
- `--outfit` is **claude-only** today. For codex/gemini/pi, suit emits to different target adapters but the spawn-side glue isn't verified — wire those up incrementally with their own integration tests.
- One bundle per worker, no sharing — keeps cleanup independent. The `subs never inherit` rule means each spawn must specify its own outfit explicitly.

Test battery: `~/projects/orch/test/test-suit-integration.sh` (4 tests, 11 assertions, all green) — covers headed + headless + cleanup + parallel.

### Fleet-awareness addendum

Workers spawned with the standard recipe get a system-prompt addendum (`~/.cache/orch-fleet-prompt.md`, mirrored to `~/projects/orch/fleet-prompt.md`) that teaches them about each other. Without it, workers behave as if they were chatting with a human user and have no awareness of peers or the orchestrator. With it, each worker knows:

- Its own pane id (`$ORCH_PANE_ID`)
- How to enumerate peers via `~/.cache/orch-registry/<pane>.json`
- How to send messages to peers via `orch-tell`
- How to wait for peer activity via `nats sub 'agents.>'` on the Synadia bus

**Per-agent injection mechanism** (all four wired):

| Agent | How fleet doctrine reaches it |
|---|---|
| claude | `--append-system-prompt-file ~/.cache/orch-fleet-prompt.md` (CLI flag, applied at spawn) |
| pi | `--append-system-prompt "$(cat ~/.cache/orch-fleet-prompt.md)"` (CLI flag, applied at spawn) |
| codex | injected into `~/.codex/AGENTS.md` between marker comments; codex auto-loads AGENTS.md at startup |
| gemini | injected into `~/.gemini/GEMINI.md` between marker comments; gemini auto-loads GEMINI.md at startup |

Codex and gemini have no `--append-system-prompt` CLI flag, so we inject into their global instructions files using a marker block:

```
<!-- BEGIN orch-fleet-doctrine -->
...content from fleet-prompt.md...
<!-- END orch-fleet-doctrine -->
```

`install.sh` runs the injection idempotently (creates the file if absent, refreshes the block if present, appends the block if file exists without markers). Re-running installs refreshes content without disturbing the user's surrounding instructions.

To skip fleet awareness for a one-off worker, just omit the spawn flag (claude/pi) or set `ORCH_NO_FLEET=1` and have the spawn wrapper consult it (not currently wired, but trivial to add).

### Reference spawn commands

```bash
# claude — yolo, fleet-aware, in some project (substitute your own)
tmux split-window -d -h -t "$ORCH" \
  'export ORCH_PANE_ID=$TMUX_PANE; cd "$(zoxide query <project>)" && \
   claude --dangerously-skip-permissions \
     --append-system-prompt-file ~/.cache/orch-fleet-prompt.md; \
   echo; echo "[claude exited — press enter]"; read; exec $SHELL -l'

# pi — fleet-aware
tmux split-window -d -h -t "$ORCH" \
  'export ORCH_PANE_ID=$TMUX_PANE; cd "$(zoxide query <project>)" && \
   pi --append-system-prompt "$(cat ~/.cache/orch-fleet-prompt.md)"; \
   echo; echo "[pi exited — press enter]"; read; exec $SHELL -l'

# codex — yolo (fleet-aware via ~/.codex/AGENTS.md)
tmux split-window -d -h -t "$ORCH" \
  'export ORCH_PANE_ID=$TMUX_PANE; cd "$(zoxide query <project>)" && \
   codex --dangerously-bypass-approvals-and-sandbox; \
   echo; echo "[codex exited — press enter]"; read; exec $SHELL -l'

# gemini — yolo (fleet-aware via ~/.gemini/GEMINI.md)
tmux split-window -d -h -t "$ORCH" \
  'export ORCH_PANE_ID=$TMUX_PANE; gemini --yolo; \
   echo; echo "[gemini exited — press enter]"; read; exec $SHELL -l'
```

## orch-tell internals you should know

The Enter-timing race is fixed inside the script, but document it for when you (or a future iteration) need to debug:

```bash
tmux send-keys -t "$pane" -l "$prompt"   # literal type
sleep 0.1                                # let the literal stream drain
tmux send-keys -t "$pane" Enter          # submit
```

Without the sleep, the Enter arrives before the TUI finishes consuming the typed chars and gets dropped — the prompt sits unsubmitted in the input box. Confirmed across claude / pi / codex / gemini. Tunable via `ORCH_TELL_SUBMIT_DELAY`.

`orch-tell` also pre-waits for the pane to be idle before sending (same screen-stability check as orch-wait), so you can pipeline calls without races.

## orch-wait (universal screen-stability)

Captures `tmux capture-pane -p` every `--interval` seconds, requires `--stable` consecutive identical samples to declare idle. Strips volatile lines (spinner chars `⠋⠙⠹⠸⠼`, `Thinking...`, `Working`, `esc to cancel`, token counters) before comparing — without that, agents with animated status lines never settle.

Defaults: `--stable 5 --interval 2 --timeout 600`. Tighter for fast experiments: `--stable 3 --interval 1`.

This is the *only* mechanism that works on non-CC agents (pi, codex, gemini) since they don't expose lifecycle hooks.

## orch-ask (one-shot tell + wait + diff)

```bash
result=$(orch-ask %30 "summarize this project" --stable 3 --interval 1)
```

Captures pre-snapshot, sends, waits for stability, captures post-snapshot, prints only content past the LAST occurrence of the pre-snapshot's last non-empty line. Use the LAST occurrence — first match catches repeating footer lines and dumps the entire scrollback.

## Event-driven path (Synadia bus)

Per-pane events are emitted by `orch-agent-shim` on the NATS bus. The shim
runs as a sibling process to each spawned agent and:

- Tails the agent's transcript / session JSONL → emits typed chunks
  (`{type:"response",...}`, `{type:"thinking",...}`, `{type:"tool_use",...}`)
  on `agents.events.<harness>.<owner>.<pane-enc>`.
- Detects turn-end via the per-harness adapter and emits a status chunk
  (`{type:"status",data:"ack"}` / terminator).
- Publishes liveness heartbeats on `agents.hb.<harness>.<owner>.<pane-enc>`
  at a fixed cadence.
- Advertises pane metadata via `$SRV.INFO.agents` (NATS micro-service
  discovery).

| Subject family | Carries |
|---|---|
| `agents.prompt.>` | inbound prompts from `orch-tell` / `nats request` |
| `agents.events.>` | outbound typed chunks (response, thinking, tool_use, etc.) |
| `agents.status.>` | turn-end / ack / terminator |
| `agents.hb.>` | heartbeats (§8.3 of Synadia spec) |
| `$SRV.INFO.agents` | NATS micro-service discovery — pane metadata |

The "Always be listening" and "Choosing the right wait primitive" sections above govern *how* you arm the bus subscription and *which* primitive holds the wait.

### The permission-prompt gap

If a CC pane hits a permission prompt mid-turn:

- The turn-end status chunk does **not** fire (turn isn't complete).
- A query chunk is emitted instead (`{type:"query","data":{"message":"..."}}`).

A subscriber only waiting for turn-end will hang forever. **Always set `--dangerously-skip-permissions` on the spawn command** unless you have a specific reason to want the prompts. If you DO want to handle them in-flight, watch `agents.events.>` for `type:"query"` chunks and respond by sending an Enter via `tmux send-keys -t <pane> Enter` (option 1 "Yes" is highlighted by default, so Enter accepts).

## Peer subscriptions (worker-side push)

Workers (other claude/pi/codex/gemini panes) that need to react to peer activity should subscribe directly to the bus from inside their own wrap shell. The legacy `orch-subscribe` daemon (which fwatched the marker dir and injected `[peer event]` prompts) was retired in #94 along with the markers.

A simple worker-side recipe (run inside the worker pane at startup):

```sh
# Subscribe to a specific peer's events; pipe through orch-tell to self.
nats sub --raw "agents.events.>.${OWNER}.pct${PEER_PANE_NUM}" \
  | while IFS= read -r chunk; do
        orch-tell "$ORCH_PANE_ID" "[peer event] $chunk"
    done &
```

Cross-cascade hazard: a worker that injects peer events into itself and then *responds* will trigger more events on its own subject — easy to make a feedback loop. Filter the subject pattern carefully (don't subscribe to your own pane's events) and consider rate-limiting the injection (e.g. one `orch-tell` per N seconds per peer).

## Broadcast pattern

Send the same prompt to N agents in parallel, time their replies, identify who said what:

```bash
for entry in "%37:stop-hook:claude-app" "%30:polling:pi-app" "%36:polling:codex-app"; do
    IFS=: read -r pane method label <<< "$entry"
    Bash(command="bash /tmp/orch-bcast.sh $pane $method $label '$PROMPT'", run_in_background=True)
done
```

Each bg bash:
1. Captures pre-snapshot for the diff
2. Records `T_SEND_NS` (nanoseconds)
3. Calls `orch-tell --collect` (so the bus's terminator chunk signals turn-end), or `orch-tell` + `orch-wait` for non-shim panes
4. Records `T_SETTLED_NS`, `T_BASH_END_NS`
6. Prints structured report:

```
AGENT=claude-app
PANE=%37
METHOD=stop-hook
T_BASH_START_NS=...
T_SEND_NS=...
T_SETTLED_NS=...
T_BASH_END_NS=...
DELTA_SEND_TO_SETTLED_MS=...
DELTA_HOOK_TO_BASH_END_MS=...
=== stop-hook payload ===
ts_ns=...
session_id=...
=== response (new content since send) ===
<diff content>
```

Parent receives N notifications in order of completion. Latency to measure on the parent side: record `date +%s%N` in the response that handles each notification — diff against `T_BASH_END_NS` to measure orch-internal push lag.

A reusable runner script lives at `/tmp/orch-bcast.sh` (write a permanent copy if you broadcast often).

## JSONL transcript alternative (zero-hook observation)

CC writes session transcripts to `~/.claude/projects/<cwd-encoded>/<session>.jsonl` in real time. Each new assistant message ≈ Stop event. We could `fswatch` those files directly and parse for completed assistant entries, no custom hooks needed.

Trade-off: transcript format is internal CC and may change between versions; we'd be parsing a private contract instead of using the documented hook system. Kept the hook approach for stability + control of the marker format. Note this option for the future if hooks prove fragile.

## Gotchas

- **Settings.json hook changes are not retroactive.** Existing CC sessions loaded settings at startup. New hooks only apply to sessions started AFTER the edit. Recycle panes to pick up new hook config.
- **ORCH_PANE_ID stale across recycles.** The pane id changes on every kill+respawn. The env-var-from-`$TMUX_PANE` pattern handles this automatically — no manual update needed in the hook script or the settings.
- **`(active)` marker can lie.** A user mouse-click moves tmux's reported active pane even if their keyboard focus is elsewhere. Don't reflexively `select-pane` to "fix" it.
- **`pane_current_command` shows `zsh` for our wrapped agents** — the wrap shell is the foreground process from tmux's view, not the agent. `tmux capture-pane -p` is the only honest way to see what's actually running.
- **`pane_current_path` lags** — only updates on shell prompt redraw (OSC 7). Don't conclude "cd failed" from it during a foreground task.
- **Default permission-dialog option is "1. Yes"** highlighted. Pressing Enter accepts. Convenient for auto-approve, but pressing Enter in an UN-blocked pane just submits an empty prompt — make sure the pane is actually showing a permission prompt before sending Enter.
- **Bg bash that runs forever doesn't push** — parent wakes on bash EXIT, not on bash output lines. Every event-of-interest needs to be its own short-lived bash that exits on the trigger.
- **Operator session self-events** — the parent CC has no `ORCH_PANE_ID` and no shim attached, so it does NOT publish to `agents.>`. If the operator session was accidentally started through `orch-spawn`, it inherits a shim and starts emitting self-events. Durable fix: launch operator sessions plainly (not via `orch-spawn`). Workaround: filter the operator pane out of your bus subscription pattern.

## Layout heuristics

Each broadcast has a different number of agents, so don't rely on a fixed "always lay them out like X" recipe. Reason from principles each time. The principles (body-rows-per-agent ≥ ~16, orchestrator gets chat aspect first, grid shape follows agent count, drop dead weight before sizing) are written up in `~/projects/orch/docs/layout-heuristics.md` with the *why* attached. Read that before sizing panes for a new broadcast.

## Headed vs headless workers

A worker pane runs identically whether it's visible in the orchestrator's window (headed) or running in a detached tmux session (headless). Tmux provides the TTY either way; the shim, `orch-tell`, bus subscriptions, and `capture-pane` all work the same.

**Headed** — default; the worker is a pane in your visible orchestrator window. Useful when you want eyes on the conversation.

**Headless** — runs in the `orch-headless` tmux session that isn't attached to any client. Useful for:
- **Spy workers** — agents that watch other workers' transcripts/sessions and synthesize learnings (e.g., "review my last hour of turns and propose skill improvements"). They don't need to be visible while observing.
- **Long-running tasks** — agents working through a multi-hour task you don't want crowding your layout.
- **Reserve agents** — pre-warmed for instant promotion when you need them.

```bash
# spawn headless
PANE=$(orch-spawn claude --project myapp --headless)

# promote to visible when needed
orch-show "$PANE"

# send back to headless
orch-hide "$PANE"
```

`orch-relayout` only sees panes in your current window, so headless workers don't crowd the layout. To attach a separate terminal directly to the headless session: `tmux attach -t orch-headless`.

## Persistence layer (survives parent CC restart)

The parent CC's in-memory state (send log, knowledge of which pane is which) dies when the parent dies. Workers keep running in tmux and their shims keep advertising on the bus. To recover cleanly on restart:

| Source | Format | Purpose |
|---|---|---|
| `$SRV.INFO.agents` (NATS micro-service) | JSON | live pane → agent → role → metadata for every shim-attached pane |
| `agents.events.>` / `agents.hb.>` (NATS subjects) | JSON chunks | event stream — durable replay requires a JetStream consumer on these subjects |
| `~/.cache/orch-send.log` | JSONL append-only | every `orch-tell` call (pane, sender, prompt preview, ts_ns) |
| `~/.cache/orch-operator.json` | JSON | operator's pane id + transcript JSONL path (written by `orch-claim-operator`) |

### Recovering on restart

```bash
# 1. What workers are live right now?
nats req '$SRV.INFO.agents' '' --replies=0 --timeout=2s \
  | jq -s '.[].metadata | {pane_id, agent, role, owner}'

# 2. What events happened while I was away?
# If you set up a JetStream consumer on agents.events.>, replay from the
# durable-consumer cursor. Otherwise, this is bus-live-only — you can't
# recover missed events without JetStream durability.

# 3. Of those events, which were ME (orchestrator-driven) vs USER?
# Match each event's pane against ~/.cache/orch-send.log in a small time
# window before the event.
```

## When NOT to use this skill

- **Need to spawn the pane first** → use `tmux-agent-panes`, then come back here.
- **Just want a one-shot answer, don't care about the live REPL's context** → use the agent's headless mode (`claude -p`, `gemini -p`, `codex exec`, `pi -p`). Cleaner — no scraping, no driving, just stdin/stdout.
- **Agent isn't in tmux** → wrong tool. Use `new-claude-window` (separate Ghostty) or just shell out.
- **Long-running multi-turn dialog from a script** → orch-ask works for one round-trip, but parsing across multiple turns is brittle. Consider building a Unix-socket daemon (option B from the original design conversation) before doing this regularly.
