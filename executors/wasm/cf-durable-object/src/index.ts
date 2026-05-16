// CF Durable Object entry — hosts a long-lived Synadia open-agent bridge keyed
// by session name. Unlike cf-worker (one bridge per fetch), the DO instance
// persists between fetches, so the same agents.prompt.open-agent.<owner>.<session>
// microservice stays registered on $SRV.INFO.agents across multiple requests.
//
// Architecture:
//
//   client ──fetch /agent/<session>/...──▶ module worker.fetch
//                                              │
//                                              ▼
//                                         env.AGENT_DO.idFromName(<session>)
//                                              │
//                                              ▼
//                                         AgentDO.fetch (stub)
//                                              │
//                       ┌──────────────────────┼──────────────────────┐
//                       │                      │                      │
//                       ▼                      ▼                      ▼
//                  runBridge once          alarm()                  reply
//                  (lazy init)             persistent timer         200 OK
//                       │                      │
//                       ▼                      ▼
//                  NATS WS ↔ hub           setAlarm(+5min)
//                  registers $SRV          (DO stays warm)
//
// Keepalive strategy:
//   The DO uses Cloudflare's storage alarm primitive (state.storage.setAlarm)
//   to re-arm itself every 5 minutes. While an alarm is pending the DO is not
//   evicted from memory, which means the bridge's NATS connection — and its
//   $SRV.INFO.agents registration — stays live across the warm window. A
//   module-level scheduled() cron tickles each warm session listed in
//   OPEN_AGENT_WARM_SESSIONS so fresh deploys repopulate without a manual
//   client fetch.
//
// Why alarms over setInterval: CF Workers' setInterval doesn't survive eviction,
// so the timer dies the moment the DO is unloaded. Alarms are durable storage —
// CF wakes the DO when the alarm fires, which is the canonical persistent-timer
// pattern (see CF docs: "Use the Alarms API for guaranteed wake-ups").

import { connect, type NatsConnection } from "@nats-io/transport-websockets";
import { runBridge, openRouterModelFactory, gatewayModelFactory } from "@synadia-ai/open-agent";
import type { SandboxBundle } from "@synadia-ai/open-agent";
import { buildCfSandbox } from "./local-sandbox.js";

export interface Env {
  /** Durable Object binding declared in wrangler.toml. */
  AGENT_DO: DurableObjectNamespace;
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
  /** Comma-separated session names the cron should pre-warm. Optional. */
  OPEN_AGENT_WARM_SESSIONS?: string;
}

/** runBridge's exact return type is internal to open-agent; capture the shape we use. */
interface Bridge {
  stop(): Promise<void>;
}

// 5 minutes — comfortably inside CF's idle eviction window (~10m for active DOs
// per CF guidance) and aligned with Synadia agent-service heartbeat cadence so
// $SRV.INFO.agents stays fresh.
const KEEPALIVE_MS = 5 * 60 * 1000;

export class AgentDO implements DurableObject {
  private bridge: Bridge | null = null;
  private nc: NatsConnection | null = null;
  private session: string | null = null;
  private startError: unknown = null;

  constructor(
    private readonly state: DurableObjectState,
    private readonly env: Env,
  ) {}

  /** Lazy-init the bridge. Idempotent: subsequent calls are no-ops once warmed. */
  private async ensureBridge(session: string): Promise<void> {
    if (this.bridge) return;
    // Concurrency: DOs serialize fetch handlers by default (single-threaded
    // event loop per instance), so we don't need an explicit mutex here.
    this.session = session;

    try {
      const nc = await connect({ servers: [this.env.NATS_WS_URL] });
      this.nc = nc;

      const provider = this.env.OPEN_AGENT_PROVIDER ?? "openrouter";
      const modelFactory =
        provider === "gateway"
          ? gatewayModelFactory()
          : openRouterModelFactory({ apiKey: this.env.OPENROUTER_API_KEY });

      const sandboxFactory = async (sessionId: string): Promise<SandboxBundle> => {
        const sandbox = buildCfSandbox(sessionId);
        return {
          sandbox,
          // Same "cloud" sentinel as cf-worker — runBridge passes the pre-built
          // sandbox through SandboxBundle.sandbox and does not re-resolve via
          // the factory. If open-agent ever changes that, this cast breaks.
          state: { type: "cloud" } as unknown as import("@synadia-ai/open-agent").SandboxState,
        };
      };

      this.bridge = await runBridge({
        nc,
        owner: this.env.OPEN_AGENT_OWNER,
        session,
        sandboxFactory,
        modelFactory,
        modelId: this.env.OPEN_AGENT_MODEL_ID ?? "anthropic/claude-3-5-haiku",
      });

      // Arm the keepalive alarm. While this is set, CF will not evict the DO
      // and will wake us if it does, re-running alarm() below.
      await this.state.storage.setAlarm(Date.now() + KEEPALIVE_MS);
    } catch (err) {
      this.startError = err;
      // Best-effort cleanup so the next request can retry from a clean state.
      try { await this.nc?.close(); } catch { /* ignore */ }
      this.nc = null;
      this.bridge = null;
      throw err;
    }
  }

