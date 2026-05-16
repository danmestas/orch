# executors/wasm/cf-worker/

Cloudflare Worker executor: runs an open-agent instance on Cloudflare's edge
network, bridged to the NATS hub via WebSocket.

## Spawn contract

Each incoming `fetch /agent/<session>` request starts an agent run for the
duration of the NATS connection. The session name maps to the NATS subject:

```
agents.prompt.open-agent.<OPEN_AGENT_OWNER>.<session>
```

**instance_id:** the `<session>` path component (a string, not a tmux pane id).

**stdout (local):** not applicable — the worker runs on Cloudflare's edge.
The instance id is derived from the request URL.

## Stop contract

```
DELETE /agent/<session>   (or let the Worker handler resolve normally)
```

CF Workers terminate after the fetch handler resolves, so teardown is implicit.
For a persistent warm agent, graduate to a Durable Object (phase 5 of the
roadmap).

## Synadia metadata

| Field | Value |
|-------|-------|
| `executor` | `wasm` |
| `location` | `edge` |

## Prerequisites

1. A NATS server with WebSocket enabled:

   ```
   websocket { port: 8080, no_tls: true }
   ```

2. A Cloudflare account with Workers enabled.

3. Wrangler CLI: `npm install -g wrangler`

## Deployment

```bash
# Set secrets (run once per deployment):
wrangler secret put NATS_WS_URL        # ws://your-hub:8080
wrangler secret put OPENROUTER_API_KEY  # sk-or-...

# Deploy:
cd executors/wasm/cf-worker
wrangler deploy

# Verify:
curl https://<worker>.workers.dev/health
```

Set `OPEN_AGENT_OWNER` in `wrangler.toml` to your namespace (e.g. GitHub
username). This scopes all agent subjects under that owner token.

## Local development

```bash
cd executors/wasm/cf-worker
wrangler dev
# Worker runs at http://localhost:8787
# POST /agent/<session> to start an agent run
```

See `wrangler.toml` for full configuration options and inline comments.
