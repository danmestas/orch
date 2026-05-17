---
name: sesh-ops-reference
description: Reference for the sesh-ops CLI's actual on-the-wire schema — field names, status enums, scope-id format requirements, JSON output shapes. Use whenever wiring sesh-ops into a script, bench, or worker adapter. Triggers on "sesh-ops command", "sesh-ops task", "sesh-ops goal", "sesh-ops scope", "what does sesh-ops emit", "what status values does a goal have", "what scope-id format is required", "how do I link a task to a goal", "sesh-ops returns nothing", "scope-id must be 8 or 32 hex", "task add doesn't seem to work", "goal state vs status". The docs in `~/projects/sesh/docs/` cover concepts; this skill covers the concrete on-the-wire surface. Pairs with `bench-docker-sesh-author` and `upstream-investigation-before-filing`.
---

# sesh-ops-reference

The reference CLI for sesh's task / goal / scoped-memory primitives. The docs at `~/projects/sesh/docs/{task-management,goal-management,scoped-memory}.md` describe the conceptual model and state machines; this skill pins the concrete surface — exact field names, enum values, scope-id format, JSON output shape — that scripts need to be correct.

The actual implementation lives at `github.com/danmestas/sesh-ops`. When in doubt, the source there is authoritative; this skill is the digest you need at hand.

## Common flag pattern

Every sesh-ops invocation needs to know where to look:

```bash
sesh-ops \
    --server=<NATS_URL>          # or --session=<label> to derive from .sesh/sessions/<label>.json
    --scope=<scope>               # default: workflow
    --scope-id=<scope-id>         # mandatory when scope is set
    <subcommand> <args>
```

A convenient bash idiom — define the prefix once, reuse:

```bash
SO=(sesh-ops --server="$NATS_URL" --scope=workflow --scope-id=cafef00d)
"${SO[@]}" task add --title=x
"${SO[@]}" task pull
```

## Scope-id format (the silent trap)

| scope | scope-id format |
|---|---|
| `workflow` | 8 OR 32 hex chars |
| `project` | project name |
| `session` | `<project>.<session>` |
| `role` | role identifier |
| `agent` | agent identifier |

Workflow scope-id validation lives at `sesh-ops/internal/scope/bucket.go`. Other formats produce an empty bucket *silently from the CLI's perspective* — the validation error ("workflow scope-id must be 8 or 32 hex chars, got N") goes to stderr, easy to miss with `2>/dev/null`.

Quick generator for bench-style IDs: any 8 hex chars work. Convention used in the docker-sesh bench is to prefix with the group number for grep-ability (`13aaaaaa`, `15ace150`).

## Subject schema (the actual one)

Sesh-ops talks to NATS using the Synadia Agent Protocol subject convention:

```
agents.{verb}.{token}.{owner}.{session}
```

- `verb`: `prompt` | `hb` | `status`
- `token`: subject-safe short ID (claude → cc; codex / pi / gemini → self)
- `owner`: usually `$USER`
- `session`: sesh session label or pane-encoded form (`pct5` for tmux pane `%5`)

## Task schema

The canonical fields the CLI emits and accepts:

```json
{
  "id": "01HXX...",
  "v": 1,
  "title": "...",
  "description": "...",
  "status": "pending",
  "puller": "role:agent-id",
  "pulled_at": "ISO8601",
  "due_at": "ISO8601",
  "depends_on": ["task-id", ...],
  "priority": 0,
  "attempts": 0,
  "max_attempts": 3,
  "created_at": "ISO8601",
  "created_by": "role:agent-id",
  "updated_at": "ISO8601",
  "result": {...} | null,
  "metadata": {"goal_id": "...", ...}
}
```

**Field name: `.status` not `.state`.** Both names appear casually in the docs; the canonical wire field is `.status`. If your script uses `.state` you'll get empty.

Status enum: `pending` | `in_progress` | `blocked` | `completed` | `failed` | `cancelled`.

State machine + pull-protocol semantics: see `~/projects/sesh/docs/task-management.md`.

## Goal schema

```json
{
  "id": "...",
  "objective": "...",
  "status": "pursuing",
  "tasks": ["task-id", ...],
  "used_tokens": 0,
  "budget_tokens": 0,
  ...
}
```

Again, **`.status` not `.state`**.

Status enum: `pursuing` | `paused` | `achieved` | `unmet` | `budget_limited`.

The `tasks[]` array is a denormalized list of linked task IDs — see "Linkage patterns" below.

## Subcommand reference

### Tasks

| command | signature | emits |
|---|---|---|
| `task add` | `--title=STRING [--description] [--depends-on=ID,...] [--priority=N] [--max-attempts=N] [--goal-id=ID] [--metadata=JSON]` | task JSON on stdout |
| `task pull` | `[--lease=DURATION]` (default 30s) | task JSON if claimed; empty if no pending |
| `task complete <id>` | `[--result=JSON]` | task JSON (status → completed) |
| `task fail <id>` | `[--result=JSON]` | task JSON (status → pending if attempts<max, else failed) |
| `task block <id>` | | task JSON (status → blocked, retains puller) |
| `task unblock <id>` | | task JSON (status → in_progress) |
| `task get <id>` | | task JSON |
| `task list` | `[--status=X] [--puller=Y] [--json]` | **table by default; `--json` for parseable** |
| `task sweep` | | reset in_progress tasks past due_at |
| `task watch` | | newline-delimited JSON event stream (tail of KV change events) |
| `task extend <id>` | | extend due_at on a held task |