  /**
   * Alarm handler — keeps the DO warm and the bridge registered.
   *
   * CF wakes the DO when the alarm fires even if no fetch is pending, which is
   * how we guarantee $SRV.INFO.agents stays discoverable across idle windows.
   * We re-arm at the tail so the cycle continues until something explicitly
   * tears the bridge down via DELETE /agent/<session>.
   */
  async alarm(): Promise<void> {
    if (!this.bridge) {
      // Lost the bridge across an eviction. Re-init from the persisted session
      // so the agent service rejoins the bus automatically.
      const persisted = (await this.state.storage.get<string>("session")) ?? this.session;
      if (persisted) {
        try {
          await this.ensureBridge(persisted);
        } catch {
          // ensureBridge will retry on the next alarm tick.
        }
      }
    }
    // Always re-arm — even on bridge-init failure — so we keep retrying.
    await this.state.storage.setAlarm(Date.now() + KEEPALIVE_MS);
  }

  async fetch(request: Request): Promise<Response> {
    const url = new URL(request.url);
    // The path inside the DO mirrors the outer route minus the routing prefix:
    //   outer: /agent/<session>/<verb>   →   inner: /<verb>
    // The module worker rewrites before forwarding (see default export below).
    const verb = url.pathname.replace(/^\/+/, "") || "status";

    // Sessions are addressed by idFromName(<session>) at the module layer; we
    // also accept a header so the DO can persist its session across alarms
    // (idFromName isn't reversible at runtime — only the caller knows the name).
    const session =
      request.headers.get("x-orch-session") ??
      (await this.state.storage.get<string>("session")) ??
      url.searchParams.get("session") ??
      "";

    if (verb === "health") {
      return jsonResponse({
        ok: this.bridge !== null,
        agent: "open-agent",
        session,
        warm: this.bridge !== null,
        startError: this.startError ? String(this.startError) : null,
      });
    }

    if (verb === "stop") {
      await this.teardown();
      return jsonResponse({ ok: true, stopped: true, session });
    }

    if (!session) {
      return new Response("missing session — caller must set x-orch-session", { status: 400 });
    }

    // Persist the name on first contact so alarm() can self-revive after eviction.
    await this.state.storage.put("session", session);

    try {
      await this.ensureBridge(session);
    } catch (err) {
      return jsonResponse({ ok: false, error: String(err), session }, { status: 502 });
    }

    if (verb === "tickle") {
      // No-op endpoint cron uses to re-arm the alarm and confirm the bridge
      // is still up. Returning 200 here is the assertion that the agent is
      // currently registered on $SRV.INFO.agents.
      return jsonResponse({ ok: true, warm: true, session });
    }

    if (verb === "status") {
      return jsonResponse({
        ok: true,
        warm: this.bridge !== null,
        session,
        owner: this.env.OPEN_AGENT_OWNER,
      });
    }

    return new Response(
      "Usage: GET /agent/<session>/{status,health,tickle}  |  DELETE /agent/<session>/stop",
      { status: 404 },
    );
  }

  private async teardown(): Promise<void> {
    // Order matters: bridge.stop() FIRST drains AgentService (in-flight
    // prompts, heartbeats) while NATS is still open, then nc.close() tears
    // down the underlying connection. Reversing causes the service to lose
    // its sink mid-drain.
    try { await this.bridge?.stop(); } catch { /* ignore */ }
    try { await this.nc?.close(); } catch { /* ignore */ }
    this.bridge = null;
    this.nc = null;
    await this.state.storage.deleteAlarm();
    await this.state.storage.delete("session");
  }
}

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    ...init,
    headers: { "content-type": "application/json", ...(init.headers ?? {}) },
  });
}

/**
 * Module-level handler — routes /agent/<session>/<verb> into the right DO stub
 * and exposes /health at the worker layer (doesn't enter a DO).
 */
export default {
  async fetch(request: Request, env: Env, _ctx: ExecutionContext): Promise<Response> {
    const url = new URL(request.url);

    if (url.pathname === "/health") {
      return jsonResponse({ ok: true, role: "router", agent: "open-agent" });
    }

    // Expected path: /agent/<session>[/<verb>]
    const match = url.pathname.match(/^\/agent\/([^/]+)(\/.*)?$/);
    if (!match) {
      return new Response(
        "Usage: /agent/<session>/{status,health,tickle}  (or /health on the router)",
        { status: 400 },
      );
    }
    const session = match[1];
    const inner = match[2] ?? "/status";

    const id = env.AGENT_DO.idFromName(session);
    const stub = env.AGENT_DO.get(id);

    // Forward with the session pinned in a header so the DO can persist it
    // across eviction cycles.
    const innerUrl = new URL(inner, url.origin);
    const fwd = new Request(innerUrl.toString(), request);
    fwd.headers.set("x-orch-session", session);
    return stub.fetch(fwd);
  },

  /**
   * Cron handler — fires on the schedule declared in wrangler.toml. Pokes
   * each session listed in OPEN_AGENT_WARM_SESSIONS so the DO stays alive
   * even when client traffic is quiet. This is the "fresh deploys repopulate
   * without a manual fetch" assurance the README references.
   */
  async scheduled(_event: ScheduledEvent, env: Env, ctx: ExecutionContext): Promise<void> {
    const raw = env.OPEN_AGENT_WARM_SESSIONS ?? "";
    const sessions = raw.split(",").map((s) => s.trim()).filter(Boolean);
    for (const session of sessions) {
      const id = env.AGENT_DO.idFromName(session);
      const stub = env.AGENT_DO.get(id);
      // Fire-and-forget; CF holds the cron handler open for ctx.waitUntil work.
      ctx.waitUntil(
        (async () => {
          try {
            const req = new Request("https://internal/tickle", {
              headers: { "x-orch-session": session },
            });
            await stub.fetch(req);
          } catch {
            // A tickle failure just means the next cron retries — the DO's
            // own alarm() also retries independently.
          }
        })(),
      );
    }
  },
};
