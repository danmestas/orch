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
npm install -g @agent-ops/orch

# Optional — outfit support (config-as-code for workers):
npm install -g @agent-ops/suit
```

If you've set `--ignore-scripts`, run `orch-setup` afterwards to wire hooks and skills. `npm uninstall -g @agent-ops/orch` cleans up.

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
- **Human-in-the-loop only when it matters.** Four-axes classification — taste / architecture / ethics / reversibility — surfaces the decisions you actually need to make. Everything else, the agents decide.
- **Per-harness configuration.** Hooks for event-driven side effects, skills for loaded behavior, outfits for config-as-code, runtime redirection when a harness wanders.
- **Any topology.** Direct pane-to-pane addressing supports orchestrator/worker, full mesh, hierarchical, ring, or ad-hoc. Topology is your policy, not the substrate's.
- **Dark-mode capable.** The same primitives that drive you-in-the-loop work also run unattended once the classifier and audit log are calibrated.

## Why it's cool

- **Lightweight.** Bash + tmux + a few short scripts. No daemons. No containers required. No orchestration runtime to maintain.
- **Extensible.** Every feature beyond the core primitives is opt-in. Want isolation? Wrap a worker's spawn in `docker run`. Want a different classifier policy? Drop in your own. The substrate doesn't enforce.
- **Visible.** Workers live in tmux panes you can see, attach to, and physically interrupt. No black-box container logs to grep through.
- **Composable.** Orch sits above whatever substrate each worker runs on. Containerized agent pipeline? Run those container shells in orch's panes. Wasm sandboxes? Same. Raw shells? Default.

## How it works

Behind the scenes, each worker is a tmux pane running a full agent CLI with an optional outfit — a config-as-code bundle that ships system prompt, tool allowlist, skills, hooks, and model choice as one versioned artifact. Workers are addressable by pane id; you (or any other harness with addressing rights) send prompts, wait for completion, and survey activity. Hooks fire on every Stop and Notification event, writing markers an always-on listener picks up. Each harness carries a role tag — worker / observer / operator — so the substrate knows who can interrupt whom.

You don't drive any of this manually. You describe what you want; orch's installed skill suite handles the spawning, the addressing, the listening, and the escalation surface.

## Dark factory automations

The same primitives that drive you-in-the-loop work also run unattended. Calibrate three things:

- **An escalation classifier** that fires only on the four axes
- **An audit log** capturing every decision for replay
- **A principal agent** trained on your past ratifications

...and you drop out of the loop. Orch keeps running. The substrate is the same artifact whether you're in the loop or not — the difference is who signs each escalation.

## License

[Apache License 2.0](./LICENSE).
