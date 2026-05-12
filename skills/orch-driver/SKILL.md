---
name: orch-driver
description: Drive interactive AI agent CLIs (claude, pi, codex, gemini) already running in tmux panes from a parent Claude Code session, and observe their lifecycle events (turn-completion, attention-needed) without polling. Use when the user asks to "send a prompt to <agent>", "drive the <agent> pane", "ask <agent> X and bring back the answer", "broadcast a prompt to all agents and time them", "wait for <agent> to finish", "fire when claude is done", "observe stop events", "auto-approve permission prompts in another claude pane", "remote control a tmux agent", "talk to my running pi/codex/gemini/claude", "wake me when <agent> finishes", or any variation involving sending prompts to and reading replies from agents already running in tmux panes. Pairs with `tmux-agent-panes` (which spawns and lays out the panes); this skill is for the after-spawn phase of orchestration.
---

# orch-driver

Drive interactive AI agents in adjacent tmux panes from a parent Claude Code session, and react to their lifecycle events without polling.

## Tools (all in `~/.local/bin`)

| command | what it does |
|---|---|
| `orch-tell <pane> <prompt>` | inject a prompt into the pane's input box and submit it |
| `orch-wait <pane>` | block until the pane's screen is stable (works for any TUI) |
| `orch-ask <pane> <prompt>` | tell + wait + return only the agent's new reply |
| `orch-listen [timeout] [--include-notify] [--exclude <pane[,pane]>] [--exclude-self]` | block on the next Stop event from ANY hook-wired pane. The orchestrator's "always on ear." Default: Stop only; opt in to Notification with `--include-notify`. `--exclude-self` filters events from the calling pane (use when the orchestrator's own Stop hook fires would otherwise wake the listener every turn). |
| `orch-peek [<pane>...] [--json] [--since <duration>] [--all]` | peek live workers from the registry — shows pane id, agent/outfit, role, activity bucket (ACTIVE/recent/idle), event count, last assistant text, last tool. `--json`, `--since <duration>`, `--all` flags. Useful as a periodic status report (e.g. via `/loop 5m orch-peek`). |
| `orch-spy <target> <mission>` | one-shot spy spawn: target is `operator` or `%pane_id`. Spawns claude `--outfit stasi --cut wait-watch` (auto-tagged role=observer by A1, default-excluded from `orch-listen`), builds the standard brief envelope (target jsonl + harness state pointers), sends it via `orch-tell`. Returns spy's pane id on stdout. `--mission-file <path>`, `--dry-run-brief` (preview the envelope without spawning), `--quiet`, `--headed` flags. |
| `orch-version [--json] [--quiet]` | drift detection between the project repo and the live install — surveys every binary, hook, and skill; reports per-item state (match/drift/missing). Symlink-aware (recognizes the symlink-farm install pattern). Exit 0 sync, 1 drift, 2 hard error. |
| `orch-subscribe <peer> [<peer>...]` / `--list` / `--unsub <peer>` / `--cancel` | give a worker pane push-notifications when listed peers fire Stop. Spawns a per-(self,peer) daemon that fwatches markers and injects `[peer event]` prompts via `orch-tell`. Mutual subscriptions are refused at setup (would cascade). |
| `orch-register <pane> <agent> <cwd> [session_id]` | record pane metadata to `~/.cache/orch-registry/<pane>.json` for restart recovery |
| `orch-spawn <agent> [--cwd p] [--project n] [--headless] [--no-fleet] [--outfit X] [--cut Y] [--accessory A]...` | unified worker spawn. Defaults to headed (in current window); `--headless` puts it in the detached `orch-headless` tmux session — agent runs identically, just not visible. Auto-registers. With `--outfit` (claude only for now), runs `suit prepare` to generate an isolated config bundle in a tempdir, makes the bundle the worker's cwd, and adds the project as a workspace dir — gives per-worker outfit isolation even when multiple workers share the same project. Bundle auto-removed via wrap-shell `trap` when the pane dies. |
| `orch-show <pane>` | promote a headless pane into the orchestrator's window (`tmux join-pane`) |
| `orch-hide <pane>` | demote a headed pane back to the detached `orch-headless` session (`tmux break-pane`) |
| `orch-relayout [orch_pane] [--orch-width N] [--cols N]` | rebuild a precise custom layout (orch full-height left + N-col agent grid right) using a hand-built layout string + checksum. Use after pane drift. |
| `orch-tell --list` | list all tmux panes for picking targets |

Aliases optional, in `~/.config/orch-aliases` (`name=%pane_id` per line). Pane ids change on every recycle.

## Operator vs worker model

Read this before the rest of the skill — half the table above splits along this distinction.

