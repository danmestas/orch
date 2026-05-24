# Multi-executor agent workers

**Status:** Deferred. As of #189 (friction 2), the dispatcher collapsed
into Go (`cmd/orch/spawn.go` — `orch spawn`) with tmux as the only
supported executor; the hybrid-discovery surface that this document
describes was retired with bin/orch-spawn. A second executor (WASM /
Cloudflare Worker / Durable Object) will return through a proper Go
`Engine` interface when there's a concrete consumer to anchor it
against. Until then, this document is the original architectural
proposal and tracks the broader roadmap; the implementation snippets
below describe the pre-#189 bash dispatcher and should be read as
historical context, not current behavior.

Orch today is an agent host: bash + tmux, with the `suit` outfit system
for skill / MCP / hook configuration. Sesh (sister project) is a neutral
substrate: NATS + Fossil + iroh under a per-user hub, with scoped KV
state, the task CAS pull protocol, and a six-pattern coordination
vocabulary. The two were built independently and don't yet talk. This
document proposes a contract that lets orch spawn agent workers across
heterogeneous executors (tmux, docker, ssh, Cloudflare Worker, Durable
Object, wasmtime, browser) while every worker participates fully in
sesh — same subject hierarchy, same Fossil substrate, same scoped state.

The proposal touches only orch. Sesh and EdgeSync stay unchanged.

---

## Current state — hybrid executor discovery (Proposal 0003)

`orch-spawn --executor <name>` resolves the backend command through
three layers, in this order:

1. **Operator override** — `ORCH_EXECUTOR_<NAME>_CMD` env var. Value is
   a shell command string interpreted by `bash -c` in the spawn
   subshell. Useful for pointing at deployed cloud backends without
   installing a local binary:

   ```sh
   ORCH_EXECUTOR_CF_WORKER_CMD="curl -sX POST https://my-cf.dev/spawn" \
     orch-spawn claude --executor cf-worker --headless
   ```

   The name is uppercased with `-` → `_` (so `cf-durable-object` →
   `ORCH_EXECUTOR_CF_DURABLE_OBJECT_CMD`). Quoting and expansion happen
   inside the subshell, not in orch-spawn's parent — operator-supplied
   strings never eval in the dispatcher's context.

2. **PATH binary** — `command -v orch-executor-<name>`. Future native
   binaries shipped by sister repos install here via npm / homebrew /
   goreleaser, the same shape as `orch-agent-shim`. Discovery is PATH
   only (no relative paths).

3. **In-tree fallback** — `${ORCH_REPO_ROOT}/executors/<name>/spawn.sh`.
   Preserves backward compatibility during the gradual cutover; the
   in-tree script must be executable.

If no layer resolves, the dispatcher exits non-zero with a diagnostic
that enumerates all three layers and the exact lookup it tried.

### Backend ownership (Proposal 0003, Ousterhout review 2026-05-18)

- **`tmux` stays in-tree.** `executors/tmux/spawn.sh` is ~50 LoC bash;
  extracting it would create release-coordination overhead exceeding the
  value of separation. The in-tree fallback is its permanent home.
