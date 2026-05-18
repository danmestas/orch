# Proposal 0002 — Typed Executor Contract (YAML SpawnSpec / WorkerHandle)

**Status:** draft (design phase active; user wants to design + implement fully as a prerequisite to expanding execution targets)
**Depends on:** none (in-repo deepening)
**Blocks:** Proposal 0003 (executor backend extraction depends on this contract)

## Why

Today `orch-spawn` shells out to per-executor `spawn.sh` scripts and parses the pane id off the last line of stdout. The contract is implicit:

- "stdout's last `%`-prefix line is the pane id" (or "the worker URL" for wasm/cf-worker)
- "stderr is for warnings; failures exit non-zero with a free-form message"
- "env vars carry the agent spec (AGENT, CWD, OUTFIT, BUNDLE, NO_FLEET, ...)"

This is **shallow IPC**. Symptoms hit in recent topology work:

- `tail -1` on orch-spawn stdout breaks when warnings interleave (workaround: `grep -E '^%[0-9]+'`)
- New executor types (cf-durable-object, future browser-tab, devcontainer) each invent their own "what to return on stdout" — no shared shape
- The pane lifecycle hooks (abort, status, cleanup) have no standard expression — each executor reinvents them
- No machine-readable validation: "did the executor honour my outfit bundle?" can only be inferred from side effects

Deepening: promote from bash IPC to a typed YAML contract. Two doc shapes — `SpawnSpec` (input) and `WorkerHandle` (output) — versioned with `apiVersion`, validated at the dispatcher.

> **Open: which YAML project does Dan want to model this after?** He referenced "a project that does this with yaml." Once named, this proposal aligns to its conventions (apiVersion, kind, spec/status separation, validation, etc.). The shape below is a starting sketch I'd reach for in absence of that reference — Kubernetes-flavoured. Awaiting Dan to name the project so the final shape matches.

## Goals

1. SpawnSpec YAML — operator's contract with the dispatcher describing what to spawn
2. WorkerHandle YAML — executor's contract with the dispatcher describing what got spawned
3. Single dispatcher (`orch-spawn`) routes by `executor:` field to the right backend
4. Each backend is a process that reads SpawnSpec on stdin and writes WorkerHandle on stdout
5. Versioned: `apiVersion:` enforces forward/backward-compat conversation

## Non-goals

- Replacing the operator-facing `orch-spawn <agent> [flags]` CLI surface (operators keep the flag UX; orch-spawn builds the SpawnSpec internally)
- Changing the Synadia wire — shim subjects, envelope headers, chunk shapes all preserved
- Locking in a specific YAML library or schema validator (those are implementation details)

## Archon comparison

