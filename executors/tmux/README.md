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

## Eventing

Turn-boundary and notification events flow through `orch-agent-shim` on the
Synadia bus (`agents.>` subjects). The filesystem-marker hooks and per-harness
NATS-publish hooks were retired in orch#94 — the shim is the only path.

## Operator notes

- The headless tmux session name defaults to `orch-headless`. Override with
  `ORCH_HEADLESS_SESSION`.
- The readiness poll timeout defaults to 60 seconds total wall time.
  Override with `ORCH_VERIFY_TIMEOUT`.
- Verify retries follow `ORCH_VERIFY_BACKOFF` (default `1,2,4,8` — sleep
  durations between attempts, in seconds). Total bounded by
  `ORCH_VERIFY_TIMEOUT`. Fail-fast on pane death or "command not found"
  output for the harness binary.
- Agent banners verified for title-rename detection: `claude` ("Claude Code"),
  `gemini` ("Gemini CLI"). Codex and pi fall back to process-title rename only.
