// Minimal Sandbox stub for the CF Durable Object runtime.
//
// Same constraints as cf-worker: no filesystem, no subprocesses. We expose a
// Sandbox interface satisfying open-agent's runBridge() with fs/exec ops that
// throw NotSupported. The bridge inspects sandbox.type and skips shell-only
// tools when type === "cloud", so the failure modes here are graceful.
//
// This file is intentionally a near-verbatim copy of cf-worker/src/local-sandbox.ts.
// Keep them in sync — the DO is a hosting wrapper around the same bridge contract;
// the sandbox shape is identical because the underlying SDK is identical.

import type {
  Sandbox,
  SandboxStats,
  ExecResult,
} from "@synadia-ai/open-agent";

function notSupported(op: string): never {
  throw new Error(`CF Durable Object sandbox: ${op} is not supported in this environment`);
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
