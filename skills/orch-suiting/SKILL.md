---
name: orch-suiting
description: Use BEFORE running orch-spawn when the operator describes a worker by role/intent rather than naming an outfit directly. Translates phrases like "spawn a backend engineer", "give me a reviewer", "spy on this session", "full cycle frontend designer", "planner for X", "debugger for Y" into the right `suit` configuration (`--outfit`, `--cut`, `--accessory`...) that orch-spawn will pass to `suit prepare`. Triggers whenever an operator request to spawn a worker includes a role description, capability list, work-shape phrase ("planning", "debugging", "executing", "reviewing"), or the words "full cycle", "engineer", "designer", "reviewer", "spy", "auditor".
---

# orch-suiting

> **As of orch#94 (2026-05-16):** post-spawn driver primitives go through
> the Synadia Agent Protocol bus. Spawn-side mechanics (`orch-spawn`,
> `suit prepare`) are unchanged.

Translate operator intent into a `suit` configuration before spawning a worker. Operator describes a role; you pick the outfit + cut + accessories.

## Workflow

1. **Parse the intent.** Three things to extract:
   - **Role** (engineer / backend / frontend / bones / meta / kb / aviation / personal / stasi / generic-coder).
   - **Work-shape** (executing / debugging / planning / reviewing / focused / ops / writing / ticketing / wait-watch / design).
   - **Extra rails** explicitly requested or implied (PR policy, philosophy pack, gh-project, linear, vault, skill authoring).

2. **Match against the wardrobe.** Use the cheat-sheet below for common intents. For unfamiliar asks, use live discovery (next section) — never invent outfits.

3. **State the recommendation in one sentence + reasoning.** Example: *"Going with `--outfit backend --cut executing --accessory pr-policy` — backend matches the Go/observability work, executing for active dev, pr-policy pins the feature-branch + local-CI rails."* Lets the operator redirect cheaply.

4. **Hand off.** Run `orch-spawn claude --project <X> --outfit <O> --cut <C> [--accessory <A>...]`. Default to headed unless the operator says "headless" / "background" / "spy on" / "in the background."

## Cheat-sheet — common intents → suit config

| Operator says | Outfit | Cut | Accessories |
|---|---|---|---|
| "backend engineer" / "Go work" / "server-side" | `backend` | `executing` | `pr-policy` |
| "frontend engineer" / "UI work" / "datastar" / "shadcn" | `frontend` | `executing` | `pr-policy` |
| "bones leaf" / "swarm worker" / "parallel work on bones" | `bones` | `executing` | `pr-policy` |
| "generic engineer" / "engineer that knows discipline" | `engineer` | `focused` | `pr-policy` |
| "code work" / "language-agnostic baseline" | `code` | `focused` | (none) |
| "wardrobe author" / "skill author" / "suit work" | `meta` | `executing` | `skill-author` |
| "vault work" / "kb curation" / "obsidian notes" | `kb` | `writing` | `vault` |
| "reviewer" / "review this PR" | `engineer` | `reviewing` | `pr-policy` |
| "planner" / "design before code" | `engineer` | `planning` | `philosophy` |
| "debugger" / "hunt this bug" | `engineer` | `debugging` | `pr-policy` |
| "ticketing" / "break this into issues" | `engineer` | `ticketing` | `gh-project` (or `linear`) |
| "ops" / "incident" / "infra change" | `engineer` | `ops` | (none) |
| "spy" / "audit a session" | `stasi` | `wait-watch` | (none) |
| "writer" / "docs" / "blog" | `kb` | `writing` | (none) |
| "aviation" / "flight planning" / "NOTAMs" | `aviation` | `executing` | (none) |
| **fallback when intent unclear** | `engineer` | `focused` | `pr-policy` |

## Live discovery (when cheat-sheet doesn't fit)

Wardrobe evolves. If the operator asks for something you don't recognize, check live:

```sh
suit list outfits
suit list cuts
suit list accessories
suit show outfit <name>     # what does this outfit force-load / exclude?
suit show cut <name>        # what's the cut's prompt body?
suit show accessory <name>  # synthetic accessories wrap rules/skills/hooks
```

Any **skill, rule, hook, agent, or command name** can be used as `--accessory` via fall-through (suit treats it as a one-component accessory). So `--accessory pr-policy` works even if `pr-policy` isn't in `suit list accessories` — it's a rule wrapped synthetically.

## Disambiguation rules

- **"engineer" alone** → ask once: backend, frontend, or generic? One question, not a battery.
- **No work-shape mentioned** → default to `executing` (active development). Override only on explicit signal: "plan this" → `planning`, "review" → `reviewing`, "debug" → `debugging`, "spy" / "audit" → `wait-watch`.
- **Multiple roles in one ask** ("backend engineer who also reviews") → primary outfit, add accessories for the secondary role's signals (e.g. `--accessory pr-policy` for PR discipline).
- **Outfit doesn't exist in current wardrobe** → say so explicitly, propose closest match. Never make up an outfit name.
- **Operator names a specific accessory or skill** ("with golang-patterns", "include the philosophy pack") → pass them through verbatim as `--accessory` flags.

## State the choice — always

After picking, before running `orch-spawn`, output a single line so the operator can redirect:

> "Spawning with `--outfit backend --cut executing --accessory pr-policy`. Backend covers Go + observability + deterministic-systems; executing for active dev; pr-policy for the branch+PR rails."

Then run the spawn. Don't pile up clarifying questions when a sensible default is obvious — pick, state the reasoning, proceed.

## Concrete invocation

```sh
orch-spawn claude --project <project> --outfit <outfit> --cut <cut> [--accessory <a>]...
# headed by default; add --headless for spy/background workers
```

After spawn returns the pane id:

```sh
orch tell <pane> "<initial brief — what you want the worker to do>"
# For push-notifications on agent events, subscribe to the Synadia bus:
nats sub --raw 'agents.>' &
```

## Quirks to know

- **claude only** for now — `orch-spawn --outfit` isn't wired for codex/pi/gemini yet (they have their own suit targets but the spawn-side glue isn't verified).
- **Subs never inherit** — every spawn must specify outfit/cut explicitly. Don't assume the operator's current dressing extends to the worker.
- **Per-worker isolation is automatic** — `orch-spawn` runs `suit prepare` internally, makes the bundle the worker's cwd, and traps EXIT to clean up. Multiple workers from the same project with different outfits compose cleanly.

## When NOT to use this skill

- Operator names an outfit directly (`--outfit X`): just run `orch-spawn` with their flags, no translation needed.
- Operator wants a worker without a configuration (`--no-fleet` style, naked claude): pass through to `orch-spawn` without `--outfit`.
- Spawning the operator session itself (the orchestrator): operator sessions aren't dressed via this skill — they're started plainly. See `orch-driver` skill, "Operator vs worker sessions."
- A worker is ALREADY RUNNING and the operator wants to add a skill/hook/accessory to it mid-task: that's `suit inject`, not a respawn-with-different-outfit. See `orch-driver § Equipping a running worker via suit inject`.