Reviewed [coleam00/Archon](https://github.com/coleam00/Archon) per Dan's reference. Findings:

- **Layer mismatch**: Archon's "executor" is a *workflow node executor* — a DAG runner for `prompt:` / `bash:` / `script:` / `loop:` / `command:` nodes. Orch's spawn-spec is a *process provisioner* — pane lifecycle, not behaviour sequencing. They're complementary, not interchangeable.
- **Shape borrow**: Archon's YAML is much cleaner than the Kubernetes-flavoured ceremony I sketched first. Flat top-level (`name`, `description`, `provider`, `model`, `nodes:`); no `apiVersion`/`kind`/`metadata`/`spec` wrapping. One-of discriminator at the node level (`prompt:` XOR `bash:` XOR `script:` ...). Cross-step refs via `$nodeId.output[.json.path]`. DAG via `depends_on` + `when:` + `trigger_rule:`. Per-node `timeout` / `idle_timeout`.
- **Future hook**: orch may eventually want an Archon-style workflow layer ON TOP of spawn-spec — DAGs of spawn → prompt → test → merge. That's a separate future proposal; out of scope for 0002. orch's `bin/orch-goal-pursue` is the current primitive workflow; an Archon-style layer would generalize it.

The shape below borrows Archon's flat YAML + discriminator pattern for the EXECUTOR field. Each executor type gets its own discriminator block instead of an `options:` bag — same idea as Archon's `bash:` XOR `prompt:` XOR `script:`.

Real Archon workflow YAML for reference (from `.archon/workflows/test-workflows/e2e-pi-all-nodes-smoke.yaml`):

```yaml
name: e2e-pi-all-nodes-smoke
description: 'Pi provider smoke across every CI-compatible node type.'
provider: pi
model: anthropic/claude-haiku-4-5

nodes:
  - id: prompt-node
    prompt: "Reply with exactly the single word 'ok' and nothing else."
    allowed_tools: []
    effort: low
    idle_timeout: 30000

  - id: bash-json-node
    bash: 'echo ''{"status":"ok"}'''

  - id: script-bun-node
    script: echo-args
    runtime: bun
    timeout: 30000

  - id: gated
    bash: "echo 'gated-ok'"
    depends_on: [bash-json-node]
    when: "$bash-json-node.output.status == 'ok'"
```

## Public interface — SpawnSpec (Archon-shaped)

```yaml
name: lead-engineer
description: "Backend engineer for shim PRs"

# Identity
agent: claude-code                # which harness CLI to launch
session: lead-engineer            # 5th subject token; mapped to SESH_SESSION
cwd: /Users/dmestas/projects/orch

# Optional cross-cutting metadata (analogous to Archon's `provider` + `model`)
owner: dmestas
labels:
  role: engineer
  tier: lead

# Optional outfit bundle (suit prepare output)
outfit:
  bundle: backend/executing+pr-policy
  # OR explicit:
  # name: backend
  # cut: executing
  # accessories: [pr-policy]

# Environment vars passed to the worker process
env:
  NATS_URL: nats://127.0.0.1:58413
  ORCH_OWNER: dmestas

# ─── Discriminator: exactly one executor block ──────────────────────────
# Same pattern as Archon's `bash:` XOR `prompt:` XOR `script:` at node level.
# Each backend documents its own keys; dispatcher routes by which key is present.

tmux:
  headless: true
  verify: false                   # banner-string verification
  layout: default                 # one of: default | grid | full

# OR:
# cf-worker:
#   script: /path/to/worker.js
#   wrangler_env: production
#   abort_endpoint: /control/abort

# OR:
# cf-durable-object:
#   do_namespace: ORCH_WORKERS
#   do_id: lead-engineer

# OR (future):
# devcontainer:
#   image: orch-claude-base:latest
#   workspace: /workspaces/orch
```

**Rule**: exactly one executor discriminator block. Validator rejects zero or two-or-more.

## Public interface — WorkerHandle (Archon-shaped)

```yaml
name: lead-engineer              # echoes SpawnSpec.name
agent: claude-code
session: lead-engineer
created_at: 2026-05-18T13:00:00Z

# Executor records WHICH block it spawned
executor: tmux
pane_id: "%64"                   # tmux-specific; alias `id:` for generic consumers

# Bus addressing
bus:
  prompt:   agents.prompt.cc.dmestas.lead-engineer
  status:   agents.status.cc.dmestas.lead-engineer
  hb:       agents.hb.cc.dmestas.lead-engineer
  signal:   orch.signal.>.cc.dmestas.lead-engineer

# Imperative cancellation (analogous to Archon's per-node abort semantics)
abort:
  kind: tmux-send-keys
  target: "%64"
  keys: "C-c"

# Diagnostics
log_file: /Users/dmestas/.cache/orch-shim/pct64.log
pid: 12345

# Lifecycle phase
status: ready                    # one of: pending | ready | failed
message: ""                      # populated on failure
```

## Cross-step variable substitution (Archon-borrowed, optional)

Archon supports `$nodeId.output` and `$nodeId.output.json.path` for downstream refs. Orch's spawn-spec is single-shot today, but if an operator chains spawns (e.g., spawn a verifier whose env depends on an engineer's pane id), substitution helps:

