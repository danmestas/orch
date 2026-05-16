---
name: tmux-agent-panes
description: Spawn and arrange additional CLI agents (claude, pi, codex, gemini, aider, any REPL) into tmux panes around the current Claude Code session, anchored so the user's Claude Code pane stays at the top / top-center and other agents grid around it, without stealing keyboard focus. Use when the user asks to "open X to the right/left/above/below", "split tmux", "add another claude/pi pane", "open Y in a new pane", "put a claude pane next to this one", "spawn an agent beside me", "tile/grid the panes evenly", "rebalance panes", "even grid", "anchor my pane on top", "keep our pane on top", "grid others around us", "open agent in <project>", "recycle the agent pane", or any variation that means "give me another REPL adjacent in this tmux window" or "rearrange the existing panes". Requires being inside tmux. Pairs with `new-claude-window` (separate Ghostty window — use that when the user wants the new agent in its own OS window) and `orch-driver` (for sending prompts to and observing events from the panes you spawn here).
---

# tmux-agent-panes

Open one or more CLI agents in tmux panes adjacent to the current pane. Two non-obvious correctness rules drive the whole skill:

1. **Don't steal focus.** New panes become active by default — that hijacks the user's keyboard input away from the Claude Code session they're typing into. Use `tmux split-window -d` so the new pane is created but not focused.
2. **Don't let the pane vanish on exit.** If the agent (`claude`, `pi`, …) exits because of an auth error or a `Ctrl-D`, tmux closes the pane immediately and the user never sees what happened. Wrap the command so the pane survives and shows the exit reason.

## Prereqs

- Inside tmux (check `[ -n "$TMUX" ]`). If not, this skill doesn't apply — fall back to `new-claude-window` or just suggest the user run the agent themselves.
- The agent CLI is on PATH for the user's login shell (login shell is what `$SHELL -l` invokes).

## Detect tmux + current pane

```bash
[ -n "$TMUX" ] || { echo "not inside tmux"; exit 1; }
CUR_PANE=$(tmux display -p '#{pane_id}')   # e.g. %27
CUR_WIN=$(tmux display -p '#{window_id}')  # e.g. @8
```

`pane_id` (e.g. `%27`) is stable across layout changes — prefer it over numeric `pane_index` which renumbers when panes are added/removed.

**Re-detect every session.** When Claude Code is restarted, re-attached, or moved between windows, its `pane_id` changes (e.g. `%19` → `%27`). Never hardcode an id from a previous interaction — always read `#{pane_id}` fresh inside the current pane. If your splits land in the wrong window/session, the most likely cause is a stale pane id.

**Cross-session check.** If you can't find the pane you just split on, run `tmux list-panes -a` — splits target the *session of the target pane*, which may not be the session you think you're in.

## The split-window incantation

```bash
WRAP='%s; echo; echo "[%s exited — press enter]"; read; exec $SHELL -l'
CMD=$(printf "$WRAP" "claude" "claude")

tmux split-window -d -h -t "$CUR_PANE" "$CMD"   # right of current
tmux split-window -d -v -t "$CUR_PANE" "$CMD"   # below current
tmux split-window -d -h -b -t "$CUR_PANE" "$CMD" # left of current
tmux split-window -d -v -b -t "$CUR_PANE" "$CMD" # above current
```

Flag breakdown:
- `-d` — don't switch focus to the new pane. **Critical.** Without this the user's typing goes to the new pane.
- `-h` — horizontal split (panes side-by-side). `-v` — vertical split (panes stacked).
- `-b` — put the new pane **before** the target (left for `-h`, above for `-v`). Without `-b`, the new pane goes after (right / below).
- `-t <pane_id>` — target pane to split. Use a `pane_id` like `%19`, not a numeric index.

The wrapper `cmd; echo; echo "[cmd exited — press enter]"; read; exec $SHELL -l` keeps the pane alive after the agent exits — the user sees the exit, presses enter, and lands in their login shell (so they can re-run it or close the pane with `exit` / prefix-x).

## Capturing the new pane's id

When chaining splits, you need the id of the pane you just created. Use `-P -F '#{pane_id}'` — `split-window` prints the new id on stdout and you capture it directly. **Prefer this over `{right-of}`** etc., which can return the wrong pane in complex layouts.

```bash
NEW=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR_PANE" 'claude; …')
# $NEW is now %33 or whatever
tmux split-window -d -v -b -t "$NEW" 'pi; …'   # split the pane we just made
```

