# orch

By Daniel Mestas

A lightweight, extensible substrate for autonomous long-running multi-agent coordination on tmux.

Each pane orch spawns speaks the [Synadia Agent Protocol v0.3](docs/synadia-comparison.md)
on NATS. `orch-spawn` launches an [`orch-agent-shim`](docs/orch-agent-shim.md) sibling that
registers the pane on `$SRV.INFO.agents` and serves prompts at
`agents.prompt.<token>.<owner>.<pane>`. `orch-tell` and `orch-ask` route through discovery;
operator UX is unchanged, the wire under it is standard.

## Prerequisites

- **tmux** (3.0+) — `brew install tmux` (macOS) or `apt install tmux` / `dnf install tmux` (Linux)
- **fswatch, jq** — `brew install fswatch jq` or equivalent
- **Node + npm** — delivery channel
- **Claude Code** — your operator surface

## Install

```bash
# 1. Install the package (binaries on PATH; postinstall symlinks hooks + skills):
npm install -g @agent-ops/orch

# 2. Complete setup — verifies deps, registers hooks in ~/.claude/settings.json:
orch up

# Optional — outfit support (config-as-code for workers):
npm install -g @agent-ops/suit
```

`orch up` is idempotent. It checks runtime deps, runs the postinstall symlink farm (in case `--ignore-scripts` was used), and merges Claude Code's Stop/Notification hook registrations into `~/.claude/settings.json` (with a timestamped backup).

To tear it down:

```bash
orch down                            # remove install state on this machine
npm uninstall -g @agent-ops/orch     # remove the package itself
```

`orch down` confirms before destructive ops, backs up `settings.json`, accepts `--dry-run` / `--keep-state` / `--yes` flags.

## Driving it

Open Claude Code in your project, inside a tmux session. Then just say what you want:

> *"Open a pi harness with full-cycle engineering below me in `<project>`. Open a spy for it and have it recommend improvements."*
>
> *"Spawn a backend engineer for the auth-flow feature."*
>
> *"Have a researcher dig into how Stripe handles webhook retries and brief the planner."*
>
> *"Split this feature into parallel tasks across three engineers."*
>
> *"Fire a reviewer on the last commit and only escalate if it tripped reversibility."*
>
> *"Audit the session running below me for skill-trigger gaps."*

That's it. Orch handles the spawning, addressing, prompting, listening, and escalation surface. You stay in chat.

## What it gives you

- **Full harnesses, not subagents.** Each worker is an independent Claude Code (or pi / codex / gemini) instance with its own context, lifecycle, and configuration. Subagents share their parent's turn and budget; full harnesses run independently for hours.
- **Long-running discipline.** Autonomous agents drift. Tell + listen + ask cycles keep them on task across many turns.
- **Long-horizon goals via sesh substrate.** Optional goal-management integration (`orch-goal-pursue`, `orch-goal-status`, Stop/SessionStart hooks, `goal-complete` skill) wraps sesh's goal protocol so a single objective survives turn boundaries, conversation compaction, and cold starts. See the goal-harness section below.
- **Human-in-the-loop only when it matters.** Four-axes classification — taste / architecture / ethics / reversibility — surfaces the decisions you actually need to make. Everything else, the agents decide.
- **Per-harness configuration.** Hooks for event-driven side effects, skills for loaded behavior, outfits for config-as-code, runtime redirection when a harness wanders.
- **Any topology.** Direct pane-to-pane addressing supports orchestrator/worker, full mesh, hierarchical, ring, or ad-hoc. Topology is your policy, not the substrate's.
- **Dark-mode ready (roadmap).** The same primitives are designed to run unattended once a calibrated classifier and audit log are in place. Those pieces aren't shipped yet — today, orch is operator-in-the-loop.

## Why it's cool

- **Lightweight.** Bash + tmux + a few short scripts. No daemons. No containers required. No orchestration runtime to maintain.
- **Extensible.** Every feature beyond the core primitives is opt-in. Want isolation? Wrap a worker's spawn in `docker run`. Want a different classifier policy? Drop in your own. The substrate doesn't enforce.
- **Visible.** Workers live in tmux panes you can see, attach to, and physically interrupt. No black-box container logs to grep through.
- **Composable.** Orch sits above whatever substrate each worker runs on. Containerized agent pipeline? Run those container shells in orch's panes. Wasm sandboxes? Same. Raw shells? Default.