### Goals

| command | signature | emits |
|---|---|---|
| `goal create` | `--objective=STRING [--budget=N]` | goal JSON |
| `goal get <id>` | | goal JSON |
| `goal status <id>` | | human-readable summary |
| `goal list` | `[--status=X] [--json]` | table by default; `--json` for parseable |
| `goal pause <id>` | | goal JSON (status → paused) |
| `goal resume <id>` | | goal JSON (status → pursuing) |
| `goal complete <id>` | `[--result=JSON]` | goal JSON (status → achieved) |
| `goal abandon <id>` | `--reason=STRING` | goal JSON (status → unmet) |
| `goal account <id> <tokens>` | positive integer | adds to used_tokens; auto budget-limits if over |
| `goal sweep` | | scan and transition budget-exhausted goals |
| `goal clear <id>` | | hard delete |

## Linkage patterns

**Task → goal.** `task add --goal-id=G_ID` does two things atomically:

1. Writes `metadata.goal_id = G_ID` on the new task.
2. Appends the new task's ID to `goal.tasks[]` on the parent goal.

Both sides are denormalized — `metadata.goal_id` is authoritative. If a goal goes away, the link survives on the task; if a task goes away, the goal's `tasks[]` entry becomes a dangling reference.

**Task → trace.** Tasks created via the shim path inherit `traceparent` headers per orch#117's envelope-headers work — propagated automatically. Manual `task add` from a script doesn't inject traceparent unless you do.

## Output-parsing recipes

Always extract via `jq -r '.field // empty'`:

```bash
SO=(sesh-ops --server="$NATS_URL" --scope=workflow --scope-id=cafef00d)
T_ID=$("${SO[@]}" task add --title=x | jq -r '.id // empty')
STATUS=$("${SO[@]}" task get "$T_ID" | jq -r '.status // empty')

if [ -z "$T_ID" ]; then
    echo "task add failed — likely invalid scope-id format"
fi
```

For task list, default output is a table:

```
TITLE       STATUS       PULLER
work-1      pending
deploy      in_progress  alice:agent-1
```

If you need structured access, add `--json`.

## Common gotchas

- `task pull` returns empty (NOT error) when no tasks pending. Check with `[ -z "$ID" ]`, not exit code.
- `task list` without `--json` is a table; `grep` works for title strings, field extraction needs `--json`.
- `goal account` requires a **positive integer**. Negative values produce a validation error to stderr.
- scope-id validation errors go to stderr. If you're debugging an unexpectedly empty response, drop `2>/dev/null` from your invocation.
- `--scope` defaults to `workflow`. If you forget to set `--scope-id`, sesh-ops errors but the message can be subtle — always set both explicitly in scripts.

## Worked examples

### Full task CAS sequence

```bash
SO=(sesh-ops --server="$NATS_URL" --scope=workflow --scope-id=cafef00d)

# Add three tasks
for i in 1 2 3; do
    "${SO[@]}" task add --title="task-$i" >/dev/null
done

# Pull them — CAS ensures distinct claims
for i in 1 2 3; do
    timeout 15 "${SO[@]}" task pull > "/tmp/pull-$i.json"
done

UNIQ=$(for i in 1 2 3; do jq -r '.id // empty' "/tmp/pull-$i.json"; done | sort -u | grep -c .)
echo "3 pulls yielded $UNIQ unique task IDs"  # expect 3
```

### Goal-task linkage with denormalized read

```bash
G_ID=$("${SO[@]}" goal create --objective="ship feature X" | jq -r '.id')
T1=$("${SO[@]}" task add --title="impl" --goal-id="$G_ID" | jq -r '.id')
T2=$("${SO[@]}" task add --title="docs" --goal-id="$G_ID" | jq -r '.id')

# Verify both sides of the link
"${SO[@]}" task get "$T1" | jq -r '.metadata.goal_id'  # → G_ID
"${SO[@]}" goal get "$G_ID" | jq -r '.tasks[]'         # → T1, T2
```

### Token accounting + budget exhaustion

```bash
G_ID=$("${SO[@]}" goal create --objective="bounded work" --budget=1000 | jq -r '.id')
"${SO[@]}" goal account "$G_ID" 600
"${SO[@]}" goal account "$G_ID" 500   # crosses budget
"${SO[@]}" goal get "$G_ID" | jq -r '.status'  # → budget_limited
```

## Cross-references

- `bench-docker-sesh-author` — for the bench wrapper that drives sesh-ops in the integration suite.
- `upstream-investigation-before-filing` — before filing "sesh-ops does X wrong", check `github.com/danmestas/sesh-ops/internal/*_test.go` to see what the upstream considers correct.
