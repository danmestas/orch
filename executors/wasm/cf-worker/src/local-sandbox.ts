// Minimal Sandbox stub for the CF Worker runtime.
//
// CF Workers have no real filesystem and cannot spawn subprocesses, so
// every file-system and exec operation throws NotSupported. This stub
// satisfies the Sandbox interface that open-agent's runBridge() requires,
// proving the seam is genuinely swappable. A real deployment would replace
// this with a thin HTTP adapter forwarding fs/exec calls to a privileged
// sidecar, or use @vercel/sandbox once Vercel exposes a CF-compatible SDK.
//
// The sandbox type is "cloud" (the only value the interface allows). The
// bridge uses sandbox.type to skip optional tools (e.g. it won't try to
// spawn a shell if type === "cloud"). With this stub, file I/O and exec
// tools (read/write/edit/grep/glob/bash) will error gracefully via the
// notSupported() helper. NATS-based tools (ask_user_question, web_fetch via
// HTTP fetch) are unaffected — they don't touch the sandbox at all.

import type {
  Sandbox,
  SandboxStats,
  ExecResult,
} from "@synadia-ai/open-agent";

function notSupported(op: string): never {
  throw new Error(`CF Worker sandbox: ${op} is not supported in this environment`);
}

export function buildCfSandbox(sessionId: string): Sandbox {
  const workingDirectory = `/sessions/${sessionId}`;
  return {
    type: "cloud",
    workingDirectory,

    async readFile(_path: string, _enc: "utf-8"): Promise<string> {
      notSupported("readFile");
    },
    async readFileBuffer(_path: string): Promise<Buffer> {
      notSupported("readFileBuffer");
    },
    async writeFile(_path: string, _content: string, _enc: "utf-8"): Promise<void> {
      notSupported("writeFile");
    },
    async stat(_path: string): Promise<SandboxStats> {
      notSupported("stat");
    },
    async access(_path: string): Promise<void> {
      notSupported("access");
    },
    async mkdir(_path: string, _opts?: { recursive?: boolean }): Promise<void> {
      notSupported("mkdir");
    },
    async readdir(_path: string, _opts: { withFileTypes: true }) {
      notSupported("readdir");
    },
    async exec(
      _command: string,
      _cwd: string,
      _timeoutMs: number,
      _opts?: { signal?: AbortSignal },
    ): Promise<ExecResult> {
      notSupported("exec");
    },
    async stop(): Promise<void> {
      // No-op: nothing to tear down.
    },
  };
}