- **`cf-worker` extracted to
  [`orch-executor-cf-worker`](https://github.com/danmestas/orch-executor-cf-worker).**
  Phase A scaffold merged 2026-05-23; Phase B (real implementation) is
  tracked there. Until Phase B lands, the in-tree
  `executors/wasm/cf-worker/` remains as the working backend
  (Decision 5: gradual cutover).
- **`cf-durable-object` extracted to
  [`orch-executor-cf-durable-object`](https://github.com/danmestas/orch-executor-cf-durable-object).**
  Same Phase A / Phase B split as cf-worker.
- **Future heavyweight backends** (`devcontainer`, `browser-tab`, …)
  follow the `orch-executor-<name>` naming convention and ship through
  the PATH or env-override layer; orch's main repo no longer absorbs
  their dependency footprint.

### Resolution order, in one line

`env-var > PATH binary > in-tree script` — operator override always
wins; PATH wins over in-tree when both exist; the diagnostic on a
failed resolution names every layer that was tried.

See `bin/orch-spawn` (`resolve_executor()`) for the implementation and
`test/test-orch-spawn-executor-resolution.sh` for the precedence
regression test.

---

## TL;DR

- Orch grows an `Executor` abstraction; tmux becomes one backend among
  several rather than the only one.
- Workers connect to sesh exclusively through EdgeSync — NATS subjects
  for messaging, Fossil checkout for files, iroh QUIC + holepunch for
  cross-NAT reach. No new transport code in orch.
- A single worker-bootstrap contract (shell flavor for native executors,
  JS module flavor for WASM executors) runs identically across all
  backends. Differences are confined to spawn / kill / status.
- WASM executors (Cloudflare Worker, Durable Object, wasmtime, browser)
  become first-class via EdgeSync's shipping WASM builds (`leaf.wasm`,
  `leaf-browser.wasm`).
- No new sister repo. All changes land in orch. Sesh stays pure substrate.

---

## Motivation

### Current orch limitations

- `tmux split-window` is hardcoded as the spawn primitive
  (`orch-spawn`); `orch-tell` sends prompts over the Synadia bus
  (`agents.prompt.…`) with a legacy `tmux send-keys` fallback; the
  Synadia bus (`agents.>`) is the only event channel after #94.
- Workers share the parent cwd or live in a tempdir outfit bundle. There
  is no edit isolation between parallel workers.
- No NATS, no Fossil, no participation in scoped state or task queues.
- The dependence on a terminal multiplexer prevents headless execution,
  cloud execution, remote execution, and machine-spanning topologies.

### Sister-repo capabilities already available

| Capability | Where lives | What it gives |
| --- | --- | --- |
| NATS hub with KV scopes | sesh (`~/.sesh/hub.nats.url`) | hub / project / session / workflow / agent state with watchers and CAS |
| Fossil per-project repo seeded from git worktree | sesh (`<cwd>/.sesh/project.repo`) | content-addressed VCS shared across sessions, sync'd over NATS |
| Per-session leaf with its own Fossil + JetStream | sesh (`<cwd>/.sesh/sessions/<label>.json`) | isolated workspace per worker without git pollution |
| Task CAS pull protocol with sweeper | sesh (`sesh_tasks_<scope>_<scope-id>`) | crash-safe work queues; expired pulls return to pending |
| Transport abstraction | EdgeSync (`libfossil.Transport`) | NATS / iroh / HTTP pluggable behind a single interface |
| Iroh P2P with QUIC + holepunch + relay | EdgeSync (iroh sidecar, Rust) | cross-NAT connectivity without Tailscale or public brokers |
| WASM builds shipping in CI | EdgeSync (`make wasm-wasi`, `make wasm-browser`) | `leaf.wasm` and `leaf-browser.wasm` — substrate runs in WASI and Web Worker |

The substrate is already capable of "workers everywhere". The gap is
that orch doesn't speak it.

---

## Architecture

### Three-repo separation

```
EdgeSync : transport (NATS / iroh / HTTP) + Fossil sync + WASM/native builds   ← substrate primitive
sesh     : sessions, scoped KV (memory + tasks), Fossil-per-project            ← substrate policy
orch     : agent lifecycle, outfit prep, executor dispatch, bootstrap modules  ← consumer
```

Only orch changes. Sesh stays a pure policy layer over EdgeSync.
EdgeSync stays a pure transport + sync layer. The bounded contexts do
not need to merge and no new sister repo is required.

### Executor abstraction

Orch exposes the smallest viable interface per backend:

```
spawn(outfit, session_label, task_id?) → worker_handle
kill(worker_handle)
status(worker_handle) → {state, last_event_ts, exit_code?}
```

Everything else — prompts, events, results — flows over NATS subjects
scoped to the worker's session. Executors differ only in how the
bootstrap script or module reaches its host: `tmux send-keys exec`,
`docker run`, `ssh user@host`, `wrangler deploy`, etc. Once the
bootstrap is running, the operating contract is executor-agnostic.

### Worker bootstrap contract

Every worker, regardless of executor, performs the same sequence:

1. Read env: `NATS_URL`, `SESH_SESSION`, `WORKER_ID`, `FOSSIL_URL`,
   `OUTFIT_BUNDLE_REF`, `AGENT_CLI` (native) or `AGENT_MODULE` (WASM).
2. Acquire the Fossil checkout — clone via `FOSSIL_URL` for remote
   executors; use the pre-mounted checkout for local executors.
3. Hydrate the outfit bundle from `<repo>/.sesh/outfits/<bundle-hash>/`
   inside the Fossil checkout (see Outfit bundle distribution below).
4. Subscribe to `orch.<session>.workers.<id>.prompt` and pipe the
   payload to the agent's stdin (or feed it to the agent module's
   conversation loop in the WASM flavor).