## Open an agent in a project directory

The user typically wants the new agent to start in some project (e.g. "open pi in <project>"). Resolve the path with `zoxide query <name>` if zoxide is installed — works in any shell, no init required:

```bash
# Inside the wrap command:
'cd "$(zoxide query <project>)" && pi; echo; echo "[pi exited — press enter]"; read; exec $SHELL -l'
```

**Don't use the bare `z` function** in tmux split commands. `z` is defined by `eval "$(zoxide init zsh)"` at zshrc time, which non-interactive subshells skip — `zoxide query` is the actual binary and is the safe form for scripted use.

If zoxide isn't installed or the name isn't indexed, fall back to a literal path: `cd ~/projects/<project>` or ask the user.

## Common layouts

**Add one agent to the right:**
```bash
tmux split-window -d -h -t "$CUR_PANE" \
  'claude; echo; echo "[claude exited — press enter]"; read; exec $SHELL -l'
```

**Add agent A to the right, then agent B above A:**
```bash
A=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR_PANE" \
  'pi; echo; echo "[pi exited — press enter]"; read; exec $SHELL -l')
tmux split-window -d -v -b -t "$A" \
  'claude; echo; echo "[claude exited — press enter]"; read; exec $SHELL -l'
```

**Three-up stacked column on the right:**
```bash
R=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR_PANE" 'claude; …; exec $SHELL -l')
tmux split-window -d -v -t "$R" 'pi; …; exec $SHELL -l'
tmux split-window -d -v -t "$R" 'codex; …; exec $SHELL -l'
```

## Layout management

**Anchor the user's pane on top, grid everything else below** (the user's preferred default):

```bash
tmux select-layout -t "$CUR_PANE" main-horizontal
# main-horizontal puts pane_index 0 on top; if that isn't us, swap.
MAIN=$(tmux list-panes -t "$CUR_PANE" -F '#{pane_id} #{pane_top}' | awk '$2==0 {print $1; exit}')
[ "$MAIN" != "$CUR_PANE" ] && tmux swap-pane -d -s "$CUR_PANE" -t "$MAIN"
```

