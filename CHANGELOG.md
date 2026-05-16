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

### refactor: executor pluralism — `executors/<type>/` is now first-class. tmux flow moved to `executors/tmux/`. No behavior change.

- `executors/tmux/spawn.sh` — tmux-specific spawn logic extracted from `bin/orch-spawn`
- `executors/tmux/hooks/` — tmux-executor hooks moved from `hooks/` (backward-compat symlinks remain)
- `executors/tmux/legacy/` — deprecated codex-hooks, gemini-hooks, pi-extensions (one-cycle retention)
- `executors/wasm/cf-worker/` — Cloudflare Worker executor (from #65, now consistently placed)
- `bin/orch-spawn` — now a dispatcher; add `--executor=tmux|wasm` (default: `tmux`)
- `executors/README.md` — documents the executor contract and how to add new executors
