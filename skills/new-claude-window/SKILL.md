---
name: new-claude-window
description: Open a new Claude Code session in a separate Ghostty window on macOS via osascript. Use when the user asks to "open another claude", "spawn a new claude session", "new claude window", "second claude instance", "open claude in another terminal", "fork off a claude", or any variation that means "give me a fresh interactive Claude Code REPL in its own window". Macos + Ghostty only.
---

# new-claude-window

Spawn a fresh interactive `claude` REPL in a new Ghostty window from inside a running Claude Code session.

## Prereqs

- macOS
- Ghostty.app installed at `/Applications/Ghostty.app`
- `claude` on PATH in the user's login shell (verify: `which claude` returns a path)

If the host terminal is iTerm / Terminal / kitty / Warp / Alacritty etc., this skill does NOT apply as-written — adapt the AppleScript or use that terminal's CLI flag instead.

## Detect host terminal first (sanity check)

```bash
ps -o comm= -p $(ps -o ppid= -p $(tmux display -p '#{client_pid}' 2>/dev/null || echo $$) 2>/dev/null) 2>/dev/null
# Or walk up ancestors until you hit a terminal app:
P=${TMUX:+$(tmux display -p '#{client_pid}')}; [ -z "$P" ] && P=$$
for i in 1 2 3 4 5 6; do P=$(ps -o ppid= -p $P 2>/dev/null | tr -d ' '); [ -z "$P" ] || [ "$P" = 1 ] && break; ps -o pid,comm -p $P; done
```

Look for `Ghostty.app/Contents/MacOS/ghostty` in the ancestor chain. If absent, stop and tell the user.

## The command

```bash
osascript -e 'do shell script "open -na Ghostty.app --args -e claude"'
```

What each piece does:

- `osascript -e '...'` — runs an AppleScript one-liner
- `do shell script "..."` — AppleScript escapes out to bash
- `open -na Ghostty.app` — `open` launches an app; `-n` forces a NEW instance (so we get a new window even if Ghostty is already running); `-a` names the app
- `--args -e claude` — args after `--args` go to Ghostty itself; Ghostty's `-e <cmd>` runs `<cmd>` inside the new window's shell

Returns immediately. New window appears with `claude` running fresh.

## Variants

Resume the most recent session instead of starting fresh:

```bash
osascript -e 'do shell script "open -na Ghostty.app --args -e \"claude --resume\""'
```

Open in a specific working directory:

```bash
osascript -e 'do shell script "open -na Ghostty.app --args -e \"cd /path/to/repo && claude\""'
```

With `--dangerously-skip-permissions` (only if user explicitly asks):

```bash
osascript -e 'do shell script "open -na Ghostty.app --args -e \"claude --dangerously-skip-permissions\""'
```

## Verify it worked

```bash
ps -eo pid,etime,command | grep -E '[g]hostty -e claude' | head -5
```

A row with `etime` under a minute = success. If empty after a few seconds, the new window failed to spawn — check Ghostty isn't blocked by macOS Gatekeeper / Accessibility prompts, and that `claude` resolves on PATH for a non-interactive shell (Ghostty's `-e` invokes the user's login shell, so `~/.zprofile` / `~/.zshrc` PATH setup must include claude).

## Why not tmux?

Tmux works but requires the user to attach (`tmux attach -t ...`) — it doesn't give them a visible new window on its own. `open -na Ghostty.app` does. Use tmux only if the user explicitly wants a backgrounded/detachable session.

## Why not the Agent tool?

The Agent tool (subagents) runs *inside* the current session. This skill is for when the user wants a *separate, human-driven* REPL they can type into themselves. Different problem.
