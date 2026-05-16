# Changelog

## Unreleased

### refactor: executor pluralism — `executors/<type>/` is now first-class. tmux flow moved to `executors/tmux/`. No behavior change.

- `executors/tmux/spawn.sh` — tmux-specific spawn logic extracted from `bin/orch-spawn`
- `executors/tmux/hooks/` — tmux-executor hooks moved from `hooks/` (backward-compat symlinks remain)
- `executors/tmux/legacy/` — deprecated codex-hooks, gemini-hooks, pi-extensions (one-cycle retention)
- `executors/wasm/cf-worker/` — Cloudflare Worker executor (from #65, now consistently placed)
- `bin/orch-spawn` — now a dispatcher; add `--executor=tmux|wasm` (default: `tmux`)
- `executors/README.md` — documents the executor contract and how to add new executors
