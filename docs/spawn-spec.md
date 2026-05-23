# SpawnSpec & WorkerHandle — schema reference

> See [spawn-spec-versioning.md](./spawn-spec-versioning.md) for the wire
> stability contract and what triggers a v2 bump.

The typed contract between `orch-spawn` (the dispatcher) and per-executor
backends. Shape borrowed from [Archon](https://github.com/Archon-research/archon)
(flat top-level, one-of executor discriminator). Rationale, alternatives,
and decision history live in
[docs/proposals/0002-typed-executor-contract.md](./proposals/0002-typed-executor-contract.md).
The wire-level protocol (stdin/stdout/exit codes, validation contract)
is in [docs/executor-protocol.md](./executor-protocol.md).

| | |
|---|---|
| Canonical types | `internal/spawnspec/` Go structs |
| Wire format | YAML |
| Published JSON Schema | `dist/schema/spawn-spec.v1.json`, `dist/schema/worker-handle.v1.json` |
| Schema regeneration | `go run ./cmd/spawnspec-schema -out dist/schema` |
| Current version | `v1` |

## Why two schemas

- **SpawnSpec** — dispatcher → backend. Declarative: "spawn this agent
  with this identity in this executor."
- **WorkerHandle** — backend → dispatcher. Records what was actually
  spawned (executor id, bus subjects, abort verb, lifecycle status).

Backends consume one, emit the other. Round-trip through YAML on the
wire.

## SpawnSpec

```yaml
spec_version: v1                 # optional; defaults to v1
name: lead-engineer              # required; DNS-label shape
description: "Backend engineer"  # optional

agent: claude-code               # required; enum
session: lead-engineer           # optional; SESH_SESSION
cwd: /path/to/project            # optional; dispatcher resolves a default

owner: dmestas                   # optional; subject token
labels:                          # optional; surfaced in $SRV.INFO
  role: engineer

outfit:                          # optional; suit bundle
  bundle: backend/executing+pr-policy
  # OR explicit:
  # name: backend
  # cut: executing
  # accessories: [pr-policy]

env:                             # optional; keys MUST match [A-Z_][A-Z0-9_]*
  NATS_URL: nats://127.0.0.1:4222

# Executor discriminator — exactly one of the following blocks
tmux:
  headless: false
  verify: false
  layout: default                # one of: default | grid | full
  position: right                # one of: right | left | above | below
  role: worker                   # one of: worker | observer
  no_shim: false

# cf-worker:
#   script: /path/to/worker.js
#   wrangler_env: production
#   abort_endpoint: /control/abort

# cf-durable-object:
#   do_namespace: ORCH_WORKERS
#   do_id: lead-engineer
```

### Field reference

| Field | Type | Required | Notes |
|---|---|---|---|
| `spec_version` | string | no | Wire version. Empty → `v1`. Other → parse error. |
| `name` | string | yes | DNS-label shape (`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`), ≤ 63 chars. |
| `description` | string | no | Free-form operator notes. |
| `agent` | enum | yes | One of: `claude-code`, `codex`, `pi`, `gemini`, `echo`. |
| `session` | string | no | sesh session label; maps to `SESH_SESSION`. |
| `cwd` | string | no | Working directory the worker lands in. |
| `owner` | string | no | Operator handle; used in subject tokens. |
| `labels` | map[string]string | no | Arbitrary key/value metadata. |
| `outfit` | OutfitBlock | no | Either `bundle` shorthand OR `name`+`cut`+`accessories`. Not both. |
| `env` | map[string]string | no | Keys MUST match `^[A-Z_][A-Z0-9_]*$`. |
| `tmux` / `cf-worker` / `cf-durable-object` | block | exactly one | Executor discriminator. |

### Executor blocks

#### `tmux:`

| Field | Type | Notes |
|---|---|---|
| `headless` | bool | Detach into `orch-headless` session. |
| `verify` | bool | Poll for agent-ready signal before declaring success. |
| `layout` | string | One of: `default`, `grid`, `full`. |
| `position` | string | One of: `right`, `left`, `above`, `below`. |
| `role` | string | One of: `worker`, `observer`. |
| `no_shim` | bool | Disable `orch-agent-shim` sidecar. |

#### `cf-worker:`

| Field | Type | Required | Notes |
|---|---|---|---|
| `script` | string | yes | Path to entrypoint, relative to wrangler root. |
| `wrangler_env` | string | no | Wrangler environment (e.g. `production`). |
| `abort_endpoint` | string | no | Worker route for graceful shutdown. |

#### `cf-durable-object:`

| Field | Type | Required | Notes |
|---|---|---|---|
| `do_namespace` | string | yes | Wrangler binding name. |
| `do_id` | string | yes | Stable DO id-from-name. |

## WorkerHandle

```yaml
spec_version: v1
name: lead-engineer
agent: claude-code
session: lead-engineer
created_at: 2026-05-18T13:00:00Z

executor: tmux                   # one of: tmux | cf-worker | cf-durable-object
pane_id: "%64"                   # for executor=tmux
# id: <opaque>                   # for non-tmux executors

bus:
  prompt: agents.prompt.cc.dmestas.lead-engineer
  status: agents.status.cc.dmestas.lead-engineer
  hb:     agents.hb.cc.dmestas.lead-engineer
  signal: orch.signal.>.cc.dmestas.lead-engineer

abort:
  kind: tmux-send-keys           # one of: tmux-send-keys | http-post | do-call
  target: "%64"
  keys: "C-c"

log_file: /Users/dmestas/.cache/orch-shim/pct64.log
pid: 12345

status: ready                    # one of: pending | ready | failed
message: ""                      # populated when status=failed or pending
```

### Cross-field rules

- `executor: tmux` requires `pane_id` non-empty.
- `status: failed` requires `message` non-empty.
- `status: pending` is the async-provisioning signal (CF Worker takes ~30s
  to deploy); operator polls until `ready` or `failed`.

## Validation flow

Two stages, both implemented in `internal/spawnspec/`:

1. **Parse** — `UnmarshalSpec(data) → (*SpawnSpec, error)`.
   - Rejects unknown fields (`yaml.KnownFields(true)`).
   - Rejects unknown `spec_version`.
   - Defaults missing `spec_version` to `v1`.

2. **Validate** — `ValidateSpec(*SpawnSpec) → error`.
   - Struct-tag rules (`required`, `oneof`, `dns_label`, `agent`).
   - Executor-discriminator XOR (exactly one block).
   - Outfit-shape XOR (bundle OR explicit, not both).
   - Env-var key shape (`^[A-Z_][A-Z0-9_]*$`).

Callers MAY compose stages independently (e.g. `orch-spawn --validate-spec`
distinguishes parse errors from validation errors so the operator can see
which stage tripped).

## Adding a new executor

1. Define a `<NewExecutor>Block` struct in `types.go` with field-level
   `validate:` tags.
2. Add a pointer field to `SpawnSpec` with `yaml:"<new-executor>,omitempty"`.
3. Extend `spawnSpecStructLevel` in `validate.go` to count the new block
   in the XOR check.
4. Extend the `WorkerHandle.Executor` enum (`oneof=`) and update the
   table above.
5. Regenerate the JSON Schema (`go run ./cmd/spawnspec-schema -out dist/schema`).
6. Add tests in `types_test.go` and `contract_test.go`.

## Adding a new agent

The `agent:` enum is closed by design — see Ousterhout: define errors
out of existence. Adding `myharness`:

1. Add `AgentMyharness Agent = "myharness"` to `types.go`.
2. Add it to `KnownAgents()`.
3. Register the corresponding adapter in `internal/adapter/myharness/`
   and wire it into the shim (`internal/shim/...`).
4. Regenerate the schema.

The contract test `TestContract_AllKnownAgentsAccepted` catches case-1
mistakes (enum without registration is still a parse-time accept, but
the adapter wiring will fail at shim startup).