`orch-spawn` is for **workers** — sessions you intend to drive from a parent. It exports `ORCH_PANE_ID`, which is what causes the Stop/Notification hooks to write markers and lazy-register the pane.

The **operator** session (the one *you* type into) should be started directly, NOT through `orch-spawn`:

```bash
cd ~/projects/foo && claude          # plain — no ORCH_PANE_ID exported
```

That gives you the right asymmetry for free: workers are visible in the registry and emit Stop markers; the operator isn't and doesn't. No `--orchestrator` flag, no role enforcement layer, no filter logic — the env-var distinction *is* the role.

A1 added a third class: **observers** (stasi spies, wait-watch / spy-on-* cuts) — workers in the registry, but tagged `role: "observer"`. `orch-listen` default-excludes them; `orch-tell` refuses worker→observer unless `--force`. Observers exist to watch the operator; the operator can redirect them, workers cannot. Spawn an observer via `orch-spy <target> <mission>` (auto-tags) or explicitly via `orch-spawn ... --role observer`.

If an operator session was accidentally started through `orch-spawn` (the wrapper exports `ORCH_PANE_ID` unconditionally, baking it into the live process), it's stuck firing self-Stops and self-registering until restart. `orch-listen --exclude-self` filters the noise on the listener side; the durable fix is to start the next operator session plainly.

If you're the operator, run `orch-claim-operator` once at session start. That writes `~/.cache/orch-operator.json` with your pane id + transcript JSONL path, so other harness tools (`orch-peek`, `orch-spy`, observer skills) can find your session without prompting.

## Always be listening

If you're orchestrating, you should NOT only listen when you've explicitly launched something for a specific event. Workers can be driven by the user too (manual chat, hand-edits) and you'd miss that activity.

**Recommended pattern: `orch-listen --stream` wrapped in a Monitor.** Self-rearming, one push notification per event. Arm once at session start; covers the whole session.

```python
Monitor(
    command="orch-listen --stream",
    description="harness events",
    persistent=True,
)
# parent does other work; each event lands as a push notification with
# one JSON line: {"event_file":..., "pane_id":..., "ext":..., "kv":{...}}
```

**Legacy bg-bash one-shot pattern** (kept for back-compat): re-arm immediately after each fire, or you go deaf. Most-violated rule in the harness — prefer `--stream` + Monitor.

```python
while orchestrating:
    Bash("orch-listen 3600", run_in_background=True)
    # parent processes event, then immediately re-launches the listener
```

Distinguishing "I sent the prompt" vs "user typed something": cross-check the event's `pane_id` against your own send-log (your recent orch-tell calls). If you didn't send to that pane recently, that's user activity.

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

"Standing by", "I'll be watching", "monitoring for…" claims a concrete vigilance posture. Only say it when something is *actually* keeping watch right now — an armed `orch-listen`, an active Monitor stream, a scheduled cron, a live `/loop` iteration. Without one, the conversation just pauses until the operator speaks again; that's stopping, not standing by.

**Why**: claiming vigilance you don't have misrepresents posture. It undermines trust in everything else you claim about state.

**How to apply**: before typing a vigilance phrase, verify the watch is real (listener pid exists, cron scheduled, /loop active). If yes, name what's watching: *"listener `bX` armed for next Stop on %114."* If no, just end the message without the claim.

## Spawning panes you intend to drive

When the parent will send prompts via `orch-tell`, each agent's spawn command needs the right permission-bypass flag baked in. Otherwise mid-turn permission prompts pause the agent and watchers hang.

### Per-agent reference matrix

One table for everything you need per agent — permission flag, hook config, resume mechanism, session storage. Match each row to your spawn recipe.

| Agent | Yolo flag | Hook config | Hook quirks | Resume | Session dir |
|---|---|---|---|---|---|
| **claude** | `--dangerously-skip-permissions` (or `--permission-mode bypassPermissions`; or `--allowed-tools "Read,Glob,Grep,Bash"`) | `~/.claude/settings.json` `hooks.{Stop,Notification}` | works out of the box | `--resume <id>` / `--continue` | `~/.claude/projects/<cwd-encoded>/<session>.jsonl` |
| **codex** | `--dangerously-bypass-approvals-and-sandbox` (or `--ask-for-approval never --sandbox <mode>`) | `~/.codex/hooks.json` `hooks.Stop` (claude format) | **`/hooks` review-and-approve gate** — codex won't run new/changed hooks until you `/hooks` in the REPL and approve, even with bypass flag | `codex resume <id>` | `~/.codex/sessions/...` |
| **gemini** | `-y` / `--yolo` (or `--approval-mode yolo`) | `~/.gemini/settings.json` `hooks` (claude format accepted) | `gemini hooks migrate --from-claude` is for *project-local* `.claude/` → `.gemini/`, NOT global config — edit `~/.gemini/settings.json` directly | (project-scoped history, no flag) | `~/.gemini/history/` |
| **pi** | n/a — no gating by default; `--no-tools` / `--tools <allowlist>` only RESTRICT | `~/.pi/agent/extensions/orch-stop-marker.ts` (TypeScript extension subscribes to `pi.on("agent_end", ...)` and writes the same marker format as the bash hooks) | extension auto-discovered on pi startup; same `ORCH_PANE_ID` env-var contract as the other agents | `--resume <id>` / `--continue` | `~/.pi/agent/sessions/<session>.jsonl` |

