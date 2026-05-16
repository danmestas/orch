# Proposal: Placement metadata — new Appendix E (informative)

**Target:** `synadia-ai/synadia-agent-sdk-docs` — new informative appendix after Appendix D  
**Compatibility:** Fully additive. No normative changes.  
**Status:** Draft for upstream review

---

## Motivation

Fleet operators of more than a handful of agents face a practical navigation
problem: "is this pane running in my tmux, in a Docker container, in a remote
SSH host, or in a Cloudflare Worker?" The protocol's subject hierarchy
identifies an agent by owner and session name (§2), but gives no signal about
the execution substrate.

This matters for:

- **Targeted restarts.** Restarting a Docker worker uses `docker restart`;
  restarting a CF Worker uses `wrangler deploy`.
- **Capability inference.** Native executors (`tmux`, `docker`, `ssh`) can run
  arbitrary shell tools. WASM executors (`cf-worker`, `browser`, `wasmtime`)
  are limited to text I/O and HTTP fetch.
- **Audit and reproducibility.** A task completion record that includes
  executor type and host is easier to attribute and reproduce.

The orch multi-executor proposal (`docs/multi-executor-workers.md`) catalogues
seven executor types and introduced the operational need for this metadata.
This appendix documents the pattern without adding normative requirements —
the protocol does not constrain where an agent runs.

---

## Appendix E: Placement metadata (informative)

This appendix describes optional `metadata` fields agents MAY set to declare
their execution placement. No normative MUST or SHOULD applies to whether an
agent sets these fields; the purpose is to document a consistent vocabulary so
fleet tools from different vendors can interpret placement data without
coordination.

### E.1 Fields

| Key               | Type   | Description |
|-------------------|--------|-------------|
| `executor`        | string | The execution substrate. See §E.2 for recommended values. The field is open: any string is valid; the listed values are conventional. |
| `host`            | string | Where the agent runs. Interpretation depends on `executor`. See §E.3. |

Both fields are optional. Either may be set independently.

### E.2 Recommended `executor` values

The values below are conventional identifiers. Implementations SHOULD use
these strings when the executor matches, and MAY use other strings for
executors not listed here. The list is open and expected to grow as new
execution environments emerge.

| Value              | Flavor | Description |
|--------------------|--------|-------------|
| `tmux`             | native | Agent runs in a tmux pane on the operator's local machine. Full shell capability. |
| `docker`           | native | Agent runs inside a Docker container, typically with host networking and volume mounts. Full shell capability inside the container. |
| `ssh`              | native | Agent runs on a remote host reached via SSH. Full shell capability on the remote. |
| `wasm`             | WASM   | Generic WASM executor not otherwise categorized. Text I/O only; no `process_spawn`. |
| `cf-worker`        | WASM   | Agent runs as a Cloudflare Worker. Scale-to-zero; no shell exec; HTTP/fetch only. |
| `cf-durable-object` | WASM  | Agent runs as a Cloudflare Durable Object. Stateful; same constraints as `cf-worker`. |
| `wasmtime`         | WASM   | Agent runs under `wasmtime` locally for sandboxed pure-reasoning work. |
| `browser`          | WASM   | Agent runs as WASM in a browser tab. Lifetime tied to the tab. |

Implementations encountering an unrecognized `executor` value MUST treat it as
an opaque string and MUST NOT reject the service registration.

**Capability inference (informative):**  
Native executors (`tmux`, `docker`, `ssh`) support arbitrary shell tool use.
WASM executors (`wasm`, `cf-worker`, `cf-durable-object`, `wasmtime`, `browser`) are
limited to text I/O, HTTP fetch, and WASI-permitted filesystem operations.
Fleet orchestrators that dispatch tool-heavy tasks SHOULD prefer native
executors unless the task is purely text or HTTP based.

### E.3 `host` conventions

The `host` field records where the agent runs. Recommended formats by executor:

| `executor`   | Recommended `host` format | Example |
|--------------|---------------------------|---------|
| `tmux`       | Hostname or `localhost`   | `studio.local` |
| `docker`     | Container ID or name      | `orch-builder-7f3a2c` |
| `ssh`        | `user@hostname`           | `runner@build-01.internal` |
| `cf-worker`  | Worker script name        | `orch-planner-prod` |
| `cf-durable-object` | DO class + ID      | `OrchestratorDO/session-abc123` |
| `wasmtime`   | Hostname or `localhost`   | `sandbox.local` |
| `browser`    | Origin of the hosting page | `https://app.example.com` |

When `host` is omitted, the agent's location is unknown or not meaningful in
context (e.g. a stateless CF Worker where the instance identity is ephemeral).

### E.4 Relationship to other metadata

The metadata fields compose along orthogonal axes:

| Field      | Describes        | Defined in              |
|------------|------------------|-------------------------|
| `agent`    | the *harness* (what runtime executes) | §3.2 |
| `outfit`   | the *configuration* (system prompt, tools, model) | §3.2 (proposed) |
| `role`     | the *authority* (worker / observer / operator) | §3.2 (proposed) |
| `executor` | the *substrate* (where it runs) | this appendix |
| `host`     | the *placement* (which instance of the substrate) | this appendix |

A complete fleet picture for a remote SSH worker running Claude Code with an
engineer outfit acting as a worker uses all five fields:

```json
{
  "agent": "claude-code",
  "outfit": "engineer:focused",
  "role": "worker",
  "executor": "ssh",
  "host": "runner@build-01.internal"
}
```

`outfit` and `role` change frequently within a deployment; `executor` and
`host` change rarely (typically only when the agent is restarted on a
different substrate).

---

## Wire example (Appendix B.12 style)

### B.12-p Service info response with placement metadata

Returned by `$SRV.INFO.agents` for a CF Worker agent:

```json
{
  "name": "agents",
  "id": "T7HX2QKRP9NMWYJE04CSLA",
  "version": "0.3.0",
  "description": "Planner agent — orch-planner-prod",
  "metadata": {
    "agent": "claude-code",
    "owner": "dmestas",
    "session": "default",
    "protocol_version": "0.3",
    "role": "worker",
    "executor": "cf-worker",
    "host": "orch-planner-prod"
  },
  "endpoints": [
    {
      "name": "prompt",
      "subject": "agents.prompt.claude-code.dmestas.default",
      "queue_group": "agents",
      "metadata": {
        "max_payload": "256KB",
        "attachments_ok": false
      }
    },
    {
      "name": "status",
      "subject": "agents.status.claude-code.dmestas.default",
      "queue_group": "agents"
    }
  ]
}
```

An operator enumerating "all cloud workers" would filter `metadata.executor`
for `cf-worker` or `cf-durable-object`. An operator enumerating "all local panes" would
filter for `tmux`. Tasks requiring shell tools would be routed to `tmux`,
`docker`, or `ssh` registrations.

---

## References

- §3.2 Required service metadata (field context)
- Appendix B.12 (service info wire example)
- Appendix C (known agent identifiers)
- orch multi-executor proposal: `docs/multi-executor-workers.md` (executor catalog)
