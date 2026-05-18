# Proposal 0003 — Extract heavyweight executor backends to sister repos

**Status:** draft (spec only; design + implementation will follow as Dan adds new execution targets)
**Depends on:** Proposal 0002 (typed executor contract — required for clean per-repo backends)
**Blocks:** none

**Ousterhout-review adjustment (2026-05-18):** scope narrowed from "all executors" to "heavyweight only." `tmux:` backend (~50 LoC bash) stays in orch's main repo — extracting it creates release-coordination overhead for zero leverage gain. Extract only backends with substantial dependency footprints (CF Worker / Durable Object / future browser-tab / devcontainer).

## Why

Today `executors/` lives in-tree:

- `executors/tmux/` — bash; lightweight
- `executors/wasm/cf-worker/` — TypeScript + wrangler + miniflare; heavyweight
- `executors/wasm/cf-durable-object/` — TypeScript + DO bindings; also heavyweight

The CF Worker / Durable Object code dominates the executors directory. They're TS in an otherwise Go+bash repo. Their dependencies (wrangler, miniflare, @nats-io/transport-websockets) bloat installs.

Once Proposal 0002 lands (typed YAML contract), each backend becomes a process that reads SpawnSpec on stdin and writes WorkerHandle on stdout. That's a clean, language-agnostic interface — backends can live anywhere PATH-discoverable.

## Goals

1. Each executor backend is its own repo (or shares a meta-repo per backend family)
2. orch-spawn dispatches via PATH lookup (`orch-executor-<name>`) or `~/.local/share/orch/executors/<name>/spawn`
3. Each backend has its own release cadence, dependencies, language choice
4. orch's main repo no longer ships TS / wrangler code

## Non-goals

