# executors/wasm/cf-durable-object/

Cloudflare Durable Object executor: hosts a **persistent** Synadia open-agent
bridge on Cloudflare's edge. Where `executors/wasm/cf-worker/` spins up one
bridge per request and tears it down when the handler resolves, this executor
keeps the bridge resident across fetches so the agent stays registered on
`$SRV.INFO.agents` for the whole warm window of its DO.

This is phase 5 of `docs/multi-executor-workers.md`.

## File tree

```
executors/wasm/cf-durable-object/
├── package.json
├── tsconfig.json
├── wrangler.toml
├── README.md
└── src/
    ├── index.ts          ← AgentDO class + module router + cron scheduled()
    └── local-sandbox.ts  ← Sandbox stub (fs/exec ops throw NotSupported)
```

## How a session resolves

```
client ──fetch /agent/<session>/<verb>──▶ module worker.fetch
                                              │
                                              ▼
                                         env.AGENT_DO.idFromName(<session>)
                                              │
                                              ▼
                                         AgentDO.fetch  (per-session stub)
                                              │
                       ┌──────────────────────┼──────────────────────┐
                       ▼                      ▼                      ▼
                  runBridge once          alarm() every 5m       reply
                  (lazy init)             keeps DO warm          200 OK
```

`idFromName(<session>)` is the routing key: every fetch for the same session
lands on the **same DO instance**, which holds the live NATS-over-WebSocket
connection and the in-flight `runBridge()` return value. The agent name on the
NATS bus matches cf-worker:

```
agents.prompt.open-agent.<OPEN_AGENT_OWNER>.<session>
```

## Routes

| Method  | Path                          | Behavior                                           |
| ------- | ----------------------------- | -------------------------------------------------- |
| GET     | `/health`                     | Module-level liveness — does not enter a DO.       |
| GET     | `/agent/<session>/status`     | Initializes the bridge if cold; returns warm flag. |
| GET     | `/agent/<session>/health`     | Per-DO health (bridge + last start error).         |
| POST    | `/agent/<session>/tickle`     | No-op — used by the cron / external pingers.       |
| DELETE  | `/agent/<session>/stop`       | Drains the bridge, closes NATS, clears the alarm.  |

All "real" agent traffic flows over NATS, **not** HTTP — clients send prompts
to the `agents.prompt.open-agent.<owner>.<session>` subject and the bridge
replies on the bus. HTTP exists only to (a) cold-start the DO, (b) keep it
warm, and (c) tear it down explicitly.

## Keepalive: alarms over setInterval

Durable Objects evict when idle. We use **CF's storage alarm primitive**
(`state.storage.setAlarm`) — not `setInterval` — because:

- `setInterval` lives in JS heap; it dies the moment the DO is evicted.
- Alarms are persisted in CF storage and survive eviction. When an alarm fires,
  CF re-instantiates the DO and runs `alarm()`, which gives us a hook to
  re-establish the bridge if it was lost.
- The cron in `wrangler.toml` (`*/5 * * * *`) tickles each session listed in
  `OPEN_AGENT_WARM_SESSIONS` so fresh deploys repopulate without a manual
  client fetch. This is belt-and-braces on top of the per-DO alarm.

Cadence is 5 minutes — comfortably inside CF's idle eviction window and
aligned with Synadia agent-service heartbeats so `$SRV.INFO.agents` always
sees a fresh bridge.

If your agent needs a tighter SLA, drop the cron to `* * * * *` (1 min) and
adjust `KEEPALIVE_MS` in `src/index.ts` to match. Going below 1 minute means
paying CF's request budget for tickles you don't strictly need; going above
10 minutes risks the bridge being un-registered when the next prompt arrives.

## Multi-turn conversations

`runBridge()` holds whatever session state the underlying `@synadia-ai/open-agent`
keeps in memory — model context, tool history, etc. Because the DO survives
between fetches, multi-turn flows that talk to the **same `<session>` name**
hit the **same bridge instance**, which is the same `runBridge` return value,
which is the same agent-service instance on NATS. From a Synadia microservice
perspective, the agent is one continuous service that happens to live on CF.

