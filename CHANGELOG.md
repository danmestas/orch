# Changelog

## Unreleased

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