## How it works

Behind the scenes, each worker is a tmux pane running a full agent CLI with an optional outfit — a config-as-code bundle that ships system prompt, tool allowlist, skills, hooks, and model choice as one versioned artifact. Workers are addressable by pane id; you (or any other harness with addressing rights) send prompts, wait for completion, and survey activity over the bus introduced above. The shim publishes typed chunks (response, thinking, tool_use, status) on each prompt's reply subject and heartbeats on `agents.hb.<token>.<owner>.<pane>`; subscribe with `nats sub 'agents.>'` (wrap in a Monitor for live streams). Each harness carries a role tag — worker / observer / operator — surfaced in the shim's `$SRV.INFO.agents` metadata so subscribers know who can interrupt whom.

You don't drive any of this manually. You describe what you want; orch's installed skill suite handles the spawning, the addressing, the listening, and the escalation surface.

## Environment variables

Orch's binaries honor a small set of env vars. Defaults are sensible; override only when needed.

| Variable | Consumer | Purpose |
| --- | --- | --- |
| `ORCH_SESH_BIN` | `orch-spawn` | Absolute path to the `sesh` binary for `--sesh-session` resolution (default: first `sesh` on PATH). Must be absolute; orch-spawn `cd`s during resolution, so a relative path can break silently. Bad / missing path → orch-spawn errors before spawning any pane. |
| `ORCH_PROJECTS_ROOT` | `orch-spawn` | Fallback root for `--project <name>` when zoxide misses (default: `$HOME/projects`). |
| `ORCH_VERIFY_TIMEOUT` | `orch-spawn` | Readiness poll budget in seconds for `--verify` (default: `60`). |
| `ORCH_HEADLESS_SESSION` | `orch-spawn` | Name of the detached tmux session for `--headless` (default: `orch-headless`). |
| `ORCH_WORKTREE_ROOT` | `orch-spawn` | Directory under which `--worktree-from <sha>` creates new worktrees (default: `${ORCH_PROJECTS_ROOT:-$HOME/projects}/<repo>-worktrees/`). Combined with `--slug <name>`, the full path is `<root>/<slug>`. |
| `ORCH_ALIASES_FILE` | `orch-spawn` | Alias file written by `--slug <name>` (default: `~/.config/orch-aliases`). Each spawn with `--slug` appends a `<slug>=<pane_id>` line so other harnesses can resolve workers by name without an active bus subscription. |
| `NATS_URL` | shim, `orch-tell`, `orch-ask` | NATS connect URL (default: client-library default, typically `nats://127.0.0.1:4222`). |
| `SESH_GOAL_ID` / `SESH_GOAL_SCOPE` / `SESH_GOAL_SCOPE_ID` | `orch-spawn`, goal-harness | Propagated into spawned panes so sesh-goal hooks activate. Usually set by `orch-goal-pursue`. |

## Goal-harness (optional sesh integration)

The goal-harness is orch's reference implementation of the [sesh goal-management spec](https://github.com/danmestas/sesh/blob/main/docs/goal-management.md). It wraps sesh's substrate-side goal records (token + wall-clock budgets, completion audit, hierarchical decomposition) with the harness-side discipline the spec calls out as non-substrate concerns: continuation, token accounting, context injection, completion verification.

**Components shipped in this repo:**

| Component | Path | Purpose |
| --- | --- | --- |
| `orch-goal-pursue` | `bin/` | bootstrap a long-horizon goal: create the record, print env exports, launch daemon |
| `orch-goal-status` | `bin/` | inspect the current goal + linked tasks + budget burn + daemon status |
| `orch-goal-stop-account-daemon` | `cmd/` | long-running binary: account tokens via Synadia §6.5 terminators across all harnesses |
| `orch-goal-stop-account.sh` | `hooks/` | **DEPRECATED** — Claude Code Stop hook; superseded by the daemon above |
| `orch-goal-session-context.sh` | `hooks/` | SessionStart hook: inject goal state into context on resume |
| `goal-complete` skill | `skills/` | model-facing audit checklist before submitting `goal complete` |
| `orch-goal-continuation.md` | `references/` | reference prompt the spec calls "harness-side" |