- Coupling backend versioning to orch's version (each releases independently)
- Locking in specific deployment targets (CF Worker users opt-in; tmux users don't need CF anything)

## Repo arrangement

Two sensible patterns; pick one:

### Option A: one repo per HEAVYWEIGHT backend

- `github.com/danmestas/orch-executor-cf-worker`
- `github.com/danmestas/orch-executor-cf-durable-object`
- `github.com/danmestas/orch-executor-devcontainer` (future)
- `github.com/danmestas/orch-executor-browser-tab` (future)

**Stays in orch's main repo:** `executors/tmux/` — too small to deserve extraction. The dispatcher's PATH discovery still finds it (lives at `~/.local/share/orch/executors/tmux/spawn`), just bundled with orch.

Pros: maximum independence for heavy backends; each repo's CI matches its language; clean release notes; tmux stays close to its only consumer.
Cons: more repos to discover / install (but fewer than option B's "everything is a repo").

### Option B: one meta-repo with subdirs per backend

- `github.com/danmestas/orch-executors` containing:
  - `cf-worker/`
  - `cf-durable-object/`
  - `devcontainer/` (future)

Pros: single discovery point; one issue tracker.
Cons: cross-language CI is awkward; release coupling.

**Lean: Option A.** Each heavyweight backend is its own thing. Tmux stays in orch.

## Per-backend contract (post-0002)

Each backend ships a binary or shell script discoverable as:
- `orch-executor-<name>` on PATH, OR
- `~/.local/share/orch/executors/<name>/spawn` (executable)

Invocation:
```
$ <backend>
  stdin:  SpawnSpec YAML (per proposal 0002)
  stdout: WorkerHandle YAML on success
  stderr: human-readable diagnostics
  exit:   0 success; non-zero failure
```

Optional supplementary commands (under design):
- `<backend> --validate <spec>` — pre-flight check without spawning
- `<backend> --status <handle-id>` — query worker lifecycle state
- `<backend> --abort <handle-id>` — imperative cancellation
- `<backend> --teardown <handle-id>` — cleanup after worker dies

## Migration plan

### Step 1: Land Proposal 0002 (typed executor contract)

Required prerequisite. Without the YAML contract, splitting backends multiplies bash IPC surface area.

### Step 2: Move tmux backend first (smallest, fastest, lowest risk)

1. New repo `orch-executor-tmux`
2. Move `executors/tmux/spawn.sh` → new repo's `bin/orch-executor-tmux`
3. Tests + README + release machinery
4. orch repo: orch-spawn no longer needs the in-tree tmux backend; PATH lookup finds the new one
5. Old `executors/tmux/` retained for one orch release with a deprecation notice

### Step 3: Move cf-worker backend (medium effort)

1. New repo `orch-executor-cf-worker`
2. Move all of `executors/wasm/cf-worker/` (TS + wrangler.toml + package.json + tests)
3. Build target: `orch-executor-cf-worker` shim binary that wraps `wrangler dev` invocation per SpawnSpec
4. orch consumes the new repo as a CLI on PATH

### Step 4: Move cf-durable-object backend (same shape as 3)

### Step 5: orch repo removes `executors/` directory entirely

1. Delete the directory
2. Update `docs/multi-executor-workers.md` to point at the per-backend repos
3. Update bench to install backend binaries during Docker build (or skip backend-specific tests if binaries aren't present, gated on a $ORCH_EXECUTORS_TO_TEST env)

## What changes for operators

- `orch-spawn claude --headless` works identically (PATH discovery)
- New backend installation: `npm i -g @agent-ops/orch-executor-cf-worker` (or curl-install pattern)
- `orch-version` should detect installed backends and report drift

## What changes for backend authors

- Each backend is a standalone project with its own README, CI, release notes
- Backend authors free to choose language (Go, Rust, TS, Python, shell — all fine as long as they honour the SpawnSpec stdin / WorkerHandle stdout contract)
- Contributors can ship a new backend without touching orch's main repo

## Backwards compatibility

- During the migration window: orch keeps the in-tree `executors/` AND looks for PATH-discovered backends; PATH wins if both present
- After cutover: in-tree directory is gone; PATH or `~/.local/share/orch/executors/` is the only discovery mechanism

## Acceptance criteria

- [ ] `orch-executor-tmux` repo published; orch consumes it; tmux flow works
- [ ] `orch-executor-cf-worker` repo published; orch consumes it; cf-worker integration tests pass
- [ ] `orch-executor-cf-durable-object` repo published; orch consumes it; DO tests pass
- [ ] orch's `executors/` directory deleted
- [ ] orch's `docs/multi-executor-workers.md` updated to point at per-backend repos
- [ ] Bench (docker-sesh) installs backend binaries during Docker build
- [ ] `orch-version` reports installed backends + their versions

## Decisions deferred to design phase

1. **One repo vs many?** Default to per-backend (option A) unless Dan prefers consolidation.
2. **Distribution channel**: GitHub Releases? Homebrew? npm? Different per backend (depending on language).
3. **`orch-version` enhancement**: how does it discover + audit installed backends? PATH walk + version probe per backend? Backends advertise their version via `--version` flag?
4. **Bench install logic**: clone all backends in the Dockerfile, or use prebuilt binaries from releases? Lean: prebuilt binaries to avoid build-time spread.
5. **Where do new backend templates live?** Suggest a `orch-executor-template` repo that future authors clone.

## Risks

- **Discovery drift**: operators install orch but forget to install backend binaries → cryptic "executor not found" errors. Mitigation: orch's install.sh prompts "do you want cf-worker support too?"; orch-version flags missing backends as warnings.
- **Backend version skew**: orch v2 might require SpawnSpec v2 fields that backend v1 doesn't honour. Mitigation: orch-spawn handshakes with backend via `--probe-version` before spawning.
- **Repo proliferation**: 4 backends × growing community = many repos to track. Mitigation: orch's docs maintain the canonical backend list.

## Effort estimate

~2 weeks per backend extraction (tmux: 3 days; cf-worker: 1 week; cf-durable-object: 4 days). Can be done one backend at a time across multiple quarters as Dan adds execution targets.