`swap-pane -d` keeps the active pane unchanged after the swap (so we don't yank focus). Tune the main pane height with `tmux set -g main-pane-height <rows>` before applying the layout.

Variants:
- `main-vertical` — main on left, others stacked on right
- `tiled` — even N×M grid, no anchor
- `even-horizontal` / `even-vertical` — single row / single column

**Rebalance after splits drift the layout:**
```bash
tmux select-layout -t "$CUR_PANE" tiled       # even grid
tmux select-layout -t "$CUR_PANE" main-horizontal  # re-anchor on top
```

Bind a hotkey for fast re-tile in `~/.tmux.conf`:
```
bind = select-layout tiled
bind + select-layout main-horizontal
```

## Layout principles (when to pick which variant)

These are principles, not recipes — fixed rules don't transfer because every session has a different agent count and terminal size. The reasoning does transfer.

**1. Pane shape matters more than pane area.** Two panes with the same area can be drastically different to read. A 99×11 pane and a 49×24 pane both have ~1080 cells, but the second shows ~2× more readable conversation history. TUI agents spend fixed rows on header (~2), input box (~3), and footer (~2); body rows = total − ~7. Aim for **body rows ≥ 16 per agent** so a prompt + a paragraph of response stay visible together.

**2. Orchestrator gets the chat-friendly aspect first.** It's doing the same thing as workers — chat — so it has the same shape constraint. Don't squash it into a wide-short banner. A column-shaped orchestrator (~80×40+) beats a banner-shaped one (~180×12). Allocate to the orchestrator first, then divide the remainder.

**3. Grid shape follows agent count.**

| Agents | Grid | Notes |
|---|---|---|
| 1 | full | trivial |
| 2 | side-by-side | each gets max width |
| 3 | row of 3 | mixed-row layouts cost symmetry |
| 4 | 2×2 | sweet spot |
| 5 | 2×3 with one empty | accept the empty cell, don't do 3+2 |
| 6 | 2×3 or 3×2 | pick the one giving body rows ≥ 16 |
| 7+ | reconsider | drop dead weight or split across windows |

Asymmetric grids (3+2, 4+1) save a few cells but cost the eye more than they're worth. Pay the empty-cell tax to keep the grid square.

**4. Drop dead weight before sizing.** Quota-locked agents, agents with broken hooks, agents in the wrong project — they're taking cells that live ones could use. Audit the lineup *before* computing the grid.

**5. Account for per-agent chrome quirks.** Some agents render uglier when squeezed: **pi** wants ≥80 cols (wide footer with model + cost + token meters); **gemini**'s box-drawing UI compresses to ~50 cols; **codex** is fine to ~40 cols; **claude** handles small widths reasonably. If pi is in the lineup and you want a 4-column grid in a 160-col terminal, pi gets ugly — give pi a wider cell (asymmetric is OK here, this exception costs less than ugly chrome) or move to a 3-column grid.

### Quick procedure

1. **Audit** — who's actually capable of responding? Drop the rest.
2. **Allocate orchestrator** — claim ~80 cols × full available height.
3. **Compute remainder** — `terminal_width − 80` × `terminal_height`.
4. **Pick grid** by agent count (table above). If any agent would land below body rows of ~16, drop an agent or move to a wider terminal.
5. **Apply** via `select-layout` and `move-pane`. Re-anchor orchestrator with `swap-pane -d` after layouts that don't preserve position (`tiled`, `main-vertical`).
6. **Inspect** — capture-pane each agent and check body rows are sufficient for one prompt+response cycle.

### Layout anti-patterns

- **Banner orchestrator** (full-width × short height) — wastes horizontal space; occludes prompt+response below the fold.
- **Single-column stack of agents** — each agent ends up too short to be useful; 4 agents stacked at 12 rows = 4 useless cells.
- **Symmetry violation to fit one more agent** — 5 agents in 3+2 looks worse than 5 in 2×3 with one empty cell.
- **Keeping a quota-locked agent in the lineup** — real estate spent on something that can't respond.

## Driving the panes after spawning

Once a pane is spawned, sending prompts to it, waiting for the agent to finish, and observing lifecycle events live in the **`orch-driver`** skill. The relevant tools are `orch-tell`, `orch-wait`, `orch-ask`, and `nats sub 'agents.>'` for event streams (post-#94 the legacy `orch-listen` / `orch-subscribe` / `orch-register` are retired — registration is automatic via `orch-agent-shim`).

**One thing to bake into the spawn here**: if the new pane will be auto-driven via `orch-tell`, set `ORCH_PANE_ID=$TMUX_PANE` in the launch command so the shim can identify the pane on the bus, and pass `--dangerously-skip-permissions` to claude so a permission prompt doesn't pause the turn indefinitely (turn-end won't fire mid-turn). Example:

```bash
tmux split-window -d -h -t %27 \
  'export ORCH_PANE_ID=$TMUX_PANE; cd "$(zoxide query <project>)" && \
   claude --dangerously-skip-permissions; \
   echo; echo "[claude exited — press enter]"; read; exec $SHELL -l'
```

For pi/codex/gemini, only the cwd matters — they don't read `ORCH_PANE_ID` (no Stop hook).

## Verification

Quick layout check after any operation:

```bash
tmux list-panes -t "$CUR_WIN" -F '#{pane_id} #{?pane_active,*,-} #{pane_current_path} #{pane_width}x#{pane_height}'
```

The `*` should still be on `$CUR_PANE`. If geometry looks wrong, re-tile with `tmux select-layout -t "$CUR_PANE" main-horizontal`.

**Two tmux fields lie — don't trust them blindly:**

1. **`pane_current_command` shows the wrap shell, not the agent.** Because the wrapper launches the agent as a child of zsh and tmux reports the foreground process group leader, you'll see `zsh` even when claude/pi/codex is running fine. Don't trust this for "is the agent alive?". Instead capture pane content:
   ```bash
   tmux capture-pane -t %30 -p | tail -20
   ```
   You'll see the agent's actual UI (claude splash, pi prompt, etc.).

2. **`pane_current_path` lags.** It only updates when the shell repaints its prompt (via OSC 7). While an agent is running foreground, the cwd reported is whatever it was at the last prompt — often the cwd *before* the `cd` ran. Don't conclude "cd failed" from this; only believe a stale path after a repaint.

## Gotchas

- **Window-level operations (`tmux new-window`) take the same `-d` flag** but it means "don't make the new window the active one" rather than the new pane. Pane splits use `tmux split-window -d`; window creation uses `tmux new-window -d`. Both prevent focus theft, but don't confuse the two operations.
- **If the user is NOT in tmux**, this skill doesn't apply. Don't try to start tmux around their existing Claude Code session — that requires re-attaching and breaks the running process. Use `new-claude-window` instead.
- **`(active)` marker can lie when `mouse on` is set.** A user click on a pane updates the tmux-active flag, even if their actual keyboard focus is elsewhere (e.g., they tabbed back). If you spot the `*` on the wrong pane, run `tmux select-pane -t "$CUR_PANE"` to pin focus — but don't do this reflexively, the user may have moved on purpose.
- **TUI agents may want csi-u extended keys.** Pi prints a warning if `extended-keys-format` isn't `csi-u`. Fix without restarting tmux:
  ```bash
  tmux set -g extended-keys-format csi-u                  # apply live
  echo 'set -g extended-keys-format csi-u' >> ~/.tmux.conf  # persist
  ```
  After applying, recycle the agent pane so it re-reads. Don't restart tmux from inside Claude Code — that kills your own session.
- **`claude` inherits auth from parent** — the new claude pane runs as a fresh process with its own login shell and picks up the same credentials as the user's normal `claude` invocations. No special arg passing needed.
- **`--dangerously-skip-permissions`** — only add this if the user explicitly asks. Don't default it.
- **Agent CLIs that need a TTY size** — `tmux split-window` gives the new pane a real TTY automatically; nothing extra needed.
- **`zsh -c` non-interactive doesn't source rc.** That's why we use `zoxide query` (binary call) instead of `z` (shell function). Same trap if you try to use any other rc-defined alias/function in a split-window command.
- **Don't `tmux kill-pane` your own pane.** Targeting `$CUR_PANE` for kill while you're still in it suspends the parent Claude Code session in unpredictable ways. Always confirm `pane_id != $CUR_PANE` before `kill-pane`.

## Agent CLI reference (npm packages)

| CLI       | npm package                            | Notes |
|-----------|----------------------------------------|-------|
| `claude`  | `@anthropic-ai/claude-code`            | First-party. Auto-updates from inside the REPL via the harness. |
| `gemini`  | `@google/gemini-cli`                   | |
| `codex`   | `@openai/codex`                        | Reports as `codex-cli <ver>`. |
| `pi`      | `@earendil-works/pi-coding-agent`      | **Don't use `@mariozechner/pi-coding-agent`** — deprecated stub capped at 0.73.1. `pi update` (built-in) still hardcodes the deprecated package, so manual `npm install -g @earendil-works/pi-coding-agent` is the only way to get >0.73.1 until upstream patches it. |
| `aider`   | (Python — installed via `pipx` / `uv`) | Not in this npm fleet. Update via `pipx upgrade aider-chat`. |

Multi-update in one go:
```bash
npm install -g @google/gemini-cli @openai/codex @earendil-works/pi-coding-agent
```

The `Reshimming mise lts...` line that npm emits is mise wiring up the new shims. Not an error, doesn't need a shell restart.

## Updating and restarting an agent pane

Updating the binary doesn't affect a running agent — you must kill and re-spawn the pane:

```bash
# 1. Update the binary
npm install -g @openai/codex

# 2. Find the pane running the old version (capture-pane to verify, since
#    pane_current_command lies — see "Verification" above).
OLD=%32

# 3. Kill it and split a fresh one in the same place
tmux kill-pane -t "$OLD"
tmux split-window -d -h -t "$CUR_PANE" \
  'cd "$(zoxide query <project>)" && codex; echo; echo "[codex exited — press enter]"; read; exec $SHELL -l'

# 4. Re-anchor the layout (kill+split disturbs main-horizontal)
tmux select-layout -t "$CUR_PANE" main-horizontal
MAIN=$(tmux list-panes -t "$CUR_PANE" -F '#{pane_id} #{pane_top}' | awk '$2==0 {print $1; exit}')
[ "$MAIN" != "$CUR_PANE" ] && tmux swap-pane -d -s "$CUR_PANE" -t "$MAIN"
```

For multiple agents at once, kill all the old panes first, then split all the new ones, then apply the layout once at the end — re-tiling per pane wastes work.

## When NOT to use this skill

- User wants a **separate OS window** rather than a pane → use `new-claude-window`.
- User wants a **detachable background session** → use plain `tmux new-session -d -s <name>`.
- User wants **parallel work without a second human-driven REPL** → use the Agent tool (subagents) inside the current session.
