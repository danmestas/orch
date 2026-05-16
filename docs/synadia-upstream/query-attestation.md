# Proposal: External query-chunk attestation — new §7.4

**Target:** `synadia-ai/synadia-agent-sdk-docs` — new normative subsection §7.4 under §7 (Mid-stream queries)  
**Compatibility:** Backward-compatible. Consumers that ignore unknown `data` fields see a normal query chunk.  
**Status:** Draft for upstream review

---

## Motivation

§7 defines query chunks as agent-initiated: the agent publishes the chunk and
waits for a reply on `reply_subject`. This works cleanly when the harness has a
native "agent waiting for input" event — for example, `claude-code`'s
`Notification` hook fires when the CLI is paused at a tool-use confirmation
prompt, giving an orch supervisor an exact moment to synthesize a query chunk.

However, not all harnesses expose such an event:

| Harness       | Native attention event   | Result                                       |
|---------------|--------------------------|----------------------------------------------|
| `claude-code` | `Notification` hook      | Can publish query chunks natively            |
| `gemini`      | `Notification` event     | Can publish query chunks natively — no attestation needed |
| `codex`       | None                     | Notification gap — needs external attestation |
| `pi`          | None                     | Notification gap — needs external attestation |

For harnesses with no native attention event, the only reliable signal today is
`Stop` (turn ended). But by then the agent has already chosen a path — there is
no interactive query opportunity.

The practical workaround, deployed in orch's NATS bridge, is for an external
observer (a shim, a sidecar, or an orch supervisor) to publish a synthetic
`query` chunk on the agent's behalf — inserting an interactive pause into the
stream without harness modification. This pattern is undocumented in the
current spec, leaving implementors to rediscover it and callers unable to
distinguish native vs. synthetic queries.

Formalizing the pattern gives:
- A standard field (`attested_by`) so callers can apply appropriate trust policy.
- Clear guidance for shim authors on the correct wire shape.
- A hook point for future access-control extensions.

---

## Proposed new §7.4

### 7.4 External query attestation

A query chunk is normally published by the agent itself. When a harness does
not expose a native "waiting for input" event, an authorized external observer
(hereafter "the attesting observer") MAY publish a synthetic query chunk on the
agent's behalf.

The attesting observer MUST publish to the same reply subject used for the
ongoing response stream and MUST use exactly the query chunk wire shape defined
in §7.1, with one additional field:

| Field          | Type   | Required | Description                                                                                                           |
|----------------|--------|----------|-----------------------------------------------------------------------------------------------------------------------|
| `attested_by`  | string | No       | `instance_id` of the observer publishing this chunk on the agent's behalf. Presence signals external attestation. Absence signals the chunk is agent-native. |

The `instance_id` in `attested_by` MUST be the 5th subject token of the
attesting observer's own service registration (§2), which is its stable
per-registration identity.

#### Trust semantics

An attested query chunk carries authority derived from the attesting observer,
not from the agent. Callers SHOULD apply the following policy:

1. **Authorization:** The attesting observer SHOULD be authorized by the
   agent's owner (the `owner` field in §3.2). Implementations MAY enforce this
   via NATS authorization policies scoped to the owner's namespace.
2. **Transparency:** Callers SHOULD surface `attested_by` in their UI or logs
   so the human operator knows the query originated from a supervisor, not from
   the agent's own reasoning.
3. **Graceful degradation:** Callers that do not understand `attested_by` see a
   normal query chunk and respond to it normally. This is the correct behavior —
   the attesting observer is acting on the agent's behalf, and the reply will be
   forwarded to the agent.

#### Lifecycle (same as §7.3)

The attesting observer is responsible for:
- Choosing a fresh `reply_subject` (typically `_INBOX.xxx`).
- Setting a reply timeout consistent with the interactive context.
- Forwarding the caller's reply to the agent via the harness's native input
  channel (e.g. stdin, FIFO, or the harness API).

The attesting observer MUST NOT publish more than one attested query chunk per
agent turn unless the harness semantics guarantee sequential consumption.

#### When to use this pattern

Use external attestation only when the harness has no native query mechanism.
Harnesses that support native queries (e.g. `claude-code`) SHOULD publish
query chunks directly. External attestation is a compatibility bridge, not a
replacement for native integration.

---

## Wire example (Appendix B.7 style)

### B.7-a Attested query chunk (codex shim)

Setup: a caller has already sent a prompt to a codex agent and is reading the
response stream on inbox `_INBOX.Kx9mNqP2A` (the reply-to subject of the
original `nats request`). The attesting observer is itself a registered agent
whose subject is `agents.prompt.claude-code.dmestas.orch-supervisor-01` — so
its 5th subject token (its `instance_id`) is `orch-supervisor-01`.

When the shim detects that codex is awaiting input, it publishes this chunk
**to the response stream's reply subject** (`_INBOX.Kx9mNqP2A`) — the same
subject the codex agent itself uses for `response` chunks — not to the codex
agent's prompt endpoint:

```json
{
  "type": "query",
  "data": {
    "id": "f2c94b7a-1e58-4d3a-bbbb-998877665544",
    "reply_subject": "_INBOX.Pq3m7Rv1X",
    "prompt": "codex is paused awaiting confirmation. Proceed with file deletion? (yes/no)",
    "attested_by": "orch-supervisor-01"
  }
}
```

The caller replies to the query's `reply_subject` (`_INBOX.Pq3m7Rv1X`):

```
yes
```

The shim receives the reply and forwards `yes` to the codex agent's stdin
FIFO. The codex agent proceeds and publishes its next `response` chunk as
normal. The stream consumer sees no structural difference from a native
`claude-code` query/reply cycle, except for the presence of `attested_by`.

---

## Full exchange annotated

```
# Setup: caller previously sent a prompt to codex with reply-to _INBOX.Kx9mNqP2A:
#   PUBLISH agents.prompt.codex.dmestas.codex-session-7 (reply: _INBOX.Kx9mNqP2A)
#   {"prompt":"delete stale build artifacts"}
# Caller is now subscribed to _INBOX.Kx9mNqP2A reading response chunks.

# 1. Shim detects codex is paused (e.g. via Stop event or heuristic)
# 2. Shim publishes attested query DIRECTLY ONTO THE RESPONSE STREAM
#    (the in-flight reply-to inbox), masquerading as a chunk from codex.

PUBLISH _INBOX.Kx9mNqP2A
{"type":"query","data":{"id":"f2c94b7a-1e58-4d3a-bbbb-998877665544","reply_subject":"_INBOX.Pq3m7Rv1X","prompt":"codex is paused awaiting confirmation. Proceed with file deletion? (yes/no)","attested_by":"orch-supervisor-01"}}

# 3. Caller (orchestrator) reads chunk from _INBOX.Kx9mNqP2A, surfaces
#    prompt to human or policy engine. Sees attested_by — knows it
#    originated from the supervisor, not codex itself.
# 4. Caller publishes reply to the query's reply_subject

PUBLISH _INBOX.Pq3m7Rv1X
yes

# 5. Shim (subscribed to _INBOX.Pq3m7Rv1X) receives reply, writes "yes\n"
#    to codex stdin FIFO
# 6. Codex continues; publishes next response chunk on the original stream
PUBLISH _INBOX.Kx9mNqP2A
{"type":"response","data":"Deleted 3 files."}
```

---

## References

- §7 Mid-stream queries (this spec)
- §7.1 Query chunk wire shape
- §7.3 Query lifecycle
- §3.2 Service metadata (instance_id via 5th subject token)
- orch NATS bridge: `docs/nats-bridge.md` (Notification gap for codex/pi)