```yaml
# Hypothetical multi-spawn workflow (out of scope for 0002 but designed-in)
spawns:
  - name: engineer
    agent: claude-code
    tmux: { headless: true }

  - name: verifier
    depends_on: [engineer]
    agent: claude-code
    env:
      VERIFY_TARGET_PANE: "$engineer.pane_id"   # ← Archon-style substitution
    tmux: { headless: true }
```

For v1, orch-spawn handles single spawns (no `spawns:` list, no `depends_on`). The substitution shape is reserved for a future multi-spawn extension.

## Dispatcher contract

```
$ cat spawn-spec.yaml | orch-spawn --from-spec
... writes WorkerHandle yaml to stdout, exits 0 on success ...

$ orch-spawn claude --headless --outfit backend  # operator CLI unchanged
... internally builds a SpawnSpec, dispatches, parses the WorkerHandle,
    echoes the .spec.id (pane id) to stdout for backwards compat ...
```

Validation:
- `apiVersion` is checked; unknown versions exit 2 with "unsupported apiVersion"
- `kind` is checked; unknown kind exits 2
- `spec.executor` is looked up against registered backends; unknown exits 2 with "unknown executor: <name>"
- `spec.agent` is checked against the resolved executor's accepted agents
- Backend-specific `options` are passed through; backend validates them

## Per-executor backend interface

Each backend is a process invoked by the dispatcher with the SpawnSpec on stdin. Lifecycle:

```
$ <backend-binary>
  stdin:  SpawnSpec YAML
  stdout: WorkerHandle YAML on success; nothing on failure
  stderr: human-readable diagnostics (always)
  exit:   0 on success; non-zero on failure with status.message populated in stderr's last line
```

Backends are discovered by name from `$ORCH_EXECUTORS_DIR` (default `~/.local/share/orch/executors/<name>`) OR PATH (`orch-executor-<name>`). The dispatcher picks the first match.

## Migration plan

### Step 1: Define schemas + validator (in-repo, no behaviour change)

1. Add `internal/spawnspec/` Go package with:
   - `SpawnSpec`, `WorkerHandle` types
   - YAML marshal/unmarshal
   - apiVersion gate
   - validation helpers
2. Unit tests cover round-trip, version mismatch, missing required fields
3. No changes to orch-spawn yet

### Step 2: orch-spawn builds SpawnSpec internally

1. orch-spawn's flag parsing produces a SpawnSpec struct
2. Dispatcher logic: build SpawnSpec → serialise → pipe to `executors/<name>/spawn.sh`
3. spawn.sh scripts updated to read SpawnSpec from stdin, write WorkerHandle to stdout
4. orch-spawn parses the WorkerHandle, echoes `.spec.id` for backwards-compat
5. Existing operator CLI behavior preserved

### Step 3: Validation harness

1. `orch-spawn --validate-spec <file>` — read a SpawnSpec from disk, validate, exit 0/non-0
2. Useful for CI tools, the UI's "spawn this" form, future GitOps tooling
3. Doc the schema in `docs/spawn-spec.md`

### Step 4: Documentation

1. `docs/spawn-spec.md` — full schema reference
2. `docs/executor-protocol.md` — backend interface contract (stdin/stdout/exit code expectations)
3. README updates in `executors/<name>/` documenting their accepted `options:` keys

## What changes for operators

- `orch-spawn claude --headless` works identically
- New: `cat spec.yaml | orch-spawn --from-spec` for declarative spawns
- New: `orch-spawn --validate-spec spec.yaml` for pre-flight checks
- UI / sister tools can build SpawnSpec YAML and pipe it to orch-spawn

## What changes for executor authors

- spawn.sh now reads from stdin instead of env vars (env still supported as fallback for v1)
- spawn.sh now writes structured YAML to stdout instead of "last line is pane id"
- All executor-specific options live under `spec.options` (versioned by apiVersion)

## Backwards compatibility

- v1 of the contract supports BOTH: SpawnSpec stdin AND legacy env-var input
- v2 (future) drops env-var fallback
- Operator CLI never breaks — flag-to-spec mapping happens inside orch-spawn

