// pi SessionStart NATS publish extension — on session_start, spawn a
// backgrounded `tail -F | nats pub` that streams each transcript line to
// NATS. Mirrors the claude-side hooks/orch-nats-publish-jsonl.sh.
//
// Subject: orch.events.<pane_num>     (pane id "%37" becomes "37")
// Body:    one JSONL line verbatim    (each pi transcript entry)
//
// Pi writes transcripts at:
//   ~/.pi/agent/sessions/<encoded-cwd>/<timestamp>_<session_uuid>.jsonl
// where <encoded-cwd> replaces path segments with `-` (e.g.
// `/Users/d/p/proj` → `--Users-d-p-proj`).
//
// The session_id arrives via the session_start ctx; the full transcript path
// is resolved by globbing for the suffix `_<session_id>.jsonl` under the
// encoded-cwd dir. Tailer is backgrounded, detached, and PID-gated so
// re-entry (e.g. /resume) doesn't double-spawn.
//
// Auto-discovered location: ~/.pi/agent/extensions/orch-nats-publish-jsonl.ts

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { spawn } from "node:child_process";
import { promises as fs, existsSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

function encodeCwd(cwd: string): string {
  // Pi's encoding: path separators (`/`) and dots (`.`) both become `-`.
  // Matches the observed pattern `--Users-dmestas-projects-darken` for
  // `/Users/dmestas/projects/darken`.
  return cwd.replace(/[/.]/g, "-");
}

async function findTranscript(
  sessionsDir: string,
  sessionId: string,
  deadlineMs: number
): Promise<string | null> {
  while (Date.now() < deadlineMs) {
    try {
      const entries = await fs.readdir(sessionsDir);
      for (const entry of entries) {
        if (entry.endsWith(`_${sessionId}.jsonl`)) {
          return join(sessionsDir, entry);
        }
      }
    } catch {
      // dir may not exist yet — keep polling
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  return null;
}

export default function (pi: ExtensionAPI) {
  pi.on("session_start", async (_event: any, ctx: any) => {
    const paneId = process.env.ORCH_PANE_ID;
    if (!paneId) return;

    const sessionId =
      ctx?.sessionId ??
      ctx?.session?.id ??
      ctx?.session?.sessionId ??
      "";
    if (!sessionId) return;

    const paneNum = paneId.replace(/^%/, "");
    const subjectPrefix = process.env.ORCH_NATS_SUBJECT_PREFIX ?? "orch";
    const subject = `${subjectPrefix}.events.${paneNum}`;
    const cwd = process.cwd();

    // PID gate — one tailer per (pane, session).
    const gateDir =
      process.env.ORCH_NATS_GATE_DIR ??
      join(homedir(), ".cache", "orch-nats-tailers");
    await fs.mkdir(gateDir, { recursive: true });
    const gateFile = join(gateDir, `${paneNum}-${sessionId}.pid`);
    if (existsSync(gateFile)) {
      try {
        const existing = parseInt((await fs.readFile(gateFile, "utf8")).trim(), 10);
        if (existing > 0) {
          try {
            process.kill(existing, 0); // probe — throws if dead
            return; // already tailing
          } catch {
            // dead pid — fall through and re-spawn
          }
        }
      } catch {
        // unreadable gate — fall through
      }
    }

    const encoded = encodeCwd(cwd);
    const sessionsDir = join(homedir(), ".pi", "agent", "sessions", encoded);

    const deadlineMs = Date.now() + 10_000;
    const jsonl = await findTranscript(sessionsDir, sessionId, deadlineMs);
    if (!jsonl) return; // transcript never materialized

    // Publish a tailer-start marker so subscribers know we're alive.
    const startBody = JSON.stringify({
      event: "jsonl_tailer_start",
      harness: "pi",
      pane_id: paneId,
      session_id: sessionId,
      cwd,
      jsonl,
      ts_ns: Number((BigInt(Date.now()) * 1_000_000n).toString()),
    });
    try {
      const probe = spawn("nats", ["pub", "--timeout=1s", subject, startBody], {
        stdio: "ignore",
        detached: true,
      });
      probe.unref();
    } catch {
      return; // nats CLI missing — can't proceed
    }

    // Detached `tail -F <jsonl> | while read; do nats pub ... done`.
    // We assemble it as a shell pipeline so backpressure / line-buffering
    // matches the bash hook exactly.
    const cmd = `tail -F -n0 ${shellQuote(jsonl)} 2>/dev/null | while IFS= read -r line; do [ -n "$line" ] || continue; nats pub --timeout=1s ${shellQuote(subject)} "$line" >/dev/null 2>&1 || true; done`;
    try {
      const tailer = spawn("bash", ["-c", cmd], {
        stdio: "ignore",
        detached: true,
      });
      tailer.unref();
      if (tailer.pid) {
        await fs.writeFile(gateFile, String(tailer.pid));
      }
    } catch {
      // tailer spawn failed — give up silently
    }
  });
}

function shellQuote(s: string): string {
  return `'${s.replace(/'/g, "'\\''")}'`;
}