5. Replace stop / notify hooks with NATS publishers on
   `orch.<session>.workers.<id>.events.*`.
6. Exec the agent loop:
   - **Native flavor**: invoke `claude` / `codex` / `pi` / `gemini` with
     the bundle's config.
   - **WASM flavor**: `runBridge()` from `@synadia-ai/open-agent` owns the
     NATS wiring and `ToolLoopAgent` loop. The executor supplies a
     `sandboxFactory` that returns an environment-appropriate `Sandbox`
     implementation (stub, Vercel sandbox, or sidecar-backed).
7. On exit, publish a final event and commit any pending Fossil state.

Two flavors, one contract. Anything an executor wants to swap out is
local to its bootstrap implementation; the substrate interaction is
identical.

### Subject hierarchy

Orch-owned subjects (orch defines, sesh never needs to know):

```
orch.<session>.workers.<id>.prompt           orchestrator → worker prompt
orch.<session>.workers.<id>.events.stop      worker turn complete
orch.<session>.workers.<id>.events.notify    worker needs attention
orch.<session>.workers.<id>.events.exit      worker process / module exited
orch.<session>.workers.<id>.events.tool      tool call observed (optional)
orch.<session>.lifecycle                     worker created / started / stopped
```

Sesh-owned KV buckets orch piggybacks on (already shipping; see sesh's
[`scoped-memory.md`](https://github.com/danmestas/sesh/blob/main/docs/scoped-memory.md)
and [`task-management.md`](https://github.com/danmestas/sesh/blob/main/docs/task-management.md)):

```
sesh_tasks_<scope>_<scope-id>       task records (CAS pull protocol)
sesh_agent_<role>_<worker-id>       per-worker memory
sesh_session_<project>_<session>    per-session memory
sesh_project_<project>              per-project memory
```

`<id>` is a ULID or short hash chosen by orch — opaque to sesh,
meaningful to orch. Workers join tracing via sesh's
[`message-envelope.md`](https://github.com/danmestas/sesh/blob/main/docs/message-envelope.md)
`traceparent` convention.

### Outfit bundle distribution

`suit prepare` writes a config bundle to a host tempdir today. That's
fine for local executors and useless for remote / cloud workers, which
have no access to the operator's filesystem.

**Decision: bundles travel through Fossil.**

- Orch tars the bundle, commits it to
  `<repo>/.sesh/outfits/<bundle-hash>/` in the project Fossil repo.
- Spawn passes `OUTFIT_BUNDLE_REF=<bundle-hash>` as env to the worker.
- The worker reads the bundle from the well-known path inside its
  Fossil checkout.

Content-addressed hashing means identical outfits dedupe. Bundles end up
in project history as a transcript of "what tool config produced this
work". No NATS-blob hack, no per-executor upload mechanism, no separate
artifact store.

### Per-worker session granularity

One `sesh up --session=<worker-id>` per worker. Each worker has:

- Its own Fossil checkout (full edit isolation)
- Its own session JSON in `<cwd>/.sesh/sessions/<worker-id>.json`
- Its own scoped KV (`sesh_agent_*_<worker-id>`,
  `sesh_session_*_<worker-id>`)

Workers do not see each other's in-flight files except through explicit
publishes to Fossil + the cross-process sync that follows.

Operators wanting brainstorm / review topologies opt in with
`--share-session=<label>`, which attaches the new worker to an existing
session and accepts the edit-conflict risk in exchange for shared-state
visibility. Defaults to isolation; sharing is opt-in.

### Cross-network connectivity

EdgeSync's iroh sidecar (`IROH_ENABLED=true` + peer endpoint URL)
handles QUIC + holepunch + relay between any two networks. Operators do
not configure Tailscale, public NATS brokers, or leaf-node-TLS bridging.
A Cloudflare Worker running `leaf-browser.wasm` connects to a home hub
behind NAT via iroh's tunnel — no infra-side network configuration
required.

This is what makes cloud and remote executors practical without
additional VPN or relay infrastructure.

---

## Executor catalog

| Executor | Flavor | Spawn mechanism | Best for | Constraint |
| --- | --- | --- | --- | --- |
| `tmux` | native | `tmux split-window` + exec bootstrap | local interactive dev (today's path) | tmux on host only |
| `docker` | native | `docker run` with mounts, host network | local isolated testing / building | image management overhead |
| `wasmtime` | WASM | `wasmtime run leaf.wasm` + agent module | sandboxed pure-reasoning agents | no shell exec inside |
| `ssh` | native | `ssh user@host bash worker-bootstrap.sh` | long-running native daemons on owned hardware | bandwidth / latency to remote |
| `cf-worker` | WASM | `wrangler deploy` + invoke | scale-to-zero cloud agents (planners, routers, watchers) | no shell exec; pure-reasoning only |
| `cf-durable-object` | WASM | DO instantiation | stateful cloud agents (chat sessions, accumulators) | no shell exec |
| `browser` | WASM | open URL hosting `leaf-browser.wasm` | human-loop UIs, prototyping | tab lifetime |

Native executors get full shell capability — `pytest`, `cargo`, `git`,
arbitrary processes. WASM executors are limited to text I/O, HTTP /
fetch, sandboxed filesystem (WASI fs or OPFS), and direct LLM API calls.
WASI Preview 1 has no `process_spawn`; CF Workers and browsers forbid it.

Most useful topologies combine both: WASM at the top (planners, routers,
watchers — scale-to-zero, always-on) and native at the bottom
(executors running real tools).

---

## Sesh-side contract

What orch can assume from sesh (no sesh changes required):

- `sesh up --session=<label>` creates a session, writes
  `<cwd>/.sesh/sessions/<label>.json` with `nats_url`, `leaf_url`,
  `fossil_url`.
- `~/.sesh/hub.nats.url` provides hub connectivity for cross-session
  shared state.
- `<cwd>/.sesh/project.repo` is the project Fossil repo, shared across
  sessions in the same project, seeded from the git worktree on first
  `sesh up`.
- `sesh_tasks_<scope>_<scope-id>` KV buckets implement the task CAS
  pull protocol per sesh's
  [`task-management.md`](https://github.com/danmestas/sesh/blob/main/docs/task-management.md),
  with a sweeper that resets expired in-progress pulls.
- The five-scope memory model (hub / project / session / workflow /
  agent) per sesh's
  [`scoped-memory.md`](https://github.com/danmestas/sesh/blob/main/docs/scoped-memory.md)
  is available through the corresponding KV buckets.

What orch contributes back to sesh: nothing in the substrate. Orch sits
entirely above sesh, defines its own subject prefix (`orch.>`), owns its
own state. Sesh remains pattern-agnostic.

---

## MVP shape

Minimum viable integration — sesh path, native flavor, local executor
only — to validate the bootstrap contract and the substrate plumbing.

```sh
# orch-spawn-sesh (parallel to orch-spawn, ~150 LOC bash)

session_label="orch-$(ulid)"
sesh up --session="$session_label"

session_json="$(pwd)/.sesh/sessions/$session_label.json"
nats_url=$(jq -r .nats_url   "$session_json")
fossil_url=$(jq -r .fossil_url "$session_json")
checkout_dir="$(pwd)/.sesh/checkouts/$session_label"

# Prepare outfit + commit bundle to Fossil under .sesh/outfits/<hash>/
bundle_dir=$(suit prepare --outfit="$1" --cut="$2")
bundle_hash=$(orch_commit_bundle_to_fossil "$bundle_dir")

# Spawn worker — native flavor, runs in the Fossil checkout dir
export NATS_URL="$nats_url"
export SESH_SESSION="$session_label"
export WORKER_ID=$(ulid)
export OUTFIT_BUNDLE_REF="$bundle_hash"
export AGENT_CLI=claude

cd "$checkout_dir"
exec bash "$ORCH_LIB/worker-bootstrap.sh"
```

`worker-bootstrap.sh` (~50 LOC):

```sh
#!/usr/bin/env bash
set -euo pipefail
prefix="orch.${SESH_SESSION}.workers.${WORKER_ID}"

# Hydrate outfit bundle from Fossil checkout
bundle_dir=".sesh/outfits/${OUTFIT_BUNDLE_REF}"

# stdin-bridge sidecar: NATS prompt subject → agent stdin via FIFO
agent_stdin="/tmp/agent-${WORKER_ID}.stdin"
mkfifo "$agent_stdin"
nats sub "${prefix}.prompt" --raw \
    --server "$NATS_URL" \
  | awk 'BEGIN{RS=""} {print > "'"$agent_stdin"'"}' &

# Replace marker-file hooks with NATS publishers
export CLAUDE_HOOK_STOP="nats pub ${prefix}.events.stop  --server $NATS_URL"
export CLAUDE_HOOK_NOTIFY="nats pub ${prefix}.events.notify --server $NATS_URL"

# Trap exit; publish a final event
trap 'nats pub "${prefix}.events.exit" "{\"code\":$?}" --server "$NATS_URL"' EXIT

# Drive the agent
"$AGENT_CLI" --config "$bundle_dir/config.json" < "$agent_stdin"
```

Total new code for MVP: ~250 LOC bash + 0 LOC in sesh + 0 LOC in
EdgeSync.

---

## Phased rollout

| Phase | Executor | Flavor | Adds | Estimated LOC |
| --- | --- | --- | --- | --- |
| 1 | `tmux` (with sesh substrate) | native | NATS plumbing, stdin-bridge sidecar, NATS-publishing hooks, `orch-spawn-sesh` entrypoint | ~250 bash |
| 2 | `docker` | native | image with agent CLIs baked, mount-based Fossil checkout, host-network NATS | ~100 bash + Dockerfile |
| 3 | `ssh` | native | remote bootstrap, iroh-enabled EdgeSync on target host | ~50 bash + setup script |
| 4 | `cf-worker` | WASM | **Synadia `open-agent` plugin** as the agent harness; `wrangler` deploy; NATS-over-WebSocket transport | ~60 TS + wrangler config |
| 5 | `cf-durable-object` | WASM | DO class wrapping the open-agent bridge for stateful multi-turn sessions | ~150 JS on top of phase 4 |
| 6 | `wasmtime` | WASM | wasmtime host harness for local sandboxed execution | ~150 Go or Rust |
| 7 | `browser` | WASM | minimal HTML host loading `leaf-browser.wasm` + open-agent bridge | ~50 HTML + reuse phase 4 |

**Phase 4 decision (resolved):** Phases 4–7 use Synadia's
[`agents/open-agent/`](https://github.com/synadia-ai/synadia-agents/tree/main/agents/open-agent)
as the agent harness. `open-agent` already speaks the Synadia Agent Protocol
for NATS, exposes a `ToolLoopAgent`, and has a swappable `Sandbox` seam.
Orch contributes the placement (wrangler deploy, DO instantiation, browser
host) and a `Sandbox` stub; `open-agent`'s `runBridge()` owns the NATS
wiring, heartbeats, and prompt streaming. No bespoke bootstrap code needed.

The proof-of-concept lives in `executors/wasm/cf-worker/` (NATS-over-WebSocket,
stub sandbox); `examples/cf-worker-agent/README.md` documents the deploy and
verify flow. Iroh-bridged NATS and a real sandbox impl are future work.

Phases 1–3 are unblocked today. Phase 4 proof-of-concept is in
`executors/wasm/cf-worker/`. Phases 5–7 build on phase 4 by swapping the
host (Durable Object, wasmtime, browser) while reusing `open-agent`
verbatim. Each phase ships independently.

---

## Topologies enabled

The substrate already supports the six patterns in sesh's
[`coordination-patterns.md`](https://github.com/danmestas/sesh/blob/main/docs/coordination-patterns.md).
This proposal adds executor heterogeneity within those patterns:

- **Mixed local + cloud orchestrator–subagent.** Orchestrator on your
  Mac (`tmux`, native); subagents on Cloudflare Workers (`cf-worker`,
  WASM, scale-to-zero). Orchestrator dispatches research / planning
  tasks to ephemeral cloud workers; results commit to Fossil; orch
  subscribes to `result.<task-id>`. Cloud workers cost pennies and
  vanish when idle.
- **Cross-host agent team.** N workers on a Hetzner box (`ssh`, native)
  pulling from a `sesh_tasks_project_<id>` queue. Each holds its own
  durable session; the queue survives crashes via the sweeper. Your
  Mac can sleep — work continues.
- **Hierarchical with WASM at the planning tier.** Top-tier planner on
  CF Worker (always-on, free idle); mid-tier coordinators on Hetzner
  native; leaf-tier executors on local docker. Planner publishes
  subtasks; coordinators claim and dispatch to executors that have the
  right tooling.
- **Watcher + actor split.** A long-running CF Worker subscribes to
  alert / notify subjects and converts them to tasks in
  `sesh_tasks_project_<id>`. Your orch session, when active, picks
  tasks up and dispatches to native executors. The system is "always
  paying attention" without your Mac being on.

These topologies aren't theoretical — they're what falls out of the
executor catalog × sesh's existing pattern primitives.

---

## Tasks for orch-development dogfood

Sesh's task system ships today. Use it now to track orch-development
work, manually for the bootstrap phase, then via `orch-spawn-sesh`
once phase 1 lands.

```sh
project_id=$(jq -r .project_id "$(pwd)/.sesh/project.json")

nats kv put "sesh_tasks_project_${project_id}" task-001 '{
  "id":"task-001","v":1,"title":"Implement orch-spawn-sesh MVP",
  "status":"pending","priority":5,
  "depends_on":[],"max_attempts":3,
  "created_at":"2026-05-13T00:00:00Z","created_by":"operator"
}'

nats kv watch "sesh_tasks_project_${project_id}"
```

After phase 1: `orch-spawn-sesh --task=task-001 --outfit=backend --cut=executing`
performs the CAS claim, spawns a worker in an isolated Fossil checkout,
runs the agent loop, commits the result, marks the task done. The
sweeper handles expired pulls if the worker dies mid-task.

This is the dogfood loop: sesh provides the task substrate, orch
consumes tasks via spawn, workers complete them in isolated Fossil
checkouts, sync back to the operator's git tree.

---

## Open questions

Three forks worth resolving before MVP code lands.

### Per-worker session vs shared session

Default to **per-worker session** (isolation by default); allow
`--share-session=<label>` for explicit collaboration topologies.
Rationale: edit conflicts are silent and dangerous in parallel coding;
the opt-in flag forces the operator to acknowledge the risk.

Decided; flagged here for future reference.

### Generic agent-loop module vs outfit-specific bundles

**Resolved:** Use Synadia's `open-agent` plugin verbatim. Do not fork and do
not write a bespoke agent-loop module.

`open-agent`'s `runBridge()` is already generic and outfit-parameterized via
its `sandboxFactory`, `modelFactory`, and `modelId` arguments. Outfit
configuration (system prompt, tool allowlist, model choice) is passed at
runtime via env vars or a thin wrapper — no per-outfit redeploy required.

The one remaining decision (how to hydrate an outfit bundle from Fossil inside
a CF Worker) is deferred to the DO phase (phase 5), which has persistent
storage and can hold the bundle in DO storage.

### First cloud executor: ssh or cf-worker?

`ssh` is the simplest adapter to write (~50 LOC bash, reuses existing
claude CLI as-is, native shell capability). `cf-worker` has the highest
leverage long-term (scale-to-zero, always-on, no infra to maintain) but
requires the WASM agent-loop module first (~300-500 LOC JS plus the
contract decision above).

Tentative ordering in the rollout table (ssh first, cf-worker second).
Revisit if the WASM agent-loop turns out smaller than estimated, or if
a concrete cloud use case appears before the SSH adapter ships.

---

## Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| WASM workers can't shell out for `pytest` / `cargo` / `git` | Topology mix: WASM at planning tier, native at execution tier. Document the boundary. |
| Agent CLI auth on remote / cloud hosts (claude-code wants `~/.claude/`) | SSH: agent forwarding or pre-populate the server. Cloud WASM: shift to `ANTHROPIC_API_KEY` headless path — different auth model, but the right boundary for ephemeral hosts. |
| Outfit bundle bloat in Fossil history | Content-addressed bundle hashes dedupe identical outfits; periodic Fossil rebuild handles long-tail drift. |
| iroh holepunch fails on hostile networks (symmetric NAT, CGN) | iroh has relay fallback. Operator can also fall back to leaf-node-over-TLS using existing EdgeSync `NATSTransport`. |
| CF Worker cold-start latency | Tolerable for queue-driven tasks (seconds, not real-time). Use Durable Objects for warm long-lived agents. |
| Cross-network NATS attack surface (cloud worker on the bus) | NATS supports per-worker nkeys with scoped subject permissions; configure when going multi-tenant or after first real-world exposure. See sesh's coordination-patterns.md "No identity / signed messages". |
| Outfit drift while worker is running (operator commits new outfit mid-task) | Workers snapshot the bundle at spawn time; long-running workers don't auto-pick-up updates. Restart to refresh. |
| Fossil sync bandwidth on slow links | Project repos stay code-shaped (text, no build artifacts). For build outputs use NATS object store with TTL, not Fossil. |

---

## Out of scope (intentional non-goals)

- **Replacing tmux for local interactive dev.** The `tmux` executor
  stays as a first-class path. Operators who want a pane-based REPL
  experience keep it.
- **Sesh learning about agents.** Sesh remains pattern-agnostic.
  Outfits, MCPs, agent CLIs, and orch-specific subjects all live in
  orch.
- **A universal "agent bus" library.** No third sister repo. If a
  second consumer ever wants the same primitives, extract then. Don't
  pre-extract.
- **Multi-tenant NATS auth.** Out of scope for MVP; addressed when a
  real exposure appears (see Risks).
- **WASM-native agent CLIs (claude / codex / pi / gemini compiled to
  WASM).** Not feasible in 2026. WASM executors run a JS / WASM
  agent-loop driving the LLM API directly, not the interactive CLIs.

---

## Further reading

Sesh substrate docs (sister repo at
[github.com/danmestas/sesh](https://github.com/danmestas/sesh)):

- [`coordination-patterns.md`](https://github.com/danmestas/sesh/blob/main/docs/coordination-patterns.md)
  — the six multi-agent patterns sesh supports; this proposal layers
  executor heterogeneity on top.
- [`scoped-memory.md`](https://github.com/danmestas/sesh/blob/main/docs/scoped-memory.md)
  — KV scopes consumed by per-worker and per-session state.
- [`task-management.md`](https://github.com/danmestas/sesh/blob/main/docs/task-management.md)
  — task CAS pull protocol orch uses for queue-driven topologies.
- [`message-envelope.md`](https://github.com/danmestas/sesh/blob/main/docs/message-envelope.md)
  — header convention for cross-worker tracing.

EdgeSync substrate (sister repo): `libfossil.Transport` interface
(`leaf/agent/{nats,iroh}.go`), iroh sidecar (`iroh-sidecar/`), WASM
build targets (`Makefile`: `wasm-wasi`, `wasm-browser`).
