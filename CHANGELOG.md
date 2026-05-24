# Changelog

## Unreleased

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