**Prerequisites** (beyond orch's defaults):
- A running [sesh](https://github.com/danmestas/sesh) hub (auto-spawned by `sesh up` in a project worktree).
- [`sesh-ops`](https://github.com/danmestas/sesh-ops) on `$PATH`.

**Typical flow:**

```bash
# 1. Start a long-horizon goal in your project:
orch-goal-pursue "Implement OAuth2 migration for the auth service" --budget=200000

# 2. The command prints export lines AND launches the token-accounting daemon:
#
#   Goal created: 01HXX...
#   Daemon:       started (pid=12345, log=/tmp/orch-goal-daemon-01HXX....log)
#
#   export SESH_GOAL_ID=01HXX...
#   export SESH_GOAL_SCOPE=project
#   export SESH_GOAL_SCOPE_ID=auth_service

# 3. Run the export lines in your shell (or let the agent run them).

# 4. Open any harness — Claude Code, codex, pi, gemini, or any shim-wrapped
#    agent. The daemon subscribes to Synadia §6.5 terminator events on
#    agents.prompt.*.*.*.> and accounts ~5k tokens per turn automatically,
#    across all four harnesses. No per-harness Stop hook required.

# 5. Work. Across many turns. Across session resumes. Across harness switches.
#    The goal record in sesh's hub remembers what you're pursuing.

# 6. When the audit completes, Claude invokes the goal-complete skill,
#    walks the 6-step checklist, then submits update_goal(complete).

# 7. Inspect goal + daemon status:
orch-goal-status
```

**Cross-harness coverage:** Token accounting is handled by `orch-goal-stop-account-daemon` — a long-running Go binary that subscribes to the NATS subject `agents.prompt.*.*.*.>` and detects §6.5 terminators (zero-byte, no-header messages). This fires once per turn regardless of which harness is running. The old `orch-goal-stop-account.sh` Stop hook was claude-code-specific and is now deprecated.

The goal-harness is **opt-in**: the daemon no-ops silently when `SESH_GOAL_ID` is unset, and the SessionStart hook injects goal state on resume. Settings-snippet.json no longer registers a goal-specific Stop hook — the daemon replaces it.

## Dark factory automations (roadmap)

The same primitives that drive you-in-the-loop work are designed to also run unattended. Three calibration surfaces are planned:

- **An escalation classifier** that fires only on the four axes
- **An audit log** capturing every decision for replay
- **A principal agent** trained on your past ratifications

Once those land, you drop out of the loop. Orch keeps running, and the substrate is the same artifact whether you're in the loop or not — the difference is who signs each escalation. None of this ships in 0.1.x; today, orch is operator-in-the-loop.

## Upstream protocol contributions

Draft proposals for extending the [Synadia Agent Protocol for NATS](https://github.com/synadia-ai/synadia-agent-sdk-docs) live in [`docs/synadia-upstream/`](./docs/synadia-upstream/). Each file is a self-contained PR draft: it cites the section it amends, shows a BEFORE/AFTER of the relevant table, and includes a worked wire example in Appendix B style.

| Draft | Amends | Topic |
| --- | --- | --- |
| [`role-metadata.md`](./docs/synadia-upstream/role-metadata.md) | §3.2 | Optional `metadata.role` — `worker / observer / operator` |
| [`outfit-metadata.md`](./docs/synadia-upstream/outfit-metadata.md) | §3.2 | Optional `metadata.outfit` + `metadata.outfit_hash` |
| [`query-attestation.md`](./docs/synadia-upstream/query-attestation.md) | §7 (new §7.4) | External query-chunk attestation for harnesses without a native attention event |
| [`placement-metadata.md`](./docs/synadia-upstream/placement-metadata.md) | Appendix (new E) | Informative placement metadata — `metadata.executor` + `metadata.host` |

## License

[Apache License 2.0](./LICENSE).