The hook scripts (`orch-stop-marker.sh`, `orch-notify-marker.sh`) are agent-agnostic — they read `$ORCH_PANE_ID` from the env and write the marker file. Same scripts work for any agent once that agent's hook config points at them.

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
- How to wait for peer activity via `orch-listen`

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

## Event-driven path

Each agent's hook fires on `Stop` (turn complete) and `Notification` (attention needed). The two tiny no-op-unless-tagged hook scripts (`~/.claude/hooks/orch-{stop,notify}-marker.sh`) read `$ORCH_PANE_ID` from env and write a marker file at `~/.cache/orch-stop/<pane_id>.<event>`. If `ORCH_PANE_ID` is unset the hook exits immediately — so installing globally is safe; only opted-in panes write markers.

`orch-listen` watches that one directory via `fswatch -1` (kqueue on macOS — `brew install fswatch`) and wakes the parent regardless of which agent fired.

| Marker file | When written | Used by `orch-listen` default? |
|---|---|---|
| `<pane>.event` | Stop fires (turn complete) | yes |
| `<pane>.notify` | Notification fires (permission prompt, idle warning) | no — opt in with `--include-notify` |

See the per-agent matrix above for which agents have hooks wired (claude ✓, codex ✓ pending `/hooks` approval, gemini configured but unverified, pi unsolved).

The "Always be listening" and "Choosing the right wait primitive" sections above govern *how* you arm the listener and *which* primitive holds the wait — read those first; the subsections below are the mechanics.

### Bg-bash + auto-notification = real push to the parent

Claude Code's harness fires a notification when a `Bash(run_in_background=true)` exits. Compose:

```python
Bash(command="orch-listen 3600", run_in_background=True)
# When the agent finishes, fswatch wakes, bash exits, parent gets a notification.
# Parent does NOT poll — it does other work and is woken on event arrival.
```

Measured end-to-end: hook fire → parent notification ≈ 1-3 seconds (dominated by harness internals; fswatch + bash-exit is sub-100ms).

### The permission-prompt gap

If a CC pane hits a permission prompt mid-turn:

