---
name: writing-plans
description: Author a structured plan before kicking off implementation — goal, success criteria, decomposition, dependencies, risks, validation. Use when the user says "make a plan for X", "plan the refactor", "write a plan before we start", "draft a plan", "let's plan this out", "what's the plan", "before you start, write the plan", "plan this issue", "plan the migration", "draft the approach", or any variation asking for a written plan before code lands. Also use proactively before dispatching builders on a multi-step change that doesn't yet have a written plan attached to the issue/PR/scratch dir. Pairs with `to-issues` (which slices a plan into independently-grabbable tickets) and `ship-issue` (which executes one slice end-to-end); this skill produces the plan that those skills consume.
---

# writing-plans

Produce a written plan before code starts. A plan is a short, opinionated document that names the goal, the chosen approach, what "done" looks like, and the risks worth flagging. It exists so the operator, the builder(s), and you can converge on the shape of the work *before* a diff makes the decision irreversible.

## When to write one

- Multi-step change touching ≥3 files or ≥2 components.
- Anything that would be slicable into ≥2 issues (then pair with `to-issues`).
- Refactors where the end-state isn't obvious from the starting point.
- New features whose acceptance shape would surprise someone who only read the issue title.

Skip the plan for: single-file fixes, mechanical renames, dependency bumps, anything the issue itself fully specifies.

## Where the plan lives

Pick the lightest location that survives long enough:

| Lifetime needed | Location |
|---|---|
| One conversation | inline in the chat |
| Until the PR merges | `.scratch/plan-<slug>.md` (gitignored or PR-committed) |
| Across multiple PRs | issue body or a pinned comment on the tracking issue |
| Long-lived design decision | `docs/adr/NNNN-<slug>.md` (and link back from the issue) |

Default to a `.scratch/plan-<slug>.md` file when the plan is more than ~10 lines or will be referenced by builders. Link the plan from the issue/PR body.

## Required sections

A plan has these sections in this order. Skip none. Use H2 (`##`) for each.

1. **Problem** — one paragraph. What's broken, missing, or unclear. Name the *symptom* the operator/users see, not the proposed fix.
2. **Approach** — the shape of the solution in 1–3 paragraphs. State the choice and *why this over the obvious alternative*. If there are forks worth flagging, name them and pick one.
3. **Decomposition** — bulleted list of the concrete steps / sub-tasks. Each step should be small enough to fit in one PR. If a step has dependencies on another step, note it inline.
4. **Acceptance criteria** — bulleted list of *observable* outcomes that prove the plan succeeded. "Tests pass" is too weak; "running `foo --bar` on a fresh checkout prints `OK` and exits 0" is right.
5. **Risks & non-goals** — what could go wrong (rollback shape, dependency surprises, perf regressions) and what the plan explicitly does NOT cover so reviewers don't ask.
6. **Open questions** *(optional)* — only if there are decisions the operator needs to weigh in on before execution. Empty section = delete it; don't leave it as "none."

## Template (paste-and-fill)

```markdown
# Plan: <one-line goal>

Issue: #NNN | PR: #MMM | Date: YYYY-MM-DD

## Problem

<One paragraph stating the symptom and why it matters now.>

## Approach

<1–3 paragraphs. State the shape of the fix and why this over alternatives.>

## Decomposition

- [ ] Step 1 — <verb-first description>
- [ ] Step 2 — <verb-first description> (depends on Step 1)
- [ ] Step 3 — <verb-first description>

## Acceptance criteria

- <Observable outcome 1, e.g. `cmd --flag` exits 0 and prints expected text>
- <Observable outcome 2, e.g. CI workflow `validate.yml` goes green>
- <Observable outcome 3, e.g. existing test suite still passes with no skips added>

## Risks & non-goals

- Risk: <what could break, and the rollback path>
- Non-goal: <scope explicitly excluded so reviewers don't ask>
```

## Anchoring

Always wire the plan back into the rest of the tracking surface:

- Link from the issue body: `Plan: [.scratch/plan-foo.md](...)` or paste the plan inline if short.
- Reference the plan in the PR body under a `## Plan` heading or link to the file.
- When the plan gets sliced via `to-issues`, each child issue links back to the parent plan in its body.

## Updating the plan

A plan is a living doc until the work ships. When reality diverges:

- **Approach changed mid-flight** → edit the Approach section and add a one-line "Revision YYYY-MM-DD: <what changed and why>" at the bottom of that section. Do not silently rewrite history.
- **A step turned out to be unnecessary** → strike it through (`~~Step N~~`) rather than deleting, so the plan still maps to the commit log.
- **A new risk surfaced** → add it to Risks. Don't bury it in the PR description hoping reviewers spot it.

When the PR merges, the plan is archive material. Keep it for ADR-style decisions; delete `.scratch/plan-*.md` for tactical ones.

## When NOT to use this skill

- The operator wants direct execution, not a plan ("just fix it", "small change", "one-liner"). Don't pile on process.
- A plan already exists for this work — reference it instead of authoring a duplicate.
- The work belongs in an ADR (long-lived architectural decision) — use the ADR template instead; this skill is for tactical plans.

## See also

- `to-issues` — slice a plan into independently-grabbable tickets.
- `ship-issue` — drive one sliced issue from triage to merged PR.
- `grill-me` — stress-test the plan's premises before committing to it.