**Caveat:** if `@synadia-ai/open-agent` ever decides a session-resumption hook
needs explicit re-hydration (e.g. reading prior context from JetStream on
restart), the DO would need to call it inside `ensureBridge()`. Today open-agent
does not expose such a hook publicly — the bridge is stateless w.r.t. CF
storage, and multi-turn correctness depends on the DO staying warm. The
keepalive design above is what makes that assumption hold.

## Synadia metadata

| Field      | Value                |
| ---------- | -------------------- |
| `executor` | `wasm`               |
| `location` | `edge`               |
| `lifetime` | `persistent`         |

Compare with `cf-worker` which advertises `lifetime: ephemeral`.

## Prerequisites

1. A NATS server reachable over WebSocket. Add to `nats.conf`:

   ```
   websocket { port: 8080, no_tls: true }
   ```

   (For TLS production, swap to `wss://` and configure certs.)

2. A Cloudflare account with Workers + Durable Objects enabled (DOs require a
   paid Workers plan or a Workers Free account that explicitly opts in to DOs).

3. Wrangler CLI: `npm install -g wrangler` (>= 3.114).

## Deployment

```bash
cd executors/wasm/cf-durable-object
npm install

# Secrets — set once per deployment:
wrangler secret put NATS_WS_URL          # ws://your-hub:8080
wrangler secret put OPENROUTER_API_KEY   # sk-or-...

# Validate config before pushing:
npx tsc --noEmit
npx wrangler deploy --dry-run

# Deploy for real:
wrangler deploy

# Smoke check:
curl https://<worker>.workers.dev/health
curl https://<worker>.workers.dev/agent/demo/status
```

Adjust `OPEN_AGENT_OWNER` in `wrangler.toml` to your namespace (e.g. your
GitHub username) — it scopes every NATS subject this DO publishes on.

To keep specific sessions warm via cron, set
`OPEN_AGENT_WARM_SESSIONS = "alpha,beta,demo"` in `wrangler.toml` `[vars]`.

## Local development

```bash
cd executors/wasm/cf-durable-object
wrangler dev
# DO worker runs at http://localhost:8787
# curl http://localhost:8787/health
# curl http://localhost:8787/agent/demo/status
```

`wrangler dev` simulates DOs locally with persistent storage in `.wrangler/`.

## Local verification (run before pushing)

```bash
cd executors/wasm/cf-durable-object
npm install
npx tsc --noEmit                 # type-check
npx wrangler deploy --dry-run    # validates wrangler.toml + bundling
```

Both must pass.

**Note on `npm install`:** dependency pinning here mirrors
`executors/wasm/cf-worker/` exactly (same `@synadia-ai/*`, `@nats-io/*`,
`wrangler`, and `@cloudflare/workers-types` versions). If those packages are
unavailable on the live npm registry at install time — which is currently the
case for `@synadia-ai/open-agent` and `@nats-io/transport-websockets` — both
executors fail to install with the same error. Bump both directories together
once the upstream packages republish or rename. The wrangler config in
`wrangler.toml` validates standalone (`wrangler deploy --dry-run` parses the
DO binding, migration, and cron schema without needing the runtime deps).

## Future: sesh-published NATS_WS_URL

Today this directory uses a **sidecar `nats-server`** with WebSocket enabled
and `wrangler secret put NATS_WS_URL` — same workaround as `cf-worker`.

When sesh advertises `nats_ws_url` in its session JSON
([danmestas/sesh#59](https://github.com/danmestas/sesh/issues/59), blocked on
[danmestas/EdgeSync#176](https://github.com/danmestas/EdgeSync/issues/176)
which adds WebSocket support to the hub), the secret step is replaced by:

```bash
wrangler secret put NATS_WS_URL \
  --value "$(jq -r .nats_ws_url < .sesh/sessions/<label>.json)"
```

That keeps this PR independently shippable: today's deploy works against any
nats-server with WebSocket enabled; tomorrow's wiring is a one-line swap.

## Related

- `executors/wasm/cf-worker/` — ephemeral (per-request) cousin of this executor.
- `docs/multi-executor-workers.md` — phases and roadmap.
- orch#92 — mixed-executor benchmark; will reference this DO for the
  persistent-discovery assertion (no wiring needed from this PR's side).
