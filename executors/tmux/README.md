# executors/tmux/

The tmux executor runs agent processes in tmux panes on the operator's local
machine. It is the default executor and supports all harnesses (claude, pi,
codex, gemini).

## Spawn contract

`spawn.sh` reads the following exported environment variables from
`bin/orch-spawn` (the dispatcher):

| Variable | Description |
|----------|-------------|
| `AGENT` | Harness name: `claude`, `pi`, `codex`, or `gemini` |
| `CWD` | Working directory for the spawned pane |
| `HEADLESS` | `1` → detached `orch-headless` session; `0` → current window |
| `POSITION` | Split direction: `right` (default), `left`, `above`, `below` |
| `ROLE` | `worker` or `observer` |
| `NO_FLEET` | `1` → skip fleet-doctrine injection |
| `VERIFY` | `1` → poll for agent readiness before returning |
| `GOAL_EXPORTS` | Shell fragment exporting `SESH_GOAL_*` vars (may be empty) |
| `NO_SHIM` | `1` → skip orch-agent-shim launch (handled by dispatcher) |
| `OUTFIT` | Outfit name (empty if not set) |
| `BUNDLE` | suit bundle directory path (empty if no outfit) |

**stdout:** exactly one line — the new tmux pane id (e.g. `%42`).

**stderr:** informational and error messages.

## Stop contract

```
orch-down <pane_id>
```

## Synadia metadata

| Field | Value |
|-------|-------|
| `executor` | `tmux` |
| `location` | `local` |

## hooks/

Contains tmux-executor-specific Claude Code hooks:

- `orch-stop-marker.sh` — fired on agent Stop; writes a marker file that
  `orch-listen` watches to detect turn boundaries.
- `orch-notify-marker.sh` — fired on agent Notification; writes a marker for
  the notification event.

These hooks are symlinked into `~/.claude/hooks/` by `install.sh` and
`scripts/postinstall.js`. The repo-root paths `hooks/orch-stop-marker.sh` and
`hooks/orch-notify-marker.sh` are backward-compat symlinks pointing here.

## legacy/

Deprecated adapter scripts retained for one release cycle. Superseded by the
shim adapters in `internal/adapter/`.

- `codex-hooks/` — codex Stop/SessionStart hook scripts. Replaced by the
  codex adapter in `internal/adapter/codex/`.
- `gemini-hooks/` — gemini Stop/Notification hook scripts. Replaced by the
  gemini adapter in `internal/adapter/gemini/`.
- `pi-extensions/` — pi TypeScript extensions. Replaced by the pi adapter in
  `internal/adapter/pi/pi.go`.

The repo-root directories `codex-hooks/`, `gemini-hooks/`, and `pi-extensions/`
are backward-compat symlinks pointing into `legacy/`. They will be removed in
the next major release once the deprecation cycle closes.

## Operator notes

- The headless tmux session name defaults to `orch-headless`. Override with
  `ORCH_HEADLESS_SESSION`.
- The readiness poll timeout defaults to 60 seconds. Override with
  `ORCH_VERIFY_TIMEOUT`.
- Agent banners verified for title-rename detection: `claude` ("Claude Code"),
  `gemini` ("Gemini CLI"). Codex and pi fall back to process-title rename only.
