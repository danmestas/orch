# executors/

An *executor* is the substrate that runs an agent process. `bin/orch-spawn` is a
dispatcher: it parses operator arguments, then delegates to
`executors/<type>/spawn.sh`.

## Lifecycle contract

Every executor exposes two primitives:

| Primitive | Signature | Description |
|-----------|-----------|-------------|
| `spawn`   | `spawn(agent, cwd, role, outfit_bundle?) → instance_id` | Start an agent process. Returns an opaque instance identifier (for tmux: the pane id, e.g. `%42`). |
| `stop`    | `stop(instance_id) → error?` | Tear down the running agent. For tmux: `orch-down <pane_id>`. |

The executor's `spawn.sh` receives resolved state via exported environment
variables (see the script's header for the full list) and writes exactly one
line to stdout: the instance id.

## Synadia Agent Protocol metadata

Each executor type advertises in `$SRV.INFO`:

| Field | Description |
|-------|-------------|
| `executor` | Executor type identifier (e.g. `tmux`, `wasm`) |
| `location` | Where the agent runs (`local`, `edge`, `remote`) |

## Current executors

### `tmux/` — local tmux pane (default)

Spawns agents in tmux panes on the operator's machine. Headed or headless.
Supports all harnesses: claude, pi, codex, gemini.

See [executors/tmux/README.md](tmux/README.md).

### `wasm/cf-worker/` — Cloudflare Worker (edge)

Runs an open-agent instance on Cloudflare's edge network via a Durable
Object + WebSocket NATS bridge. Requires a configured wrangler deployment.

See [executors/wasm/cf-worker/README.md](wasm/cf-worker/README.md) (or
the inline `wrangler.toml` for deployment instructions).

## Adding a new executor

Names must match `[a-z][a-z0-9-]*` (lowercase letters, digits, hyphens;
starts with a letter). The dispatcher's `resolve_executor()` accepts any
such name and discovers the backend through three layers
(`ORCH_EXECUTOR_<NAME>_CMD` env var → `command -v orch-executor-<name>`
on PATH → in-tree `executors/<name>/spawn.sh`); see
[docs/multi-executor-workers.md](../docs/multi-executor-workers.md)
§ "Current state — hybrid executor discovery" for the full precedence
and the Proposal 0003 sister-repo split.

### For in-tree backends (lightweight only — tmux pattern)

1. Create `executors/<name>/` with a `spawn.sh` that:
   - Reads context from exported env vars (AGENT, CWD, HEADLESS, ROLE, etc.)
   - Starts the agent process
   - Writes exactly one line to stdout: the instance id
   - Writes informational / error messages to stderr
2. Write a `README.md` documenting the spawn/stop contract and any
   infrastructure prerequisites.

No dispatcher edit is required — `resolve_executor()` picks the new
script up via the in-tree fallback layer.

### For sister-repo backends (heavyweight — cf-worker pattern)

Per Proposal 0003, heavyweight backends (TS + wrangler, devcontainer,
browser-tab, …) ship as their own repo named `orch-executor-<name>`.
Operators install them via the per-language release shape (npm /
homebrew / goreleaser) so the binary lands on PATH; alternatively, they
set `ORCH_EXECUTOR_<NAME>_CMD=<command>` to point at a deployed remote
endpoint.

The tmux executor's `spawn.sh` is the reference implementation for the
spawn-side contract regardless of where the backend lives.
