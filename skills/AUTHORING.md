# Skill authoring conventions for orch

This document is the source of truth for the shape and style of skills in `skills/`. It exists because the project went from 8 skills to 14 in one batch and we needed a single place to record what makes a skill in this repo *feel* like a skill in this repo. New skills mirror what's here. Existing skills that pre-date this doc are the empirical reference; this is what we extracted from them.

The goal is not enforcement — it's that the next orc reading skills/ shouldn't have to reverse-engineer the conventions from 14 examples to add their 15th.

## Frontmatter contract

Every skill is one directory under `skills/` with a single `SKILL.md` file. The frontmatter has exactly two required fields:

```yaml
---
name: skill-name              # kebab-case; MUST equal the directory name
description: One paragraph...  # what it does + when to use it + (often) what it pairs with
---
```

`name` exists so the model can refer to the skill by identifier; `description` is the primary triggering mechanism. The description's job is to make a Claude looking at the available-skills list decide whether to invoke this skill for the current user request.

### A good description

```yaml
description: Drive interactive AI agent CLIs (claude, pi, codex, gemini) already running in tmux panes from a parent Claude Code session, and observe their lifecycle events (turn-completion, attention-needed) without polling. Use when the user asks to "send a prompt to <agent>", "drive the <agent> pane", "ask <agent> X and bring back the answer", "broadcast a prompt to all agents and time them", "wait for <agent> to finish", "fire when claude is done", "observe stop events", "auto-approve permission prompts in another claude pane", "remote control a tmux agent", "talk to my running pi/codex/gemini/claude", "wake me when <agent> finishes", or any variation involving sending prompts to and reading replies from agents already running in tmux panes. Pairs with `tmux-agent-panes` (which spawns and lays out the panes); this skill is for the after-spawn phase of orchestration.
```

What makes it good:
- One concrete sentence on what the skill does.
- A long, explicit list of phrases an operator might actually say. Quoted, in the active voice the operator uses.
- A "Pairs with X" cross-reference that disambiguates from a related skill.
- Closes the loop on edge cases ("or any variation involving...").

### A bad description

```yaml
description: Skill for driving agents.
```

What's wrong:
- No triggers. The model has no idea when to invoke.
- "Driving agents" could mean a dozen things; collides with `orch-suiting` and `tmux-agent-panes`.
- No pairing or scope info.

## Trigger discipline

Triggers go inside the description as quoted phrase lists — "Use when the user asks to X, Y, Z". The principle: list the *phrases an operator types*, not the *categories* of things they might want.

Rules:

- **Phrases, not categories.** "User wants to debug a worker" is too abstract. "the bench timed out", "shim isn't responding", "my Group N is failing" are right.
- **Cover the active and passive forms.** "drive the pane" + "drive a pane" + "drive the <agent> pane".
- **Include the natural ways operators describe the same thing.** "wake me when claude finishes" and "fire when claude is done" both belong if the skill covers that case.
- **Negative triggers when ambiguous.** `assume-orch` says "Do NOT trigger on mentions of spawning workers" so it doesn't compete with `orch-driver`. Use this pattern when two skills are close.

Lengths: most skills list 5–12 triggers. `orch-driver` has 12, `goal-complete` has gated phrases that double as triggers ("I think we're done", "objective met"), `tmux-agent-panes` has spatial phrases ("open X to the right", "anchor my pane on top").

## Body conventions

After the frontmatter:

1. **`# Skill name`** as the H1.
2. **Optional "As of #N" version marker** for skills that have pivoted (see `orch-driver`'s post-#94 header).
3. **One short summary paragraph** — what the skill does in 2–3 sentences. The reader should know in 30 seconds whether they're in the right place.
4. **Sections** organized by use rather than by topic:
   - Tools/commands table where the skill covers a CLI surface (`orch-driver` is the model)
   - Decision trees / "X → Y" tables for "pick the right thing"
   - Recipes (paste-and-run shell or code blocks)
   - Gotchas / known hazards
   - Examples
5. **Cross-references** to other skills via "Pairs with X" or links to `skills/<x>/SKILL.md`.
6. **Paste-and-run code blocks** where possible — operators will copy them.

## Length norms

| existing skill | lines |
|---|---:|
| new-claude-window | 74 |
| assume-orch | 99 |
| goal-complete | 100 |
| orch-suiting | 104 |
| migrating-to-synadia | 137 |
| release-watch | 173 |
| tmux-agent-panes | 268 |
| orch-driver | 412 |

Aim for ~150 lines for a focused skill. Go to 300+ only when covering multiple coordinated tools (orch-driver's family) or large state-machines (tmux-agent-panes). If you cross 400, either the skill is doing too much (split it) or the reference material belongs in `docs/` (move it).

## When NOT to write a skill

A skill is action-oriented — "use when user wants to do X". If the content is reference material consumed by a human reader (architecture decisions, spec edges, message-envelope formats), prefer `docs/` over `skills/`.

Two heuristics:

- *"Will Claude do something different after invoking?"* → skill.
- *"Will a human read this once and then know?"* → docs.

Examples that are docs, not skills:
- Synadia spec edges → already in `docs/orch-agent-shim.md`; expand there.
- Sesh dialect vs Synadia → already in `docs/working-with-sesh.md`.
- Tmux/orch-spawn implementation details → inline `cmd/orch/spawn.go` + `cmd/orch/spawn_tmux.go` comments.

## PR-review checklist (for the skill-reviewer agent)

- [ ] Frontmatter has `name` (kebab-case, matches dir) and `description` (paragraph with embedded trigger list).
- [ ] At least 5 explicit trigger phrases in the description.
- [ ] No competing triggers with existing skills, or an explicit `Do NOT trigger on...` exclusion.
- [ ] Body has a summary paragraph + at least one tools/recipes/examples section.
- [ ] Cross-references resolve (links to other skills or docs exist).
- [ ] Length appropriate for scope (~150 average; 50–400 acceptable).
- [ ] Examples are paste-and-run.
- [ ] Domain-specific facts (field names, status enums, format strings) link to or quote the source they're derived from, so they don't decay silently.

## When the body starts to drift

Three signs the skill needs a rewrite:

- The summary paragraph no longer matches what the body covers. Either narrow the scope or rewrite the summary.
- Operators are invoking the skill for things it doesn't cover. Either expand or file a sibling skill.
- Operators aren't invoking it when they should. Triggers are too narrow; widen them or add explicit phrases the operator actually uses.

The `skill-creator` plugin has a `--description-optimization` mode that runs an A/B benchmark on trigger phrases. Use it when triggering is the bottleneck.
