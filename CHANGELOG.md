# Changelog

## 0.2.2 (2026-05-24)

Release-pipeline fix: the v0.2.1 release attempt failed in goreleaser
because `goreleaser release --clean` deletes the default `dist/`
directory, which includes the committed `dist/schema/*.json` files
(SpawnSpec v1/v2 wire schemas). Goreleaser's dirty-state check then
aborted before any binaries were built or published.

Fixed by giving goreleaser its own scratch dir via the `dist:` field
in `.goreleaser.yaml` — schemas in `dist/` are no longer disturbed.

No code or behavior changes — release pipeline only.

## 0.2.1 (2026-05-24)

Install fix for v0.2.0: `npm install -g @agent-ops/orch` succeeded but
the lazy-build bash shims (`orch`, `orch-subtree`, `orch-workflow`, the
`orch-cc-subagent-bridge` extension) failed at first invocation with
`cannot find go.mod above /opt/hostedtoolcache/node/.../bin` because the
npm tarball does not ship Go source. `orch --help` was the first symptom
because `bin/orch` itself is a lazy-build shim around `cmd/orch/` (the
consolidated tell/ask/peek/spy/spawn binary from #189).

Fix mirrors the synadia-agent-shim pattern: goreleaser builds platform
binaries on tag, uploads them to the GitHub Release, and the postinstall
fetches the right archive for the user's OS/arch and unpacks into
`vendor/`. The bash shims now exec the vendored binary on installed
machines, falling back to lazy-build only on dev checkouts where
`go.mod` is reachable.

- **`.goreleaser.yaml`** — multi-platform Go builds for darwin + linux ×
  amd64 + arm64. One archive per OS/arch
  (`orch_${version}_${os}_${arch}.tar.gz`) bundling all six Go
  binaries: `orch`, `orch-subtree`, `orch-workflow`, `orch-registry`,
  `orch-goal-stop-account-daemon`, `orch-cc-subagent-bridge`.
- **`scripts/postinstall.js`** — fetches the platform archive from
  `github.com/danmestas/orch/releases/download/v${version}/...` and
  extracts the binaries into `vendor/`. Skips when
  `ORCH_SKIP_DOWNLOAD=1` (CI / offline / vendored installs) or when the
  vendored binaries already match the package version (idempotent
  re-installs). Symlink farm + fleet doctrine injection unchanged.
- **`bin/orch`, `bin/orch-subtree`, `bin/orch-workflow`** — vendor-first
  resolution. Lazy-build is now the fallback for dev checkouts only;
  the error message points the operator at
  `npm install -g @agent-ops/orch` when neither path is available.
- **`bin/orch-registry`, `bin/orch-goal-stop-account-daemon`** — new
  shims with the same vendor-first pattern, so npm-installed users get
  them on PATH (previously only present after `install.sh` lazy-built
  `orch-registry` directly into `~/.local/bin`).
- **`bin/orch-up`** — extension launch loop checks `vendor/<basename>`
  unconditionally (not gated on the cmd source dir being present), so
  installed users without Go source still resolve
  `orch-cc-subagent-bridge`.
- **`.github/workflows/release.yml`** — adds a `goreleaser` job that
  runs before `publish`; `publish` depends on `goreleaser`. New smoke
  step validates the full install path: install the just-published
  package, exec each vendored binary, fail loudly if any binary falls
  through to the `cannot find go.mod` error.
- **`package.json`** — version bump to `0.2.1`; new bin entries for
  `orch-subtree`, `orch-workflow`, `orch-registry`,
  `orch-goal-stop-account-daemon`. `files` manifest no longer ships
  `cmd/`, `internal/`, `go.mod`, `go.sum` — installed users get
  pre-built binaries via the postinstall fetch.

The tag push is operator action — push `v0.2.1` after this PR merges.

## 0.2.0 (2026-05-24)

First minor release after the v0.1.x bash era. Highlights:

- **HARD CUT of bash CLIs** (#189 friction points 1+2+3): `orch-tell`,
  `orch-peek`, `orch-spy`, `orch-ask`, `orch-spawn`, `orch-claim-operator`,
  `orch-register` are gone with no shim. All flows now go through
  `orch <subcommand>` (Go binary). The `package.json` `bin` manifest no
  longer publishes them; consumers with hardcoded references must
  migrate. `orch migrate-aliases` prints sed-style rewrites for shell
  config files.
- **Synadia Agent Protocol is the only wire** (#94, shipped earlier in
  the 0.1.x line and now made permanent): the legacy filesystem-marker
  IPC, the NATS comms bridge daemon, the per-harness publish hooks, and
  the fswatch-based listener are deleted.
- **Pluggable engines land** (#180, #207/#208, #209, #210, #211, #212):
  `internal/persistence.Engine` is a real seam with `tmux`, `cmux`, and
  `zmx` implementations; `orch spawn --persistence <engine>
  --layout <layout>` picks the composition; the SpawnSpec v2 wire format
  carries `executor` (`cmux`/`zmx` join `tmux`) and supports
  `layout: none` for headless flows.
- **Workflow YAML CLI lands** (#202, #145, #146): `orch-workflow`
  parses, validates, compiles, applies, statuses, and cancels workflow
  DAGs seeded against `sesh-ops`.
- **Subtree topology phase B** (#201, #145): `orch-subtree apply/
  status/destroy/watch` wired to the live registry + NATS.
- **install.sh absorbed** into `scripts/postinstall.js` (#189 friction
  point 2): `npm install -g @agent-ops/orch` is now the single install
  path on every platform.
- **claudecode-subagent-panel extension** (#93): orch panes surface in
  Claude Code's subagent panel via the `orch-cc-subagent-bridge`
  sidecar.

Semver minor bump justified by the bash-CLI hard cut (consumers with
hardcoded references break) plus the new public surface (SpawnSpec v2,
pluggable engines, workflow/subtree CLIs).

### Breaking changes (summary)

- `bin/orch-tell`, `bin/orch-peek`, `bin/orch-spy`, `bin/orch-ask`,
  `bin/orch-spawn`, `bin/orch-claim-operator`, `bin/orch-register`,
  `bin/orch-nats-bridge-in`, `bin/orch-listen`, `bin/orch-current-jsonl`:
  all deleted. No compatibility shim. Use `orch <subcommand>` instead;
  see `orch migrate-aliases` for shell-config rewrites.
- `orch-tell --legacy-keystrokes` and `orch-ask --legacy` paths removed
  (#96). Panes must be Synadia-registered; discovery failure is now a
  hard error, not a silent tmux send-keys fallback.
- `executors/` directory deleted with `orch-spawn` (#206); returns when
  a second concrete executor backend ships.
- Per-harness Claude Code hooks (`orch-stop-marker.sh`, etc.) deleted;
  `~/.claude/settings.json` entries referencing them become dangling
  and are stripped by `orch down`.

### Release-cycle PRs

#173, #174, #175, #176, #177, #179, #183, #186, #187, #190, #191, #192,
#193, #194, #195, #196, #197, #198, #200, #201, #202, #203, #204, #205,
#206, #208, #209, #210, #211, #212.

### feat(spawnspec): v2 wire format — cmux + zmx executors, layout=none (#212)

SpawnSpec gains an explicit `version: 2` and grows the
`executor: tmux | cmux | zmx` field plus `layout: tmux | cmux | none`.
Phase-A `executor: tmux` specs continue to validate (the schema treats
absence of `version` as v1 for backward compat across the boundary),
but the writers in `cmd/orch/spawn.go` now always emit v2. The shim
treats `layout: none` as "no surface", letting headless flows
(API-driven spawns, daemon workers) opt out of pane-allocation
entirely.

### refactor(subtree): dispatch worker-killer abort + kill through engine handles (#211)

`internal/subtree/worker_killer.go` no longer shells out to
`tmux kill-pane` directly; it dispatches abort + kill through the
persistence engine handle, so cmux + zmx workers respect the same
teardown semantics as tmux.

### feat(orch): add zmx persistence engine — Proposal 0008 Phase C, zmx Phase 2 (#210)

`orch spawn --persistence zmx --layout zmx <agent>` spawns workers
into zmx (Zellij-multiplexer) sessions, mirroring the tmux + cmux
shapes. Composition table grows `{zmx, zmx}`. Cross-engine pairs still
reject at flag-parse with a clear diagnostic.

### refactor(orch): extract persistence.Engine seam around tmux + cmux — zmx Phase 1 (#209)

`internal/persistence/engine.go` is now a real interface (was
copy-paste between tmux + cmux in #207/#208). Both tmux and cmux
implementations move behind it; the subsequent zmx engine (#210) lands
as a third implementation rather than a third copy.

### feat(orch spawn): cmux persistence engine — Proposal 0008 Phase B (#207)

`orch spawn --persistence cmux --layout cmux <agent>` now spawns a
worker into a cmux pane via cmux's CLI (`cmux new-pane` + `cmux send`),
in addition to the default tmux path.

The cmux engine mirrors `cmd/orch/spawn_tmux.go`'s shape inline in
`cmd/orch/spawn_cmux.go` — no `Engine`/`Handle`/`LayoutEngine`
interfaces yet (Rule of Three: extract when a third engine, e.g. zmx,
lands). `cmd/orch/spawn.go` switches on `--persistence` to pick the
concrete spawn function.

Composition table (`internal/persistence/registry.go`) grows
`{cmux, cmux}` alongside `{tmux, tmux}`. Cross-engine pairs
(`{tmux, cmux}`, `{cmux, tmux}`) continue to reject at flag-parse with
a clear diagnostic.

Deliberately deferred (return clean operator errors, not silent
fall-through):

- `--verify` on cmux — `internal/tmuxctl.Verify` is tmux-specific.
- `--headless` on cmux — cmux has no headless-session concept.

The shim wire format (`--pane <locator>`) is unchanged; the shim
treats the locator as an opaque string, so cmux's `surface:30`-style
refs flow through.

### feat(orch spawn): inline orch-spawn + executors/ into Go (#189 friction point 2)

`bin/orch-spawn` (~1000 LoC bash) and `executors/tmux/spawn.sh` (~316
LoC bash) have been collapsed into `cmd/orch/spawn.go` +
`cmd/orch/spawn_tmux.go` (~1000 LoC Go). Operators now invoke
`orch spawn <agent> [flags...]` — the legacy `orch-spawn` bash entry
point has been deleted (HARD CUT, no shim).

Helper package `internal/tmuxctl/` houses the readiness-poll loop,
banner detection, and adapter probe used by the new Go subcommand.

Deleted alongside `bin/orch-spawn`:

- `executors/` directory entirely (was placeholder Wrangler scaffolds
  for future WASM/CF executors plus the now-superseded
  `executors/tmux/spawn.sh`). The Executor abstraction returns when a
  second concrete backend ships.
- `internal/persistence/tmux/`, `internal/persistence/engine.go`,
  `internal/layout/`, `internal/instance/` — Phase-A engine
  scaffolding from #180 that bridged to the deleted bash. The closed
  `(persistence, layout)` composition registry stays
  (`internal/persistence/registry.go` is still used by `orch-engines`).
- `--executor` flag's hybrid discovery (env / PATH / in-tree). The
  flag still parses but only accepts `tmux`.

### feat(postinstall): absorb install.sh (#189 friction point 2)

`install.sh` has been deleted. Its work — symlinking hooks/skills,
caching the fleet doctrine, and injecting the doctrine block into
`~/.codex/AGENTS.md` and `~/.gemini/GEMINI.md` — moved into
`scripts/postinstall.js` so `npm install -g @agent-ops/orch` is now the
single install path on every platform.

### feat(cmd/orch): collapse orch-tell / orch-peek / orch-spy / orch-ask into `orch <subcommand>` (#189 friction points 1 + 3)

The four bash CLIs that used to live at `bin/orch-tell`, `bin/orch-peek`,
`bin/orch-spy`, and `bin/orch-ask` (≈ 950 LoC) have been collapsed into
a single Go binary under `cmd/orch/` whose subcommands import the
existing `internal/registry` package directly. The `bin/orch` entrypoint
is now a thin lazy-build shim around that binary, matching the
`bin/orch-workflow` pattern.

User-visible CLI shape after upgrade:

| Was                | Now                            |
|--------------------|--------------------------------|
| `orch-tell …`      | `orch tell …`                  |
| `orch-ask …`       | `orch ask …`                   |
| `orch-peek …`      | `orch peek …`                  |
| `orch-spy …`       | `orch spy …`                   |
| `orch-claim-operator` | (deleted; `export ORCH_ROLE=operator`) |
| `orch-register`    | (deleted; the shim auto-registers) |

To migrate shell aliases / dotfiles, run:

```bash
orch migrate-aliases
```

That new subcommand scans `~/.bashrc`, `~/.zshrc`, `~/.bash_aliases`,
`~/.zprofile`, `~/.profile`, `~/.config/fish/config.fish`,
`~/.config/orch-aliases`, and (if invoked inside a git repo) the repo
itself for references to the retired CLIs, and prints sed-style rewrite
suggestions. It never auto-writes.

A new `internal/synadia` package centralises the protocol constants
(`AdapterMissingExitCode = 2`, `TerminatorByte = 0x00`, the §9
`Nats-Service-Error-Code` → exit-code mapping) and the `IsTerminator`
predicate that were previously embedded as magic numbers across the
bash scripts and the goal-stop-account daemon. The daemon imports them
now; future protocol bumps touch one file.

#### Breaking

- `bin/orch-tell`, `bin/orch-peek`, `bin/orch-spy`, `bin/orch-ask`:
  deleted. Hard cut — no compatibility shim. Use `orch tell` etc.
- `bin/orch-claim-operator`, `bin/orch-register`: deleted (they had
  been no-op deprecation stubs for several releases).
- `package.json` `bin` manifest drops the six retired entries.

### feat(extensions): claudecode-subagent-panel bridge — surface orch panes in Claude Code's subagent panel (#93)

A new top-level `extensions/` plane lands alongside executors and adapters.
The first extension, `claudecode-subagent-panel`, runs as a sidecar daemon
(`orch-cc-subagent-bridge`) launched by `orch up` and stopped by `orch down`.
It subscribes to `$SRV.INFO.agents` (discovery) and `agents.>` (chunks) and
synthesises JSONLs under `~/.claude/projects/<cwd-enc>/<session-uuid>/subagents/`
so every orch-spawned pane — claude, codex, pi, gemini — appears in Claude
Code's subagent panel. The bridge is harness-agnostic (reads only the SAP
wire). See [`docs/extensions.md`](docs/extensions.md) and
[`extensions/README.md`](extensions/README.md) for the contract.

### Breaking changes

- **`orch-tell --legacy-keystrokes` removed** (#96). The tmux send-keys fallback
  flag and its associated code path (`_send_via_tmux`, `ORCH_TELL_FORCE_LEGACY`,
  auto-fallback on discovery no-match) have been deleted. All panes must be
  registered on the Synadia bus via `orch-spawn`. On discovery failure,
  `orch-tell` now emits an actionable error and exits non-zero — there is no
  silent fallback.

- **`orch-ask --legacy` removed** (#96). The tmux capture-pane snapshot diff
  path in `orch-ask` has been removed. `orch-ask` is now unconditionally a thin
  wrapper around `orch-tell --collect`.

### chore: retire legacy bridge + fs-marker hooks + legacy listener (#94) — BREAKING

The legacy comm substrate (filesystem-marker IPC, the orch NATS comms bridge
daemon, the per-harness NATS-publish hooks, and the fswatch-based listener)
has been deleted. The Synadia Agent Protocol path via `orch-agent-shim` is
now the only wire.

**Deleted bins:** `orch-nats-bridge-in`, `orch-listen`, `orch-current-jsonl`.
**Deleted hooks:** `orch-stop-marker.sh`, `orch-notify-marker.sh`,
  `orch-session-jsonl.sh`, `orch-nats-publish-{stop,notify,jsonl}.sh`,
  and the per-harness counterparts under `executors/tmux/legacy/`
  (`codex-hooks/`, `gemini-hooks/`, `pi-extensions/`).
**Deleted snippets:** `codex-hooks-snippet.json`, `gemini-settings-snippet.json`.
**Settings-snippet content reduced** to a single SessionStart entry for
  `orch-goal-session-context.sh` (the only still-live Claude Code hook).
**Deleted tests:** `test-orch-listen-stream.sh`, `test-orch-subscribe.sh`,
  `test-orch-subscribe-real.sh`, `test-orch-current-jsonl.sh`.
**Docker bench:** legacy bridge-dependent tests (T4-T7 in `test/docker`,
  T3.2/T6.x in `test/docker-sesh`) removed; shim-side coverage (T9-T11)
  remains.
**Removed top-level compat symlinks:** `hooks/orch-{stop,notify}-marker.sh`,
  `codex-hooks/`, `gemini-hooks/`, `pi-extensions/`.

**Operator impact:** existing `~/.claude/settings.json` entries pointing to
the deleted hook scripts become dangling references. `orch-down` strips
them on the next run; or remove them manually. Operator caches under
`~/.cache/orch-stop/`, `~/.cache/orch-notify/`, `~/.cache/orch-nats-bridge.log`,
and `~/.orch/sessions/` are migration residue — `orch-down` sweeps them.

**Skills updated:** `assume-orch`, `orch-driver`, `orch-suiting`,
`tmux-agent-panes`, `migrating-to-synadia` now describe only Synadia
primitives. `docs/nats-bridge.md` is retained as the historical record;
`docs/synadia-comparison.md`, `docs/multi-executor-workers.md`, and
`docs/orch-agent-shim.md` were updated in place.

### refactor: executor pluralism — `executors/<type>/` is now first-class. tmux flow moved to `executors/tmux/`. No behavior change.

- `executors/tmux/spawn.sh` — tmux-specific spawn logic extracted from `bin/orch-spawn`
- `executors/tmux/hooks/` — tmux-executor hooks moved from `hooks/` (backward-compat symlinks remain)
- `executors/tmux/legacy/` — deprecated codex-hooks, gemini-hooks, pi-extensions (one-cycle retention)
- `executors/wasm/cf-worker/` — Cloudflare Worker executor (from #65, now consistently placed)
- `bin/orch-spawn` — now a dispatcher; add `--executor=tmux|wasm` (default: `tmux`)
- `executors/README.md` — documents the executor contract and how to add new executors
