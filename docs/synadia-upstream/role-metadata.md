# Proposal: Optional `metadata.role` field (§3.2)

**Target:** `synadia-ai/synadia-agent-sdk-docs` — amendment to §3.2 (Required service metadata)  
**Compatibility:** Backward-compatible. Existing registrations without `role` are unaffected.  
**Status:** Draft for upstream review

---

## Motivation

Heterogeneous fleets mix agents that do work, agents that watch work, and the
operator console driving them. Treating all three as interchangeable causes
two concrete problems:

1. **Discovery fans out to the wrong agents.** A caller asking "show me workers
   to dispatch a task to" today gets observers back in the result set. Callers
   must implement ad-hoc filtering outside the protocol.
2. **Messaging authority is ambiguous.** Without a declared role, there is no
   protocol-level signal that an agent should not receive task prompts.

The `worker / observer / operator` distinction was proven out in the orch
multi-pane fleet (`docs/adr/0002-observer-role-default-exclude.md`). An
amplification-loop incident (May 2026) demonstrated that treating observer
agents as plain workers creates event-equivalence bugs that surface as
oscillating feedback cycles. Formalizing the role in protocol metadata lets
any compliant implementation — not just orch — reason about authority without
out-of-band coordination.

---

## Proposed change to §3.2

### BEFORE

| Key                | Type   | Required             | Description |
|--------------------|--------|----------------------|-------------|
| `agent`            | string | Yes                  | Canonical harness identifier. |
| `owner`            | string | Yes                  | Operator / account. |
| `session`          | string | When session-aware   | Harness-specific session label. |
| `protocol_version` | string | Yes                  | Protocol version implemented. |

> Additional metadata keys MAY be included and MUST be preserved by tools that relay service info.

### AFTER

| Key                | Type   | Required             | Description |
|--------------------|--------|----------------------|-------------|
| `agent`            | string | Yes                  | Canonical harness identifier. |
| `owner`            | string | Yes                  | Operator / account. |
| `session`          | string | When session-aware   | Harness-specific session label. |
| `protocol_version` | string | Yes                  | Protocol version implemented. |
| `role`             | string | No                   | Agent role. When present, MUST be one of `worker`, `observer`, or `operator`. Absence is equivalent to `worker`. See §3.2.1. |

> Additional metadata keys MAY be included and MUST be preserved by tools that relay service info.

### New §3.2.1 — Role semantics

**`worker`** (default)  
The agent accepts task prompts and produces results. Discovery tools SHOULD
include workers by default. A worker that omits `role` is treated identically
to one that sets `role: "worker"`.

**`observer`**  
The agent monitors fleet activity but does not perform primary task work.
Examples: log-tail readers, supervisor dashboards, stasi-style spy panes.
Discovery tools SHOULD exclude observers from default result sets and SHOULD
expose an explicit flag (e.g. `--include-observers`) to surface them. Callers
SHOULD NOT route task-bearing prompts to observers; implementations MAY refuse
such deliveries.

**`operator`**  
The agent (or human console) exercises authority over the fleet — it spawns,
redirects, and terminates other agents. There is at most one operator per fleet
at any time, though the protocol does not enforce this limit normatively.
Callers that enumerate agents for task dispatch SHOULD skip operator
registrations.

The protocol provides no built-in mechanism for detecting duplicate operator
claims; implementations that need this enforcement SHOULD use an out-of-band
claim record (see orch's ADR-0003: `docs/adr/0003-operator-claim-record.md`
for a reference implementation that records the operator pane separately from
the worker registry and refuses second-claim attempts).

**Interaction rules:**
- `worker → observer` messaging is discouraged; implementations MAY reject it.
- `operator → any` messaging is unrestricted.
- `observer → operator` reporting is the canonical channel for observer output.

---

## Wire example (Appendix B.12 style)

### B.12-r Service info response with `role`

Returned by `$SRV.INFO.agents` for an observer pane:

```json
{
  "name": "agents",
  "id": "VMKS6MHK71PCPWGY38A7N5",
  "version": "0.3.0",
  "description": "Claude Code — audit-2026-05-16",
  "metadata": {
    "agent": "claude-code",
    "owner": "dmestas",
    "session": "audit-2026-05-16",
    "protocol_version": "0.3",
    "role": "observer"
  },
  "endpoints": [
    {
      "name": "prompt",
      "subject": "agents.prompt.claude-code.dmestas.audit-2026-05-16",
      "queue_group": "agents",
      "metadata": {
        "max_payload": "1MB",
        "attachments_ok": true
      }
    },
    {
      "name": "status",
      "subject": "agents.status.claude-code.dmestas.audit-2026-05-16",
      "queue_group": "agents"
    }
  ]
}
```

A discovery client filtering for workers would skip this registration because
`metadata.role == "observer"`. A stasis / audit harness would use the same
subject hierarchy and queue group — only the `role` field changes.

---

## Discovery filter guidance

Implementations of §4 discovery SHOULD document how they handle `role`:

- Default enumeration: return `worker` and (missing-role-treated-as-worker) only.
- `--include-observers` (or equivalent): add `observer` registrations.
- `--role=operator`: return the fleet operator registration.

Callers that do not understand `role` see a valid service info object and may
interact with the agent normally — backward compatibility is preserved because
`role` is optional and additive.

---

## References

- §3.2 Required service metadata (this spec)
- §4 Discovery
- Appendix B.12 (service info wire example)
- orch ADR-0002: `docs/adr/0002-observer-role-default-exclude.md`
- orch ADR-0003: `docs/adr/0003-operator-claim-record.md`
