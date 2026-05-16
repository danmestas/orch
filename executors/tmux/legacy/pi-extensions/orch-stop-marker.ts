/**
 * @deprecated Superseded by the pi adapter in internal/adapter/pi/pi.go (orch#62).
 * This extension is retained for one release cycle for users who have not yet
 * migrated to the unified shim. It will be removed in a future release.
 *
 * See docs/orch-agent-shim.md for the migration path.
 */
// Harness Stop-marker extension for pi.
//
// Mirrors what `~/.claude/hooks/orch-stop-marker.sh` does for claude/codex:
// when an agent finishes a turn AND ORCH_PANE_ID is set in env, write the
// per-pane marker file at ~/.cache/orch-stop/<pane_id>.event and append a
// line to ~/.cache/orch-stop/events.log. This unifies pi into the same
// event-driven listener (`orch-listen`) the other agents use.
//
// Auto-discovered location: ~/.pi/agent/extensions/orch-stop-marker.ts

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { promises as fs } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

export default function (pi: ExtensionAPI) {
  pi.on("agent_end", async (_event: any, ctx: any) => {
    const paneId = process.env.ORCH_PANE_ID;
    if (!paneId) return;

    const dir = process.env.ORCH_STOP_DIR ?? join(homedir(), ".cache", "orch-stop");
    await fs.mkdir(dir, { recursive: true });

    // ms-precision nanoseconds — matches the bash hooks' format width
    const tsNs = (BigInt(Date.now()) * 1_000_000n).toString();
    const tsIso = new Date().toISOString();
    const sessionId =
      ctx?.sessionId ??
      ctx?.session?.id ??
      ctx?.session?.sessionId ??
      "";
    const cwd = process.cwd();

    const target = join(dir, `${paneId}.event`);
    const tmp = `${target}.${process.pid}.tmp`;
    const body =
      [
        `ts_ns=${tsNs}`,
        `ts_iso=${tsIso}`,
        `pane_id=${paneId}`,
        `session_id=${sessionId}`,
        `cwd=${cwd}`,
      ].join("\n") + "\n";

    try {
      await fs.writeFile(tmp, body);
      await fs.rename(tmp, target);
    } catch (e) {
      // never fail the agent_end on hook errors
    }

    const logPath = process.env.ORCH_EVENT_LOG ?? join(dir, "events.log");
    const entry = JSON.stringify({
      ts_ns: Number(tsNs),
      event: "Stop",
      pane_id: paneId,
      session_id: sessionId,
      cwd,
    });
    try {
      await fs.appendFile(logPath, entry + "\n");
    } catch (e) {
      // ignore
    }
  });
}