- Stop does **not** fire (turn isn't complete).
- Notification **does** fire with a `message` field describing what's being requested.

A watcher only listening for Stop will hang forever. **Always set `--dangerously-skip-permissions` on the spawn command** unless you have a specific reason to want the prompts. If you DO want to handle them in-flight, the pattern is:

1. Launch parallel watchers — one for Stop, one for Notification.
2. Whichever fires first wakes the parent.
3. On Notification: read `message`, decide approve/reject, `tmux send-keys -t <pane> Enter` (option 1 "Yes" is highlighted by default, so Enter accepts), re-arm the Notification watcher.
4. Loop until Stop fires.

This is a state machine; bg-bash-per-event-of-interest is the unit of reaction.

## Peer subscriptions (worker-side push)

`orch-listen` is for the orchestrator — it returns when a peer's Stop fires. Workers (other claude/pi/codex/gemini panes) can get the same push behavior via `orch-subscribe`, which spawns a per-(self,peer) daemon that fwatches the marker dir and injects a `[peer event]` prompt into the calling pane on each fire.

```sh
# inside a worker pane (ORCH_PANE_ID set by orch-spawn):
orch-subscribe %59 %60         # subscribe to two peers' Stops
orch-subscribe --list          # show what we're subscribed to
orch-subscribe --unsub %59     # drop one
orch-subscribe --cancel        # drop all
```

The injected prompt looks like `[peer event] %59 fired Stop at <iso> (cwd=...) — read-only context, do not auto-reply unless instructed.` The fleet doctrine (`~/.cache/orch-fleet-prompt.md`) tells workers to treat these as notifications, not conversation — but doctrine alone can't suppress the *turn* the agent runs on receipt, only the verbosity of its response.

**Mutual subscriptions are refused at setup.** If A subs to B and you try to sub B to A, the second subscribe returns exit 2 with a "mutual subscriptions cause cascade loops" error. Empirically verified: with two real claudes mutually subscribed and a single trigger prompt, the cascade hits ~12 events in 45s. The refusal is the only Ousterhout-shaped fix that doesn't require hook surgery — bidirectional coordination should go through the orchestrator instead.

Internals worth knowing:
- Daemon dedups on the `ts_ns` field inside the marker file (not mtime) — a shell `> file` redirect can produce multiple kernel events per logical write, but the producer hook stamps exactly one `ts_ns`.
- Daemon exits when the calling pane disappears (cheap `tmux list-panes` check on each fswatch wake) or on TERM signal (which is what `--cancel` sends).
- Pidfiles at `~/.cache/orch-subs/<self>.<peer>.pid`. Subscriptions are in-memory only — restart of the worker = re-subscribe.
- Test suites: `~/projects/orch/test/test-orch-subscribe.sh` (fast, 18 assertions) and `test-orch-subscribe-real.sh` (slow, real-claude E2E, 7 assertions).

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
3. Calls `orch-tell`
4. Waits via `orch-listen` (CC) or `orch-wait` (non-CC)
5. Records `T_SETTLED_NS`, `T_BASH_END_NS`
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
- **fswatch on Mac** — `brew install fswatch`. No inotify on macOS.
- **The Stop hook fires for the parent CC too** if no gating. Our scripts gate on `ORCH_PANE_ID`; the parent doesn't have it set, so it no-ops. Don't remove the gate. **Edge case:** if the operator session was accidentally started through `orch-spawn`, it inherits `ORCH_PANE_ID` and starts emitting self-Stops — see "Operator vs worker sessions" above. Workaround: `orch-listen --exclude-self`. Durable fix: launch operator sessions plainly (not via `orch-spawn`).

## Layout heuristics

Each broadcast has a different number of agents, so don't rely on a fixed "always lay them out like X" recipe. Reason from principles each time. The principles (body-rows-per-agent ≥ ~16, orchestrator gets chat aspect first, grid shape follows agent count, drop dead weight before sizing) are written up in `~/projects/orch/docs/layout-heuristics.md` with the *why* attached. Read that before sizing panes for a new broadcast.

## Headed vs headless workers

A worker pane runs identically whether it's visible in the orchestrator's window (headed) or running in a detached tmux session (headless). Tmux provides the TTY either way; hooks, `orch-tell`, `orch-listen`, and `capture-pane` all work the same.

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

The parent CC's in-memory state (send log, listener, knowledge of which pane is which) dies when the parent dies. Workers keep running in tmux. To recover cleanly on restart, every relevant signal is also written to disk:

| File | Format | Purpose | Updated by |
|---|---|---|---|
| `~/.cache/orch-stop/<pane>.event` | key=value | latest Stop fired (overwritten) | Stop hook |
| `~/.cache/orch-stop/<pane>.notify` | key=value | latest Notification fired (overwritten) | Notification hook |
| `~/.cache/orch-stop/events.log` | JSONL **append-only** | full history of every Stop and Notification across all panes | Stop + Notification hooks |
| `~/.cache/orch-send.log` | JSONL append-only | every `orch-tell` call (pane, sender, prompt preview, ts_ns) | `orch-tell` |
| `~/.cache/orch-registry/<pane>.json` | JSON | pane → agent → cwd → session_id → spawn_ts_ns → last_seen_ts_ns | `orch-register` (eager) + Stop hook (lazy refresh) |

### Recovering on restart

```bash
# 1. What workers do I know about?
ls ~/.cache/orch-registry/

# 2. What happened while I was away? Replay events since some checkpoint.
LAST_TS_NS=...   # whatever timestamp you last processed
jq -c "select(.ts_ns > $LAST_TS_NS)" ~/.cache/orch-stop/events.log

# 3. Of those events, which were ME (orchestrator-driven) vs USER?
# Match each event against send-log for the same pane in a small time window before.
```

### Registering a worker at spawn

Hooked agents (claude, codex) register themselves lazily — first Stop event refreshes their registry entry. **Non-hooked agents (pi, and gemini until proven otherwise)** need explicit registration:

```bash
orch-register <pane_id> <agent> <cwd> [session_id]
# e.g.
orch-register %44 pi ~/projects/example
```

Add this call to the spawn recipe for non-hooked agents.

## When NOT to use this skill

- **Need to spawn the pane first** → use `tmux-agent-panes`, then come back here.
- **Just want a one-shot answer, don't care about the live REPL's context** → use the agent's headless mode (`claude -p`, `gemini -p`, `codex exec`, `pi -p`). Cleaner — no scraping, no driving, just stdin/stdout.
- **Agent isn't in tmux** → wrong tool. Use `new-claude-window` (separate Ghostty) or just shell out.
- **Long-running multi-turn dialog from a script** → orch-ask works for one round-trip, but parsing across multiple turns is brittle. Consider building a Unix-socket daemon (option B from the original design conversation) before doing this regularly.
