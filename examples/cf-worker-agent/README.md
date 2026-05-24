# CF Worker Agent — Deploy + Verify Guide

This example shows how to deploy orch's `executors/wasm/cf-worker/` as a
Cloudflare Worker, verify it registers as an `agents` microservice, and
send it a prompt via raw `nats req`.

The code lives in `executors/wasm/cf-worker/`. This directory is a guide
only — no additional files needed here.

---

## Prerequisites

- [Node.js 20+](https://nodejs.org) or [Bun](https://bun.sh)
- [Wrangler CLI](https://developers.cloudflare.com/workers/wrangler/):
  `npm install -g wrangler`
- A running NATS server with **WebSocket** support (see below)
- An [OpenRouter](https://openrouter.ai) API key (or Vercel AI Gateway key)

---

## Step 1 — Expose a WebSocket NATS endpoint

CF Workers can speak WebSockets but not raw TCP. Your NATS server (or sesh
hub) must accept WebSocket connections.

**Option A — sesh hub with WS enabled (recommended for sesh users)**

`nats-server` enables WebSocket via a config file (there is no CLI flag for
the WebSocket subsystem). Create `nats.conf`:

```conf
# nats.conf
port: 4222

websocket {
  port: 8080
  no_tls: true   # set to false and add `tls { ... }` for production
}
```

Then run:

```sh
nats-server -c nats.conf
```

Verify the WS port is listening: `curl -sI http://localhost:8080` should
return a `400 Bad Request` (NATS WS expects an Upgrade handshake, not a
plain GET — the 400 confirms the port is bound).

Expose port 8080 publicly (or via Cloudflare Tunnel / Tailscale Funnel if
you want TLS without a certificate — see below).

**Option B — local wrangler dev mode (no public endpoint needed)**

`wrangler dev` runs the worker locally and can reach `ws://localhost:8080`
directly. This is the easiest smoke-test path. Skip the public-exposure step
and set `NATS_WS_URL=ws://localhost:8080` in step 3.

**TLS note:** For production deploys, `wss://` is strongly recommended.
[Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/)
or [Tailscale Funnel](https://tailscale.com/kb/1223/funnel) both give you a
`wss://` URL in front of a local NATS WebSocket port without buying a
certificate.

---

## Step 2 — Install dependencies

```sh
cd executors/wasm/cf-worker
npm install          # or: bun install
```

---

## Step 3 — Configure secrets

```sh
# Required: NATS WebSocket URL
wrangler secret put NATS_WS_URL
# → enter: ws://localhost:8080   (dev)  or  wss://hub.example.com:443 (prod)

# Required: model API key (choose one)
wrangler secret put OPENROUTER_API_KEY   # if OPEN_AGENT_PROVIDER=openrouter (default)
wrangler secret put AI_GATEWAY_API_KEY   # if OPEN_AGENT_PROVIDER=gateway
```

Edit `wrangler.toml` to set `OPEN_AGENT_OWNER` to something meaningful (e.g.
your GitHub username). This becomes the 4th token in the NATS subject:
`agents.prompt.open-agent.<owner>.<session>`.

---

## Step 4 — Run locally with wrangler dev

```sh
# Terminal 1: start NATS with WebSocket support (uses nats.conf from step 1)
nats-server -c nats.conf

# Terminal 2: start the worker
cd executors/wasm/cf-worker
wrangler dev

# Terminal 3: trigger the worker (starts the bridge for session "demo")
curl http://localhost:8787/agent/demo &

# Verify registration — should show an open-agent entry
nats --server nats://localhost:4222 \
  req '$SRV.INFO.agents' '' --timeout 5s
```

The `$SRV.INFO.agents` response lists all registered microservices. Look for:

```json
{
  "name": "open-agent",
  "description": "open-agent bridge for <owner>/demo",
  ...
}
```

---

## Step 5 — Send a prompt

Because `orch tell`'s Synadia integration is planned for a future PR (see
[orch#59](https://github.com/danmestas/orch/issues/59)), use raw `nats req`
to talk to the worker directly:

```sh
OWNER="$(grep OPEN_AGENT_OWNER executors/wasm/cf-worker/wrangler.toml | awk -F'"' '{print $2}')"

nats --server nats://localhost:4222 \
  req "agents.prompt.open-agent.${OWNER}.demo" \
  --replies=0 --reply-timeout=30s --timeout=5m \
  "Say hello and tell me what tools you have available."
```

Response chunks arrive as JSON lines; the final chunk has `"type":"terminator"`.
A dumb client (plain `nats req`) sees the model's text without tool-call details;
a rich client parsing `"type":"status"` chunks gets structured tool events.

---

## Step 6 — Deploy to Cloudflare (optional)

```sh
wrangler deploy
```

Then invoke the worker at its `*.workers.dev` URL:

```sh
curl https://orch-cf-agent.<your-subdomain>.workers.dev/agent/my-session &
```

The bridge persists for the duration of the HTTP request. CF Worker
execution contexts time out (typically 30 s CPU / unlimited wall-clock for
active connections). For a persistent warm agent, use Durable Objects
(phase 5 in `docs/multi-executor-workers.md`).

---

## Subject layout

| Subject | Direction | Description |
|---|---|---|
| `agents.prompt.open-agent.<owner>.<session>` | caller → worker | Send a prompt; receive streaming response chunks |
| `agents.status.open-agent.<owner>.<session>` | caller → worker | Query worker status (heartbeat, metadata) |
| `$SRV.INFO.agents` | operator | List all registered agent microservices |

---

## Known gaps / future work

- **NATS transport:** This POC uses `@nats-io/transport-websockets` (WS).
  The iroh-bridged NATS path described in `docs/multi-executor-workers.md`
  §Cross-network connectivity is future work — iroh's Rust-native transport
  does not yet have a CF Worker-compatible JS shim.

- **Sandbox:** The `LocalSandbox` stub in `src/local-sandbox.ts` throws
  `NotSupported` for all file-system and exec operations. The agent loop
  runs, but tool calls that require a real sandbox will error. Options:
  1. Forward fs/exec calls over HTTP to a privileged sidecar running on a
     native executor (the hybrid WASM-at-top / native-at-bottom topology).
  2. Use `@vercel/sandbox` once Vercel exposes a CF-compatible SDK.
  3. Limit CF Worker agents to pure-reasoning tasks (planners, routers,
     watchers) and delegate tool-heavy work to native workers via the task
     queue.

- **Persistence:** Each `fetch` invocation starts a fresh bridge (no
  conversation history across requests). Graduate to Durable Objects (phase
  5) for stateful multi-turn sessions.

- **Auth:** The POC uses no NATS auth. Production deployments should
  configure per-worker nkeys with scoped subject permissions as described in
  `docs/multi-executor-workers.md` §Risks.

- **Private-package version skew:** `@synadia-ai/open-agent` is a private
  pre-release package (the synadia-agents monorepo, not yet on the public
  npm registry). Future versions may change the `RunBridgeOptions` shape
  (`sandboxFactory` signature, `modelFactory` contract, `SandboxState`
  internals). Pin to the exact tested version (or a git SHA) and re-verify
  the `tsc --noEmit` pass when bumping. The current `package.json` pins
  `@synadia-ai/open-agent@0.0.1` — treat any version change as a breaking
  bump until the package stabilizes.
