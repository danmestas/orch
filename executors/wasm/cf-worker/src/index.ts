// CF Worker entry point — bootstraps open-agent as a NATS microservice.
//
// Transport note:
//   CF Workers cannot open TCP sockets, so the standard @nats-io/transport-node
//   (TCP) client won't work. This entry point uses @nats-io/transport-websockets,
//   which speaks NATS-over-WebSocket (the same protocol nats-server exposes on
//   its --websocket port). Set NATS_WS_URL to ws[s]://host:port in wrangler.toml
//   or via `wrangler secret put NATS_WS_URL`.
//
//   Iroh-bridged NATS is future work (see docs/multi-executor-workers.md §phase 4).
//   For now, expose the hub's WebSocket NATS port (or proxy it) and point this
//   worker at it.
//
// Lifecycle:
//   Each incoming `fetch` request keeps the worker alive for the duration of
//   the NATS connection + agent run. CF Workers terminate after the handler
//   resolves, so the bridge is per-request, not persistent. For a persistent
//   warm agent, graduate this to a Durable Object (phase 5).

import { connect } from "@nats-io/transport-websockets";
import { runBridge, openRouterModelFactory, gatewayModelFactory } from "@synadia-ai/open-agent";
import { buildCfSandbox } from "./local-sandbox.js";
import type { SandboxBundle } from "@synadia-ai/open-agent";

export interface Env {
  /** WebSocket NATS URL, e.g. wss://hub.example.com:443 */
  NATS_WS_URL: string;
  /** Arbitrary owner token — used as the 4th subject-token in agents.prompt.open-agent.<owner>.<session> */
  OPEN_AGENT_OWNER: string;
  /** "gateway" or "openrouter" (default: "openrouter") */
  OPEN_AGENT_PROVIDER?: string;
  /** Model id forwarded to the configured model factory */
  OPEN_AGENT_MODEL_ID?: string;
  /** OpenRouter API key (required when OPEN_AGENT_PROVIDER=openrouter) */
  OPENROUTER_API_KEY?: string;
  /** Vercel AI Gateway key (required when OPEN_AGENT_PROVIDER=gateway) */
  AI_GATEWAY_API_KEY?: string;
}

export default {
  async fetch(request: Request, env: Env, _ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);

    // Health-check — useful for wrangler dev verification.
    if (url.pathname === "/health") {
      return new Response(JSON.stringify({ ok: true, agent: "open-agent" }), {
        headers: { "content-type": "application/json" },
      });
    }

    // Derive session from the URL path: /agent/<session>
    // Example: fetch("https://worker.dev/agent/my-session")
    // → registers on agents.prompt.open-agent.<owner>.my-session
    const match = url.pathname.match(/^\/agent\/([^/]+)$/);
    if (!match) {
      return new Response(
        "Usage: POST /agent/<session>  (or GET /health)",
        { status: 400 },
      );
    }
    const session = match[1];
    const owner = env.OPEN_AGENT_OWNER || "worker";

    const nc = await connect({ servers: [env.NATS_WS_URL] });

    const provider = env.OPEN_AGENT_PROVIDER ?? "openrouter";
    const modelFactory =
      provider === "gateway"
        ? gatewayModelFactory()
        : openRouterModelFactory({ apiKey: env.OPENROUTER_API_KEY });

    const sandboxFactory = async (sessionId: string): Promise<SandboxBundle> => {
      const sandbox = buildCfSandbox(sessionId);
      return {
        sandbox,
        // The SandboxState type only knows "local" and "vercel"; we use "cloud"
        // as a sentinel signaling "this sandbox is not factory-resolved —
        // connectSandbox(state) is never called for it." The cast works at
        // runtime because runBridge passes our pre-built sandbox through
        // directly via SandboxBundle.sandbox and never re-resolves it through
        // the factory. If open-agent ever changes its internal dispatch to
        // re-call connectSandbox(state), this assumption breaks.
        state: { type: "cloud" } as unknown as import("@synadia-ai/open-agent").SandboxState,
      };
    };

    const bridge = await runBridge({
      nc,
      owner,
      session,
      sandboxFactory,
      modelFactory,
      modelId: env.OPEN_AGENT_MODEL_ID ?? "anthropic/claude-3-5-haiku",
    });

    // Keep the worker alive until the NATS connection closes or the CF
    // execution context times out. In wrangler dev, send a manual drain via
    // the NATS management plane; in production, the DO version (phase 5) is
    // the right host for long-lived agents.
    //
    // Order matters: bridge.stop() FIRST drains AgentService (in-flight
    // prompts, heartbeats) while NATS is still open, then nc.closed()
    // waits for the underlying connection to actually tear down. If we
    // reverse the order, by the time bridge.stop() runs the connection
    // is already gone and the service can't drain gracefully.
    await bridge.stop();
    await nc.closed();

    return new Response(JSON.stringify({ ok: true, owner, session }), {
      headers: { "content-type": "application/json" },
    });
  },
};
