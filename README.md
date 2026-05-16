# orch

By Daniel Mestas

A lightweight, extensible substrate for autonomous long-running multi-agent coordination on tmux.

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

Behind the scenes, each worker is a tmux pane running a full agent CLI with an optional outfit — a config-as-code bundle that ships system prompt, tool allowlist, skills, hooks, and model choice as one versioned artifact. Workers are addressable by pane id; you (or any other harness with addressing rights) send prompts, wait for completion, and survey activity. Hooks fire on every Stop and Notification event, writing marker files that `orch-listen` (blocking wait) and `orch-subscribe` (peer push-notifications) consume on demand — no daemon, no always-on process. Each harness carries a role tag — worker / observer / operator — so the substrate knows who can interrupt whom.

You don't drive any of this manually. You describe what you want; orch's installed skill suite handles the spawning, the addressing, the listening, and the escalation surface.

For Synadia Agent Protocol integration (every spawned pane discoverable on a NATS bus, addressable via `agents.prompt.cc.<owner>.pct<pane>`), see [`docs/orch-agent-shim.md`](docs/orch-agent-shim.md) and `orch-spawn --with-shim`.

## Goal-harness (optional sesh integration)

The goal-harness is orch's reference implementation of the [sesh goal-management spec](https://github.com/danmestas/sesh/blob/main/docs/goal-management.md). It wraps sesh's substrate-side goal records (token + wall-clock budgets, completion audit, hierarchical decomposition) with the harness-side discipline the spec calls out as non-substrate concerns: continuation, token accounting, context injection, completion verification.

**Components shipped in this repo:**

| Component | Path | Purpose |
| --- | --- | --- |
| `orch-goal-pursue` | `bin/` | bootstrap a long-horizon goal: create the record, print env exports |
| `orch-goal-status` | `bin/` | inspect the current goal + linked tasks + budget burn |
| `orch-goal-stop-account.sh` | `hooks/` | Stop hook: account ~5k tokens per turn while a goal is active |
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

# 2. The command prints export lines. Run them (or paste into your shell):
export SESH_GOAL_ID=01HXX...
export SESH_GOAL_SCOPE=project
export SESH_GOAL_SCOPE_ID=auth_service

# 3. Open Claude Code. The SessionStart hook injects the goal state into
#    your initial context. Every Stop hook accounts ~5k tokens.

# 4. Work. Across many turns. Across session resumes. The goal record in
#    sesh's hub remembers what you're pursuing.

# 5. When the audit completes, Claude invokes the goal-complete skill,
#    walks the 6-step checklist, then submits update_goal(complete).

# 6. Inspect:
orch-goal-status
```

The goal-harness is **opt-in**: hooks no-op silently when `SESH_GOAL_ID` is unset, so they don't affect normal orch sessions. Settings-snippet.json registers them under Stop and SessionStart automatically when you run `orch up`.

## Dark factory automations (roadmap)

The same primitives that drive you-in-the-loop work are designed to also run unattended. Three calibration surfaces are planned:

- **An escalation classifier** that fires only on the four axes
- **An audit log** capturing every decision for replay
- **A principal agent** trained on your past ratifications

Once those land, you drop out of the loop. Orch keeps running, and the substrate is the same artifact whether you're in the loop or not — the difference is who signs each escalation. None of this ships in 0.1.x; today, orch is operator-in-the-loop.

## License

[Apache License 2.0](./LICENSE).