## Acceptance criteria

- [ ] `internal/spawnspec/` package with SpawnSpec + WorkerHandle types + YAML codec + validator
- [ ] Unit tests: round-trip, version mismatch, required-field violations
- [ ] orch-spawn builds SpawnSpec internally for all current flag combinations
- [ ] tmux executor reads SpawnSpec from stdin, writes WorkerHandle to stdout
- [ ] cf-worker executor same
- [ ] cf-durable-object executor same
- [ ] Bench (docker-sesh) passes with the new dispatch path
- [ ] `orch-spawn --validate-spec spec.yaml` works
- [ ] `docs/spawn-spec.md` + `docs/executor-protocol.md` published

## Decisions deferred to design phase

**Resolved by Archon review:**

- ~~YAML reference project~~ → Archon. Shape borrowed: flat top-level, no `apiVersion`/`kind` ceremony, one-of discriminator pattern, `$nodeId.output` substitution reserved for future multi-spawn extension.
- ~~Top-level shape~~ → flat (`name`, `description`, identity fields, optional sections, executor discriminator block at root).

**Still open:**

1. ~~**Schema validation library**~~ → **Industry standard: Go structs (canonical) + JSON Schema (published)** (Dan: 2026-05-18). Pattern adopted from Kubernetes, Argo Workflows, Tekton: Go structs with `yaml:` tags are the source of truth; JSON Schema is generated from them and published alongside the binary for cross-language consumers (TS/UI/Python). YAML parsing via `gopkg.in/yaml.v3`. Validation: struct-tag-driven (`go-playground/validator`) for field-level + custom validators for the executor-discriminator XOR rule. JSON Schema generation via `invopop/jsonschema` or similar. Published location: `dist/schema/spawn-spec.v1.json` + `dist/schema/worker-handle.v1.json` per release.
2. **WorkerHandle persistence**: should `~/.cache/orch-spawn/<name>.handle.yaml` be the canonical worker registry, supplanting today's scattered state? Lean: yes; ties into Proposal 0005.
3. **Executor name discoverability**: today's discriminator (`tmux:` / `cf-worker:` / `cf-durable-object:`) is hardcoded in the dispatcher. Should new executors register via plugin metadata, or stay enum-locked? Lean: enum-locked for v1, plugin in v2 (Proposal 0003's territory).
4. **Async / streaming for long-spawning executors** (e.g. CF Worker provisioning takes 30s): dispatcher returns WorkerHandle with `status: pending`, then operator polls `orch-spawn --status <handle>` until `ready` or `failed`. Same shape as Archon's loop+until pattern for AI nodes — recognizable convention.
5. **`outfit:` shape**: `bundle: backend/executing+pr-policy` shorthand vs explicit `name/cut/accessories`? Lean: support both — operator-friendly shorthand AND explicit form. Validator accepts either.
6. **Multi-spawn workflows**: when (not if) to add `spawns:` list + `depends_on` + variable substitution. Lean: defer to a separate proposal (0006?) once single-spawn shape is stable in production.

## Risks

- **YAML allergy**: some operators dislike YAML's whitespace sensitivity. Mitigation: orch-spawn CLI remains flag-driven (YAML is the wire format, not the human format).
- **Schema versioning thrash**: if v1 lands with the wrong shape, v2 migration is painful. Mitigation: design phase with Dan's YAML reference before any code lands.
- **Backend authors' burden**: existing spawn.sh scripts gain a new responsibility. Mitigation: provide a Go helper library in `internal/spawnspec/` that wraps the stdin/stdout shape so backends written in Go are trivial.

## Effort estimate

~2 weeks for design + implementation:
- Days 1-2: design phase with Dan, settle on YAML shape per his reference project
- Days 3-4: spawnspec package + validator + tests
- Days 5-6: orch-spawn refactor to build/dispatch SpawnSpec
- Days 7-8: tmux + cf-worker + cf-do backends updated
- Days 9-10: bench passes, docs published, validation helpers shipped
