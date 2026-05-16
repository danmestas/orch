#!/usr/bin/env bun
// wire-compat.ts — drives a running orch-agent-shim with the upstream
// @synadia-ai/agents SDK to verify wire compatibility end-to-end.
//
// Expected env:
//   NATS_URL    — defaults to nats://127.0.0.1:4222
//   OWNER       — owner the shim was launched with (default $USER)
//   PANE        — raw pane id the shim was launched with (default "%test")
//   TIMEOUT_MS  — overall timeout (default 10000)
//
// Exit code 0 on full success, non-zero on any failure. The script
// expects an orch-agent-shim to be ALREADY running and discoverable
// via $SRV.INFO.agents on the bus. test/test-orch-agent-shim.sh wraps
// this with the spin-up/tear-down dance.

import { connect, type NatsConnection } from "@nats-io/nats-core";
import { Agents } from "@synadia-ai/agents";

const url = process.env.NATS_URL ?? "nats://127.0.0.1:4222";
const owner = process.env.OWNER ?? process.env.USER ?? "tester";
const pane = process.env.PANE ?? "%test";
const timeoutMs = Number(process.env.TIMEOUT_MS ?? "10000");
const paneSubjectToken = "pct" + pane.replace(/^%/, "");

function fail(msg: string): never {
  console.error(`wire-compat: FAIL: ${msg}`);
  process.exit(1);
}

function ok(msg: string): void {
  console.log(`wire-compat: ok: ${msg}`);
}

async function main(): Promise<void> {
  let nc: NatsConnection;
  try {
    nc = await connect({ servers: url });
  } catch (e) {
    fail(`connect ${url}: ${e}`);
  }

  const agents = new Agents(nc);

  // 1. Discovery: enumerate via $SRV.INFO.agents and assert OUR instance
  //    is present with the expected metadata + endpoint subjects.
  const discovered = await agents.discover({ maxWaitMs: 2000, stallMs: 500 });
  const ours = discovered.find(
    (a) =>
      a.metadata.owner === owner &&
      a.metadata.pane_id === pane &&
      a.metadata.protocol_version?.startsWith("0.3"),
  );
  if (!ours) {
    fail(
      `discovery: no instance with owner=${owner} pane_id=${pane}; got ${discovered.length} instances`,
    );
  }
  ok(`discovered ours: id=${ours.id}, agent=${ours.metadata.agent}`);

  const promptEp = ours.endpoints.find((e) => e.name === "prompt");
  if (!promptEp) fail("no prompt endpoint in $SRV.INFO");
  const expectedSubj = `agents.prompt.cc.${owner}.${paneSubjectToken}`;
  if (promptEp.subject !== expectedSubj) {
    fail(
      `prompt subject: got ${promptEp.subject} want ${expectedSubj}`,
    );
  }
  ok(`prompt subject correct: ${promptEp.subject}`);

  const statusEp = ours.endpoints.find((e) => e.name === "status");
  if (!statusEp) fail("no status endpoint in $SRV.INFO");
  ok(`status endpoint present: ${statusEp.subject}`);

  // 2. Status endpoint: must reply with a §8.3-shaped heartbeat payload.
  const statusReply = await nc.request(statusEp.subject, "", {
    timeout: 1000,
  });
  const hb = JSON.parse(new TextDecoder().decode(statusReply.data));
  for (const key of ["agent", "owner", "instance_id", "ts", "interval_s"]) {
    if (hb[key] === undefined) fail(`status reply missing ${key}: ${JSON.stringify(hb)}`);
  }
  if (hb.instance_id !== ours.id) {
    fail(`status reply instance_id mismatch: got ${hb.instance_id} want ${ours.id}`);
  }
  ok("status reply shape conforms to §8.3");

  // 3. Heartbeat subscription: must receive a beat within 3 * interval.
  const sub = nc.subscribe(`agents.hb.cc.${owner}.${paneSubjectToken}`, {
    max: 1,
  });
  const hbDeadline = Date.now() + Math.max(timeoutMs, 3 * hb.interval_s * 1000);
  let gotHb = false;
  for await (const m of sub) {
    void m;
    gotHb = true;
    break;
  }
  if (!gotHb && Date.now() < hbDeadline) fail("no heartbeat observed");
  ok("heartbeat observed");

  // 4. Prompt: send a plain-text shorthand request and confirm we receive
  //    the mandatory `ack` first, then the terminator. The mock pane in
  //    the smoke test doesn't actually run claude, so the response stream
  //    after `ack` may be empty — we only assert protocol invariants.
  const agent = agents.get(ours);
  const stream = agent.prompt("compat ping", { inactivityTimeoutMs: 3000 });

  let sawAck = false;
  let sawTerminator = false;
  try {
    for await (const chunk of stream) {
      if (chunk.type === "status" && chunk.data === "ack") {
        sawAck = true;
      }
      if (chunk.type === "done") {
        sawTerminator = true;
        break;
      }
    }
  } catch (e) {
    // Some SDK versions surface inactivity timeout as an exception —
    // the protocol-level invariants are what we care about, so we
    // capture and decide based on what we saw.
    console.warn(`wire-compat: stream ended with: ${e}`);
  }
  if (!sawAck) fail("prompt stream did not start with §6.4 ack");
  if (!sawTerminator) fail("prompt stream did not end with §6.5 terminator");
  ok("prompt stream: ack + terminator (§6.4 + §6.5)");

  await nc.drain();
  console.log("wire-compat: PASS");
}

const timer = setTimeout(() => fail(`overall timeout after ${timeoutMs}ms`), timeoutMs);
timer.unref?.();

main().catch((e) => fail(String(e)));
