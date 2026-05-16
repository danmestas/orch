// pi Stop NATS publish extension — fires on agent_end + publishes a stop
// event onto NATS, mirroring the claude-side hooks/orch-nats-publish-stop.sh.
//
// Subject: orch.stop.<pane_num>     (pane id "%37" becomes "37")
// Body:    JSON  {event, harness, pane_id, session_id, cwd, ts_ns, ts_iso}
//
// Sibling to the existing orch-stop-marker.ts extension (which writes the
// filesystem marker for orch-listen). This one is the NATS fan-out for
// sesh-aware listeners. Both safe to install together; both ORCH_PANE_ID-gate.
//
// Uses the `nats` CLI via child_process to avoid adding a Node dependency.
// Matches the bash-hook strategy (consistency + no version-skew on a NATS
// client library).
//
// Auto-discovered location: ~/.pi/agent/extensions/orch-nats-publish-stop.ts

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawn } from "node:child_process";

export default function (pi: ExtensionAPI) {
  pi.on("agent_end", async (_event: any, ctx: any) => {
    const paneId = process.env.ORCH_PANE_ID;
    if (!paneId) return;

    const paneNum = paneId.replace(/^%/, "");
    const subjectPrefix = process.env.ORCH_NATS_SUBJECT_PREFIX ?? "orch";
    const subject = `${subjectPrefix}.stop.${paneNum}`;

    const tsNs = (BigInt(Date.now()) * 1_000_000n).toString();
    const tsIso = new Date().toISOString();
    const sessionId =
      ctx?.sessionId ??
      ctx?.session?.id ??
      ctx?.session?.sessionId ??
      "";
    const cwd = process.cwd();

    const body = JSON.stringify({
      event: "stop",
      harness: "pi",
      pane_id: paneId,
      session_id: sessionId,
      cwd,
      ts_ns: Number(tsNs),
      ts_iso: tsIso,
    });

    // Fire-and-forget. Detached so we don't block agent_end on the publish.
    // --timeout=1s caps the wait if NATS is unreachable.
    try {
      const proc = spawn("nats", ["pub", "--timeout=1s", subject, body], {
        stdio: "ignore",
        detached: true,
      });
      proc.unref();
      proc.on("error", () => {
        // nats CLI missing or other spawn failure — silently no-op
      });
    } catch {
      // never fail agent_end on publish errors
    }
  });
}
