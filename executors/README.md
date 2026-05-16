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

1. Create `executors/<type>/` with at minimum a `spawn.sh` that:
   - Reads context from exported env vars (AGENT, CWD, HEADLESS, ROLE, etc.)
   - Starts the agent process
   - Writes exactly one line to stdout: the instance id
   - Writes informational / error messages to stderr
2. Add `<type>` to the `--executor` validation list in `bin/orch-spawn`.
3. Write a `README.md` documenting the spawn/stop contract and any
   infrastructure prerequisites (e.g. wrangler credentials, SSH access).

The tmux executor's `spawn.sh` is the reference implementation.
